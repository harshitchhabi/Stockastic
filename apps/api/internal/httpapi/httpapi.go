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
	"stockastic/api/internal/disputes"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/engine"
	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/rulebook"
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
	// Static, if set, is served for every path that is not an API or WebSocket path (the built web app).
	Static fs.FS
}

type Server struct {
	a      *app.App
	log    *slog.Logger
	opt    Options
	logins *loginGuard
}

// New builds the router.
func New(opt Options) (http.Handler, error) {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{a: opt.App, log: opt.Log, opt: opt, logins: newLoginGuard()}
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(s.recovery(), s.requestLog(), securityHeaders())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/readyz", s.ready)
	r.GET("/ws", func(c *gin.Context) { s.a.Hub.Serve(c.Writer, c.Request) })

	api := r.Group("/api")
	api.POST("/auth/signup", s.signup)
	api.POST("/auth/login", s.login)

	me := api.Group("", s.requireUser)
	me.GET("/auth/me", s.me)
	me.GET("/config", s.config)
	me.GET("/symbols", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Companies()) })
	me.GET("/symbols/:symbol/depth", s.depth)
	me.GET("/symbols/:symbol/history", s.history)
	me.GET("/portfolio/me", s.portfolio)
	me.GET("/orders/pending", s.pending)
	me.POST("/orders", s.placeOrder)
	me.DELETE("/orders/:symbol/:id", s.cancelOrder)
	me.GET("/leaderboard", s.leaderboard)
	me.GET("/news", s.news)
	me.GET("/funds", func(c *gin.Context) { c.JSON(http.StatusOK, []any{}) })
	me.POST("/disputes", s.raiseDispute)
	me.GET("/disputes/mine", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.TicketsOf(user(c).ID)) })

	adm := api.Group("/admin", s.requireUser, requireAdmin)
	adm.GET("/overview", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Overview()) })
	adm.GET("/systems", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Systems()) })
	adm.GET("/accounts", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AdminAccounts()) })
	adm.GET("/news", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.NewsDesk()) })
	adm.GET("/disputes", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AdminTickets()) })
	adm.GET("/audit", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AuditLog()) })
	adm.GET("/rulebook", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.RulebookView(s.opt.RulebookSource)) })
	adm.GET("/standings", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Leaderboard()) })

	adm.POST("/clock/start", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.ClockStart(u, b.Reason) }))
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
		if err != nil || i < 0 || i >= s.a.RB.WindowCount() {
			return &app.BadRequest{Code: "unknown_window", Message: "There is no such allocation window."}
		}
		o, err := b.override()
		if err != nil {
			return err
		}
		return s.a.SetWindowOverride(u, b.Reason, i, o)
	}))
	adm.POST("/symbols/:symbol/resume", s.act(func(u app.User, b body, c *gin.Context) error {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		return s.a.ResumeSymbol(ctx, u, b.Reason, c.Param("symbol"))
	}))
	adm.POST("/accounts/:id/promote", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Promote(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/warn", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Warn(u, b.Reason, c.Param("id")) }))
	adm.POST("/accounts/:id/disqualify", s.act(func(u app.User, b body, c *gin.Context) error {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		return s.a.Disqualify(ctx, u, b.Reason, c.Param("id"))
	}))
	adm.POST("/news", s.act(func(u app.User, b body, _ *gin.Context) error {
		return s.a.PublishNews(u, b.Reason, b.Kind, b.Headline, b.Body)
	}))
	adm.POST("/disputes/:id/triage", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.TriageTicket(u, b.Reason, c.Param("id"), b.PlatformWide)
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
	id, err := s.a.Signer.Parse(tok)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	u, ok := s.a.User(id)
	if !ok {
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
			"message": "You have used your trades for now. Try again in " + strconv.Itoa(secs) + " seconds."})
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
		{app.ErrDisqualified, 403, "This team has been disqualified."},
		{app.ErrMarketClosed, 403, "The market is closed right now."},
		{app.ErrNoAccount, 403, "This account has no trading account."},
		{app.ErrUnknownUser, 404, "No such account."},
		{app.ErrUnknownTicket, 404, "No such dispute."},
		{engine.ErrFrozen, 423, "Trading is frozen by the organisers."},
		{engine.ErrUnknownSymbol, 404, "No such company."},
		{engine.ErrInvalidOrder, 400, "That order is not valid."},
		{engine.ErrIdempotencyMismatch, 409, "That order id was already used for a different order."},
		{engine.ErrSymbolHalted, 503, "This company is briefly unavailable. Try again shortly."},
		{engine.ErrJournal, 503, "The order could not be saved, so it was not placed. Try again."},
		{engine.ErrStopped, 503, "The server is shutting down."},
		{engine.ErrNotFound, 404, "No such order."},
		{engine.ErrNotOwner, 403, "That is not your order."},
		{engine.ErrAlreadyClosed, 409, "That order is already closed."},
		{ledger.ErrInsufficientCash, 422, "Not enough cash for this order."},
		{ledger.ErrInsufficientShares, 422, "You do not hold enough shares to sell."},
		{eventclock.ErrNotStarted, 409, "The event has not started."},
		{eventclock.ErrAlreadyStarted, 409, "The event has already started."},
		{eventclock.ErrNotPaused, 409, "The event is not paused."},
		{eventclock.ErrPaused, 409, "The event is paused."},
		{eventclock.ErrUnknownBlock, 400, "There is no such block."},
		{disputes.ErrUnknownTicket, 404, "No such dispute."},
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
	if s.a.Ledger.Anomalies() > 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "ledger_anomaly"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- auth ----

type credentials struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type session struct {
	Token   string      `json:"token"`
	Account dto.Account `json:"account"`
}

func (s *Server) issue(c *gin.Context, u app.User) {
	tok, err := s.a.Signer.Issue(u.ID)
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
	u, err := s.a.Signup(in.DisplayName, in.Email, in.Password)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.issue(c, u)
}

func (s *Server) login(c *gin.Context) {
	var in credentials
	if !s.decode(c, &in) {
		return
	}
	if wait := s.logins.blocked(in.Email); wait > 0 {
		c.Header("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_attempts", "message": "Too many wrong passwords for this email. Wait a few minutes."})
		return
	}
	u, err := s.a.Login(in.Email, in.Password)
	if err != nil {
		if errors.Is(err, app.ErrInvalidCredentials) {
			s.logins.failed(in.Email)
		}
		s.fail(c, err)
		return
	}
	s.logins.ok(in.Email)
	s.issue(c, u)
}

func (s *Server) me(c *gin.Context) { c.JSON(http.StatusOK, s.a.Account(user(c))) }

// ---- reads ----

type publicConfig struct {
	rulebook.PublicConfig
	TradingFrozen bool            `json:"tradingFrozen"`
	MarketOpen    bool            `json:"marketOpen"`
	WindowsOpen   map[string]bool `json:"windowsOpen"`
}

func (s *Server) config(c *gin.Context) {
	cs := s.a.ControlState()
	anyOpen := false
	for i := 0; i < s.a.RB.WindowCount(); i++ {
		if s.a.Clock.WindowOpen(i) {
			anyOpen = true
		}
	}
	c.JSON(http.StatusOK, publicConfig{
		PublicConfig: s.a.RB.Public(), TradingFrozen: cs.TradingFrozen, MarketOpen: cs.MarketOpen,
		WindowsOpen: map[string]bool{"fundAllocationWindow": anyOpen},
	})
}

func (s *Server) depth(c *gin.Context) {
	d, err := s.a.Depth(c.Param("symbol"))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) history(c *gin.Context) {
	sym := c.Param("symbol")
	if !s.a.HasSymbol(sym) {
		s.fail(c, engine.ErrUnknownSymbol)
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

func (s *Server) pending(c *gin.Context) {
	live := s.a.LiveOrders(user(c).ID)
	out := make([]dto.Order, len(live))
	for i, o := range live {
		out[i] = dto.FromOrder(o)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) leaderboard(c *gin.Context) {
	if !user(c).IsAdmin && !s.a.RB.Leaderboard.VisibleToParticipants {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "message": "Standings are not published to teams."})
		return
	}
	c.JSON(http.StatusOK, s.a.Leaderboard())
}

func (s *Server) news(c *gin.Context) { c.JSON(http.StatusOK, s.a.NewsFor(user(c))) }

// ---- orders ----

func (s *Server) placeOrder(c *gin.Context) {
	var in app.OrderRequest
	if !s.decode(c, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestWait)
	defer cancel()
	res, err := s.a.PlaceOrder(ctx, user(c), in)
	if err != nil {
		s.fail(c, err)
		return
	}
	fills := make([]dto.Fill, len(res.Fills))
	for i, f := range res.Fills {
		fills[i] = dto.FromFill(f)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deduped": res.Deduped, "order": dto.FromOrder(res.Order), "fills": fills})
}

func (s *Server) cancelOrder(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestWait)
	defer cancel()
	res, err := s.a.CancelOrder(ctx, user(c), c.Param("symbol"), c.Param("id"))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": true, "order": dto.FromOrder(res.Order)})
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
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": rec.Ticket.ID, "queue": rec.Ticket.Queue})
}

// ---- organiser actions ----

type body struct {
	Reason          string `json:"reason"`
	CompressBlockID string `json:"compressBlockId"`
	BlockID         string `json:"blockId"`
	Minutes         int    `json:"minutes"`
	Frozen          bool   `json:"frozen"`
	Override        any    `json:"override"`
	Kind            string `json:"kind"`
	Headline        string `json:"headline"`
	Body            string `json:"body"`
	PlatformWide    bool   `json:"platformWide"`
	FillID          string `json:"fillId"`
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
