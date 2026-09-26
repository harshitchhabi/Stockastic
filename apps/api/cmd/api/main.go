// Command api runs the Stockastic platform: the REST API, the WebSocket feed, the matching engine and
// the built web app, as one process.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"stockastic/api/internal/app"
	"stockastic/api/internal/auth"
	"stockastic/api/internal/config"
	"stockastic/api/internal/httpapi"
	"stockastic/api/internal/oauth"
	"stockastic/api/internal/pgstore"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
	"stockastic/api/internal/universe"
	"stockastic/api/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stockastic:", err)
		os.Exit(1)
	}
}

// openDatabaseWithRetry opens the database, trying again with backoff for a while before giving up. Postgres on
// the same machine can be briefly unavailable for perfectly ordinary reasons — it is still starting up after a
// reboot, a package update restarted it, or the machine hiccuped — and none of that should cost the event a full
// stop. Without this, each attempt would fail at once and burn through systemd's limited restart budget
// (deploy/stockastic.service) in well under a minute, after which the whole platform would sit dead until an
// organiser noticed and ran it by hand. Retrying here instead means an ordinary bounce is invisible to everyone
// but the log, and only a genuinely broken database (or a typo in DATABASE_URL) exhausts the budget and needs a
// person.
func openDatabaseWithRetry(ctx context.Context, log *slog.Logger, opt pgstore.Options) (*pgstore.Log, error) {
	return openDatabaseRetrying(ctx, log, opt, 3*time.Minute)
}

