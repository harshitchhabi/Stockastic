// Package httpapi is the REST surface (Gin). It only translates requests into calls on app.App and
// results into JSON; every rule lives in app or below.
package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"stockastic/api/internal/app"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/funds"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/oauth"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
	"stockastic/api/internal/webui"
)

const (
	maxBody     = 64 << 10
	ctxUser     = "user"
	requestWait = 8 * time.Second
)

type Options struct {
	App *app.App
	Log *slog.Logger
	// RulebookSource describes where the rulebook came from (shown on the organiser Rulebook page).
	RulebookSource string
	// TrustedProxies are the addresses whose X-Forwarded-For header is believed (the reverse proxy in front of the
	// server). Empty means the header is ignored and the connection's own address is used.
	TrustedProxies []string
	// Google, if set, turns on "Continue with Google". StateKey signs its round trip (any secret of 32+ bytes).
	Google *oauth.Client
	// GoogleRedirect is the registered return address; the cookie is marked Secure when it is https.
	GoogleRedirect string
	StateKey       []byte
	// Static, if set, is served for every path that is not an API or WebSocket path (the built web app).
	Static fs.FS
}

type Server struct {
	a      *app.App
	log    *slog.Logger
	opt    Options
	logins *loginGuard
	lim    *limits
	google *googleFlow
}

