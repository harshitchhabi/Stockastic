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
	"stockastic/api/internal/rulebook"
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

	wal, err := store.OpenFile(filepath.Join(cfg.DataDir, "stockastic.wal"))
	if err != nil {
		return fmt.Errorf("opening the data log: %w", err)
	}
	if wal.Torn > 0 {
		log.Warn("discarded an unfinished final record from the data log (an unacknowledged write before a crash)", "bytes", wal.Torn)
	}

	signer, err := auth.NewSigner([]byte(cfg.JWTSecret), cfg.TokenTTL)
	if err != nil {
		return err
	}
	a, err := app.New(app.Config{
		Rulebook: rb, Log: log, WAL: wal, Universe: companies, Signer: signer,
		AllowSignup: cfg.AllowSignup, AllowedOrigins: cfg.AllowedOrigins, Autostart: cfg.Autostart,
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
	handler, err := httpapi.New(httpapi.Options{App: a, Log: log, RulebookSource: source, Static: static})
	if err != nil {
		_ = a.Close(context.Background())
		return err
	}
	srv := &http.Server{
		Addr: cfg.Addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
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