func openDatabaseRetrying(ctx context.Context, log *slog.Logger, opt pgstore.Options, retryFor time.Duration) (*pgstore.Log, error) {
	deadline := time.Now().Add(retryFor)
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		l, err := pgstore.Open(ctx, opt)
		if err == nil {
			if attempt > 1 {
				log.Info("connected to the database", "attempts", attempt)
			}
			return l, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("after retrying for %s: %w", retryFor, err)
		}
		log.Warn("could not reach the database yet; retrying", "err", err, "attempt", attempt, "givingUpIn", time.Until(deadline).Round(time.Second).String())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func level(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func run() error {
	if err := config.LoadEnvFile(".env.local"); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level(cfg.LogLevel)}))
	slog.SetDefault(log)

	rb, err := rulebook.LoadOrDefault(cfg.RulebookPath)
	if err != nil {
		return fmt.Errorf("rulebook: %w", err)
	}
	source := "embedded default"
	if cfg.RulebookPath != "" {
		source = cfg.RulebookPath
	}

	companies := universe.Default()
	if cfg.UniversePath != "" {
		if companies, err = universe.Load(cfg.UniversePath); err != nil {
			return err
		}
	} else {
		log.Warn("using the PLACEHOLDER company list; set UNIVERSE_PATH to the real one", "companies", len(companies))
	}

	scenario := sim.DefaultScenario()
	if cfg.ScenarioPath != "" {
		if scenario, err = sim.Load(cfg.ScenarioPath); err != nil {
			return err
		}
	} else {
		log.Warn("using the PLACEHOLDER price simulation (a plain random walk, no market events); set SCENARIO_PATH to the real scenario")
	}

	// The local disk still matters even when PostgreSQL holds the durable record: logs, the OS, and the
	// server's own temporary files live here, and a machine with no free disk space fails in surprising ways.
	// So the guard is always on cfg.DataDir; Postgres additionally has its own disk, watched on the database
	// host (see docs/deployment.md).
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating the data directory: %w", err)
	}
	disk := &store.DiskGuard{Dir: cfg.DataDir, MinFree: uint64(cfg.DiskMinFreeMB) << 20}

	var (
		wal     store.Log
		health  func() error
		history store.History
		closeDB = func() {}
	)
	if cfg.DatabaseURL != "" {
		// PostgreSQL is the durable record. Records are stored before they are acknowledged; the lookup tables
		// (wallet history, activity, trades and so on) are filled from it in the background.
		pg, err := openDatabaseWithRetry(context.Background(), log, pgstore.Options{
			DSN: cfg.DatabaseURL, Rules: app.CompactRules(), Log: log,
			OnBroken: func(err error) {
				// The database stopped taking writes (or another server took over). Stop at once so nothing is
				// acknowledged that is not stored; the service manager restarts this process, which replays.
				log.Error("the database is no longer accepting writes; stopping", "err", err)
				time.AfterFunc(2*time.Second, func() { os.Exit(3) })
			},
		})
		if err != nil {
			return fmt.Errorf("opening the database: %w", err)
		}
		if pg.Compacted > 0 {
			log.Info("database record tidied", "droppedRecords", pg.Compacted)
		}
		proj, err := pgstore.NewProjector(context.Background(), cfg.DatabaseURL, log)
		if err != nil {
			_ = pg.Close()
			return fmt.Errorf("opening the lookup tables: %w", err)
		}
		proj.Start()
		wal, health, history = pg, pg.Healthy, proj.History()
		closeDB = proj.Stop
		log.Info("using PostgreSQL as the durable record")
	} else {
		// Tidy the log before opening it: drop records a newer one replaced, so restarting any number of times
		// never lets it grow without bound. Trades and the audit log are always kept in full.
		walPath := filepath.Join(cfg.DataDir, "stockastic.wal")
		if res, err := store.Compact(walPath, app.CompactRules()); err != nil {
			return fmt.Errorf("compacting the data log: %w", err)
		} else if res.Dropped > 0 {
			log.Info("data log compacted", "droppedRecords", res.Dropped, "keptRecords", res.Kept, "beforeMB", res.BeforeBytes>>20, "afterMB", res.AfterBytes>>20)
		}
		f, err := store.OpenFile(walPath)
		if err != nil {
			return fmt.Errorf("opening the data log: %w", err)
		}
		if f.Torn > 0 {
			log.Warn("discarded an unfinished final record from the data log (an unacknowledged write before a crash)", "bytes", f.Torn)
		}
		wal = f
	}
	defer closeDB()

	signer, err := auth.NewSigner([]byte(cfg.JWTSecret), cfg.TokenTTL)
	if err != nil {
		return err
	}
	a, err := app.New(app.Config{
		Rulebook: rb, Log: log, WAL: wal, Universe: companies, Scenario: scenario, Signer: signer,
		Disk: disk, Track: cfg.DatabaseURL != "", History: history, Health: health,
		AllowSignup: cfg.AllowSignup, AllowedOrigins: cfg.AllowedOrigins, Autostart: cfg.Autostart,
		MaxAccounts: cfg.MaxAccounts, SignupCode: cfg.SignupCode, MaxSockets: cfg.MaxSockets, MaxSocketsPerAccount: cfg.MaxSocketsPerAccount,
	})
	if err != nil {
		_ = wal.Close()
		return err
	}
	if err := a.SeedAdmin(cfg.AdminEmail, cfg.AdminPassword, cfg.AdminName); err != nil {
		_ = wal.Close()
		return err
	}
	if err := a.Start(); err != nil {
		_ = wal.Close()
		return err
	}

	var static fs.FS
	if cfg.WebDir != "" {
		static = os.DirFS(cfg.WebDir)
	} else if e, err := webui.Embedded(); err == nil {
		static = e
	}
	handler, err := httpapi.New(httpapi.Options{App: a, Log: log, RulebookSource: source, Static: static, TrustedProxies: cfg.TrustedProxies,
		Google:         oauth.New(oauth.Config{ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, RedirectURL: cfg.GoogleRedirectURL, AllowedDomains: cfg.GoogleAllowedDomains}),
		GoogleRedirect: cfg.GoogleRedirectURL, StateKey: []byte("oauth-state:" + cfg.JWTSecret)})
	if err != nil {
		_ = a.Close(context.Background())
		return err
	}
	srv := &http.Server{
		Addr: cfg.Addr, Handler: handler,
		// Slow or half-finished requests are dropped: headers within 10 s, the whole request within 30 s, a reply
		// within 30 s (a client that will not read cannot hold a connection open), and small headers.
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 16 << 10,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "rulebook", rb.Version, "companies", len(companies), "signup", cfg.AllowSignup)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = a.Close(context.Background())
			return err
		}
	case s := <-stop:
		log.Info("shutting down", "signal", s.String())
	}

	// Stop taking new requests, let in-flight ones finish, then drain matching and close the log.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	if err := a.Close(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped cleanly")
	return nil
}