// New builds the router.
func New(opt Options) (http.Handler, error) {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{a: opt.App, log: opt.Log, opt: opt, logins: newLoginGuard(), lim: newLimits()}
	if opt.Google != nil {
		key := opt.StateKey
		if len(key) < 32 {
			return nil, errors.New("httpapi: Google sign-in needs a StateKey of at least 32 bytes")
		}
		s.google = newGoogleFlow(key, opt.GoogleRedirect)
	}
	r := gin.New()
	if err := r.SetTrustedProxies(opt.TrustedProxies); err != nil {
		return nil, err
	}
	r.HandleMethodNotAllowed = true
	r.Use(s.recovery(), s.requestLog(), securityHeaders(), s.ipGate())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/readyz", s.ready)
	r.GET("/ws", func(c *gin.Context) { s.a.Hub.Serve(c.Writer, c.Request) })

	api := r.Group("/api")
	api.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"signupOpen": s.a.SignupOpen(), "signupNeedsCode": s.a.SignupCode() != "", "signupListed": s.a.AllowlistCount() > 0, "googleEnabled": s.opt.Google != nil})
	})
	s.googleRoutes(api)
	api.POST("/auth/signup", s.signup)
	api.POST("/auth/login", s.login)

	me := api.Group("", s.requireUser, s.accountGate)
	me.GET("/auth/me", s.me)
	me.GET("/config", s.config)
	me.GET("/symbols", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Companies()) })
	me.GET("/symbols/:symbol/history", s.history)
	me.GET("/portfolio/me", s.portfolio)
	me.POST("/trades", s.trade)
	me.GET("/trades/mine", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.MyTrades(s.a.AcctOf(user(c)))) })
	me.GET("/leaderboard", s.leaderboard)
	me.GET("/news", s.news)
	me.POST("/disputes", s.raiseDispute)
	me.GET("/disputes/mine", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.TicketsOf(user(c).ID)) })

	adm := api.Group("/admin", s.requireUser, s.accountGate, requireAdmin)
	adm.GET("/overview", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Overview()) })
	adm.GET("/systems", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Systems()) })
	adm.GET("/accounts", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AdminAccounts()) })
	adm.GET("/news", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.NewsDesk()) })
	adm.GET("/disputes", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AdminTickets()) })
	adm.GET("/audit", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AuditLog()) })
	adm.GET("/rulebook", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.RulebookView(s.opt.RulebookSource)) })
	adm.GET("/sim", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.SimStatus()) })
	s.fundRoutes(me, adm)
	s.privilegeRoutes(adm)
	adm.GET("/standings", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Leaderboard()) })

	adm.POST("/clock/start", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockStartAt(u, b.BlockID) }))
	adm.POST("/clock/pause", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockPause(u, b.Reason) }))
	adm.POST("/clock/resume", s.act(func(u app.User, b body, _ *gin.Context) error {
		return s.a.ClockResume(u, b.Reason, b.CompressBlockID)
	}))
	adm.POST("/clock/nudge", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockNudge(u, b.Reason, b.Minutes) }))
	adm.POST("/clock/jump", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockJump(u, b.Reason, b.BlockID) }))
	adm.POST("/control/freeze", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.SetFrozen(u, b.Reason, b.Frozen) }))
	adm.POST("/control/market", s.act(func(u app.User, b body, _ *gin.Context) error {
		o, err := b.override()
		if err != nil {
			return err
		}
		return s.a.SetMarketOverride(u, b.Reason, o)
	}))
	adm.POST("/control/windows/:i", s.act(func(u app.User, b body, c *gin.Context) error {
		i, err := strconv.Atoi(c.Param("i"))
		if err != nil || i < 0 || i >= s.a.Clock.WindowCount() {
			return &app.BadRequest{Code: "unknown_window", Message: "There is no such allocation window."}
		}
		o, err := b.override()
		if err != nil {
			return err
		}
		return s.a.SetWindowOverride(u, b.Reason, i, o)
	}))
	adm.POST("/accounts/:id/promote", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Promote(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/warn", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Warn(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/disqualify", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Disqualify(u, b.Reason, c.Param("id")) }))
	adm.POST("/news", s.act(func(u app.User, b body, _ *gin.Context) error {
		return s.a.PublishNews(u, b.Reason, b.Kind, b.Headline, b.Body)
	}))
	adm.POST("/disputes/:id/resolve", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.ResolveTicket(u, b.Reason, c.Param("id")) }))
	adm.POST("/grants", func(c *gin.Context) {
		var in struct {
			Reason    string  `json:"reason"`
			AccountID string  `json:"accountId"`
			Symbol    string  `json:"symbol"`
			Qty       int64   `json:"qty"`
			Price     float64 `json:"price"`
		}
		if !s.decode(c, &in) {
			return
		}
		n, err := s.a.GrantShares(user(c), in.Reason, in.AccountID, in.Symbol, in.Qty, in.Price)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "teams": n})
	})
	adm.GET("/accounts/:id", func(c *gin.Context) {
		d, err := s.a.Team(c.Param("id"))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, d)
	})
	adm.GET("/accounts/:id/history", s.teamHistory)
	adm.GET("/shared-addresses", s.sharedAddresses)
	adm.POST("/accounts/:id/cash", s.act(func(u app.User, b body, c *gin.Context) error {
		if b.SetTo != nil {
			return s.a.SetCash(u, b.Reason, c.Param("id"), *b.SetTo)
		}
		return s.a.AdjustCash(u, b.Reason, c.Param("id"), b.Amount)
	}))
	adm.POST("/accounts/:id/shares", s.act(func(u app.User, b body, c *gin.Context) error {
		switch b.Direction {
		case "give":
			_, err := s.a.GrantShares(u, b.Reason, c.Param("id"), b.Symbol, b.Qty, b.Price)
			return err
		case "take":
			return s.a.RevokeShares(u, b.Reason, c.Param("id"), b.Symbol, b.Qty)
		case "set":
			return s.a.SetShares(u, b.Reason, c.Param("id"), b.Symbol, b.Qty)
		}
		return &app.BadRequest{Code: "invalid_direction", Message: "Direction must be give, take or set."}
	}))
	adm.POST("/accounts/:id/reinstate", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Reinstate(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/reset-password", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.ResetPassword(u, b.Reason, c.Param("id"), b.Password)
	}))
	adm.POST("/accounts/:id/role", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.SetRole(u, b.Reason, c.Param("id"), b.Role) }))
	adm.POST("/accounts/:id/sign-out", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.SignOut(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/lock", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Lock(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/unlock", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Unlock(u, b.Reason, c.Param("id")) }))
	adm.POST("/sign-out-all", func(c *gin.Context) {
		var b body
		if !s.decode(c, &b) {
			return
		}
		n, err := s.a.SignOutAll(user(c), b.Reason)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "teams": n})
	})
	adm.POST("/clock/block-duration", s.act(func(u app.User, b body, _ *gin.Context) error {
		return s.a.ClockSetBlockDuration(u, b.Reason, b.BlockID, b.Minutes)
	}))
	adm.POST("/clock/end", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockEnd(u, b.Reason) }))
	adm.POST("/sim/:id/fire", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.FireSimEvent(u, b.Reason, c.Param("id")) }))
	adm.POST("/announce", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.Announce(u, b.Reason, b.Text) }))
	adm.POST("/control/symbols/:symbol", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.PauseSymbol(u, b.Reason, c.Param("symbol"), b.Paused)
	}))
	adm.POST("/trade-adjustments", s.act(func(u app.User, b body, _ *gin.Context) error {
		return s.a.RecordAdjustment(u, b.Reason, b.FillID, b.Adjustment.Note)
	}))

	if opt.Static != nil {
		h, err := webui.New(opt.Static, "/api", "/ws", "/healthz", "/readyz")
		if err != nil {
			return nil, err
		}
		r.NoRoute(gin.WrapH(h))
	} else {
		r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "not_found"}) })
	}
	return r, nil
}

// ---- middleware ----

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		} else {
			// (The web app handler sets the Content-Security-Policy: only our own scripts may run.)
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		}
		c.Next()
	}
}

func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("request panicked", "path", c.Request.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
				}
			}
		}()
		c.Next()
	}
}

func (s *Server) requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		d, st := time.Since(start), c.Writer.Status()
		attrs := []any{"method", c.Request.Method, "path", c.Request.URL.Path, "status", st, "ms", d.Milliseconds()}
		switch {
		case st >= 500:
			s.log.Error("request", attrs...)
		case d > time.Second:
			s.log.Warn("slow request", attrs...)
		default:
			s.log.Debug("request", attrs...)
		}
	}
}

func (s *Server) requireUser(c *gin.Context) {
	h := c.GetHeader("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	id, ver, err := s.a.Signer.Verify(tok)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	u, ok := s.a.User(id)
	if !ok || !s.a.SessionOK(u, ver) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.Set(ctxUser, u)
	c.Next()
}

func requireAdmin(c *gin.Context) {
	if !user(c).IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.Next()
}

func user(c *gin.Context) app.User { return c.MustGet(ctxUser).(app.User) }

// ---- errors ----

// fail writes an error as {"error": code, "message": ...} with the right status. Anything unexpected is
// logged in full and reported to the caller only as a generic internal error.
func (s *Server) fail(c *gin.Context, err error) {
	var status int
	var msg string
	var br *app.BadRequest
	var rl *app.RateLimited
	switch {
	case errors.As(err, &br):
		status, msg = http.StatusBadRequest, br.Message
		c.JSON(status, gin.H{"error": br.Code, "message": msg})
		return
	case errors.As(err, &rl):
		secs := int(rl.RetryAfter.Seconds()) + 1
		c.Header("Retry-After", strconv.Itoa(secs))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited", "retryAfterSeconds": secs,
			"message": limitMessage(rl.Funds, secs)})
		return
	}
	var tc *trading.TooConcentrated
	if errors.As(err, &tc) {
		msg := "You can hold at most " + strconv.FormatFloat(tc.Percent, 'f', -1, 64) + "% of your portfolio in one company."
		if tc.MaxQty > 0 {
			msg += " You can buy up to " + strconv.FormatInt(tc.MaxQty, 10) + " more shares of " + tc.Symbol + "."
		} else {
			msg += " You cannot buy more " + tc.Symbol + " right now."
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "too_concentrated", "maxQty": tc.MaxQty, "message": msg})
		return
	}
	var pc *trading.PriceChanged
	if errors.As(err, &pc) {
		now := strconv.FormatFloat(dto.Rupees(pc.Current), 'f', 2, 64)
		c.JSON(http.StatusConflict, gin.H{"error": "price_changed", "currentPrice": dto.Rupees(pc.Current), "message": "The price changed to ₹" + now + ". Review it and try again."})
		return
	}
	table := []struct {
		err    error
		status int
		msg    string
	}{
		{app.ErrInvalidCredentials, 401, "Wrong email or password."},
		{app.ErrEmailTaken, 409, "That email is already registered."},
		{app.ErrSignupClosed, 403, "Sign-up is closed."},
		{app.ErrNotOnList, 403, "That email is not on the list of people registered for this event. Ask an organiser."},
		{app.ErrBadEventCode, 403, "That event code is not right. Ask an organiser."},
		{app.ErrAccountsFull, 403, "Registration is full. Ask an organiser."},
		{app.ErrBusy, 503, "The server is busy checking passwords. Try again in a moment."},
		{app.ErrDisqualified, 403, "This team has been disqualified."},
		{app.ErrAccountLocked, 403, "This account is locked. Ask an organiser."},
		{app.ErrMarketClosed, 403, "The market is closed right now."},
		{app.ErrSymbolPaused, 403, "Trading in this company is paused by the organisers."},
		{app.ErrFrozen, 423, "Trading is frozen by the organisers."},
		{trading.ErrUnknownSymbol, 404, "No such company."},
		{trading.ErrInvalid, 400, "That trade is not valid."},
		{trading.ErrIdempotencyMismatch, 409, "That trade id was already used for a different trade."},
		{trading.ErrJournal, 503, "The trade could not be saved, so it did not happen. Try again."},
		{trading.ErrNoPrice, 503, "This company has no price right now."},
		{store.ErrDiskLow, 503, "The server is short of disk space, so it is not accepting trades. An organiser has been alerted."},
		{app.ErrNoAccount, 403, "This account has no trading account."},
		{app.ErrFundsNotFormed, 409, "The funds have not been formed yet."},
		{app.ErrNotAllowed, 403, "You cannot do that."},
		{app.ErrNotTrader, 403, "The other team in your fund places its trades. You can watch the fund from here."},
		{funds.ErrUnknownFund, 404, "No such fund."},
		{app.ErrUnknownUser, 404, "No such account."},
		{app.ErrUnknownTicket, 404, "No such dispute."},
		{ledger.ErrInsufficientCash, 422, "Not enough cash for this order."},
		{ledger.ErrInsufficientShares, 422, "You do not hold enough shares to sell."},
		{eventclock.ErrNotStarted, 409, "The event has not started."},
		{eventclock.ErrAlreadyStarted, 409, "The event has already started."},
		{eventclock.ErrNotPaused, 409, "The event is not paused."},
		{eventclock.ErrPaused, 409, "The event is paused."},
		{eventclock.ErrUnknownBlock, 400, "There is no such block."},
		{eventclock.ErrBlockInPast, 409, "That block has already finished."},
	}
	for _, e := range table {
		if errors.Is(err, e.err) {
			c.JSON(e.status, gin.H{"error": e.err.Error(), "message": e.msg})
			return
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "busy", "message": "The server is busy. Try again."})
		return
	}
	s.log.Error("unexpected error", "path", c.Request.URL.Path, "err", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "message": "Something went wrong on our side."})
}

func (s *Server) decode(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	if err := c.ShouldBindJSON(v); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "message": "The request body is not valid."})
		return false
	}
	return true
}

// ---- health ----

func (s *Server) ready(c *gin.Context) {
	if s.a.DiskLow() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "disk_low"})
		return
	}
	if !s.a.DBHealthy() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "database_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- auth ----

type credentials struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	EventCode   string `json:"eventCode"`
}

type session struct {
	Token   string      `json:"token"`
	Account dto.Account `json:"account"`
}

func (s *Server) issue(c *gin.Context, u app.User) {
	tok, err := s.a.Signer.Issue(u.ID, u.SessionVersion)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, session{Token: tok, Account: s.a.Account(u)})
}

func (s *Server) signup(c *gin.Context) {
	var in credentials
	if !s.decode(c, &in) {
		return
	}
	// The event code is checked before the address allowance is used, so guessing at it cannot use up the
	// allowance of honest people who share the address.
	if err := s.a.CheckSignupCode(in.EventCode); err != nil {
		s.fail(c, err)
		return
	}
	if ok, wait := s.lim.signup.allow(c.ClientIP(), time.Now()); !ok {
		tooMany(c, wait, "too_many_signups", "Too many sign-ups from this connection. Wait a moment.")
		return
	}
	u, err := s.a.Signup(in.DisplayName, in.Email, in.Password, in.EventCode)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.track(c, u.ID, "signup", "")
	s.issue(c, u)
}

func (s *Server) login(c *gin.Context) {
	var in credentials
	if !s.decode(c, &in) {
		return
	}
	// An organiser can always sign in, from any address, however many wrong guesses were made at the email: the
	// password is long (12 or more characters), and a lock here would only help someone who wants the console shut.
	guarded := !s.a.IsAdminEmail(in.Email)
	if wait := s.logins.blocked(in.Email, c.ClientIP()); guarded && wait > 0 {
		c.Header("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_attempts", "message": "Too many wrong passwords for this email. Wait a few minutes."})
		return
	}
	u, err := s.a.Login(in.Email, in.Password)
	if err != nil {
		if errors.Is(err, app.ErrInvalidCredentials) && guarded {
			s.logins.failed(in.Email, c.ClientIP())
		}
		if errors.Is(err, app.ErrInvalidCredentials) {
			s.track(c, "", "login_failed", in.Email)
		}
		s.fail(c, err)
		return
	}
	s.logins.ok(in.Email, c.ClientIP())
	s.track(c, u.ID, "login", "")
	s.issue(c, u)
}

// track notes what a person did, with the address and browser it came from.
func (s *Server) track(c *gin.Context, account, typ, detail string) {
	s.a.Track(account, typ, c.ClientIP(), c.GetHeader("User-Agent"), detail)
}

func (s *Server) me(c *gin.Context) { c.JSON(http.StatusOK, s.a.Account(user(c))) }

// ---- reads ----

type publicConfig struct {
	rulebook.PublicConfig
	TradingFrozen    bool            `json:"tradingFrozen"`
	MarketOpen       bool            `json:"marketOpen"`
	WindowsOpen      map[string]bool `json:"windowsOpen"`
	PriceTickSeconds int             `json:"priceTickSeconds"`
	SignupOpen       bool            `json:"signupOpen"`
}

func (s *Server) config(c *gin.Context) {
	cs := s.a.ControlState()
	anyOpen := false
	for i := 0; i < s.a.Clock.WindowCount(); i++ {
		if s.a.Clock.WindowOpen(i) {
			anyOpen = true
		}
	}
	pc := s.a.RB.Public()
	pc.Event.Timeline, pc.Event.TotalMinutes = s.a.PublicSchedule() // the lengths the organiser has set now
	c.JSON(http.StatusOK, publicConfig{
		PublicConfig: pc, TradingFrozen: cs.TradingFrozen, MarketOpen: cs.MarketOpen,
		WindowsOpen: map[string]bool{"fundAllocationWindow": anyOpen}, PriceTickSeconds: s.a.Sim.TickSeconds(), SignupOpen: s.a.SignupOpen(),
	})
}

func (s *Server) history(c *gin.Context) {
	sym := c.Param("symbol")
	if !s.a.HasSymbol(sym) {
		s.fail(c, trading.ErrUnknownSymbol)
		return
	}
	c.JSON(http.StatusOK, s.a.History(sym))
}

func (s *Server) portfolio(c *gin.Context) {
	p, err := s.a.Portfolio(user(c))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) leaderboard(c *gin.Context) {
	if !user(c).IsAdmin && !s.a.RB.Leaderboard.VisibleToParticipants {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "message": "Standings are not published to teams."})
		return
	}
	c.JSON(http.StatusOK, s.a.Leaderboard())
}

func (s *Server) news(c *gin.Context) { c.JSON(http.StatusOK, s.a.NewsFor(user(c))) }

// ---- trades ----

func (s *Server) trade(c *gin.Context) {
	var in app.TradeRequest
	if !s.decode(c, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestWait)
	defer cancel()
	res, err := s.a.Trade(ctx, user(c), in)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deduped": res.Deduped, "trade": dto.FromTrade(res.Trade)})
}

func (s *Server) raiseDispute(c *gin.Context) {
	var in struct {
		Category   string `json:"category"`
		Summary    string `json:"summary"`
		IncidentAt int64  `json:"incidentAt"`
	}
	if !s.decode(c, &in) {
		return
	}
	at := time.Time{}
	if in.IncidentAt > 0 {
		at = time.UnixMilli(in.IncidentAt)
	}
	rec, err := s.a.RaiseDispute(user(c), in.Category, in.Summary, at)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": rec.Ticket.ID, "dueBy": dto.MS(rec.Ticket.DueBy)})
}

// ---- organiser actions ----

type body struct {
	Pairs           [][]string `json:"pairs"`
	AccountID       string     `json:"accountId"`
	Reason          string     `json:"reason"`
	CompressBlockID string     `json:"compressBlockId"`
	BlockID         string     `json:"blockId"`
	Minutes         int        `json:"minutes"`
	Frozen          bool       `json:"frozen"`
	Override        any        `json:"override"`
	Kind            string     `json:"kind"`
	Headline        string     `json:"headline"`
	Body            string     `json:"body"`
	PlatformWide    bool       `json:"platformWide"`
	Amount          float64    `json:"amount"`
	SetTo           *float64   `json:"setTo"`
	Password        string     `json:"password"`
	Role            string     `json:"role"`
	Text            string     `json:"text"`
	Symbol          string     `json:"symbol"`
	Qty             int64      `json:"qty"`
	Price           float64    `json:"price"`
	Paused          bool       `json:"paused"`
	Direction       string     `json:"direction"`
	FillID          string     `json:"fillId"`
	Adjustment      struct {
		Note string `json:"note"`
	} `json:"adjustment"`
}

// override turns the wire value ("open", "closed" or null) into a tri-state.
func (b body) override() (*bool, error) {
	switch v := b.Override.(type) {
	case nil:
		return nil, nil
	case string:
		t := v == "open"
		if v == "open" || v == "closed" {
			return &t, nil
		}
	}
	return nil, &app.BadRequest{Code: "invalid_override", Message: "Override must be open, closed or null."}
}

// act wraps an organiser action: it parses the body once, runs the action and reports the outcome.
func (s *Server) act(f func(u app.User, b body, c *gin.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		var b body
		if !s.decode(c, &b) {
			return
		}
		if err := f(user(c), b, c); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func limitMessage(funds bool, secs int) string {
	if funds {
		return "You are moving money in and out of funds too often. Try again in " + strconv.Itoa(secs) + " seconds."
	}
	return "You have used your trades for now. Try again in " + strconv.Itoa(secs) + " seconds."
}

// teamHistory is one team's wallet over time, what they did, and every change to their cash and shares. It needs
// PostgreSQL (the lookup tables); without it the answer says so.
func (s *Server) teamHistory(c *gin.Context) {
	h := s.a.PastRecords()
	if h == nil {
		c.JSON(http.StatusOK, gin.H{"available": false})
		return
	}
	id := c.Param("id")
	if _, err := s.a.Team(id); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	wallet, err1 := h.WalletHistory(ctx, id, 400)
	acts, err2 := h.Activity(ctx, id, 100)
	led, err3 := h.Ledger(ctx, id, 200)
	behind, _ := h.Behind(ctx)
	if err1 != nil || err2 != nil || err3 != nil {
		s.log.Warn("history lookup failed", "err", errors.Join(err1, err2, err3))
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "history_unavailable", "message": "The history could not be read just now."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"available": true, "behind": behind, "wallet": wallet, "activity": acts, "ledger": led})
}

// sharedAddresses lists addresses that several accounts signed in from. It is a hint only: a venue's network
// makes many honest people share one address.
func (s *Server) sharedAddresses(c *gin.Context) {
	h := s.a.PastRecords()
	if h == nil {
		c.JSON(http.StatusOK, gin.H{"available": false, "addresses": []any{}})
		return
	}
	rows, err := h.SharedAddresses(c.Request.Context())
	if err != nil {
		s.log.Warn("shared address lookup failed", "err", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "history_unavailable", "message": "The history could not be read just now."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"available": true, "addresses": rows})
}
