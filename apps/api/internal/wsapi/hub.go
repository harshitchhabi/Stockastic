// Package wsapi is the WebSocket side: plain JSON frames {"t": type, "d": payload}.
//
// A slow or stuck client can never slow the platform: every client has a small bounded send queue and a
// broadcast never waits; a client whose queue is full is disconnected (it reconnects and refetches).
package wsapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	sendQueue    = 256
	authDeadline = 3 * time.Second
	writeTimeout = 10 * time.Second
	readLimit    = 4096
	idleTimeout  = 60 * time.Second
	// CloseUnauthenticated is the close code the web client treats as "stop retrying, sign in again".
	CloseUnauthenticated = 4401
	// CloseTooMany is sent to a connection that is closed for using more than its share (too many sockets for
	// one account, or messages too fast). The web app reconnects after a pause.
	CloseTooMany = 4429

	defaultMaxConns    = 4000
	defaultPerAccount  = 4
	maxUnauthenticated = 1500 // sockets that have opened but not yet sent a valid login at one time
	msgsPerSecond      = 20.0 // inbound messages one connection may send, sustained
	msgBurst           = 40.0
)

// Identity is who an authenticated socket belongs to.
type Identity struct {
	AccountID string
	Role      string
}

// Authenticator verifies a token and returns the identity behind it.
type Authenticator func(token string) (Identity, bool)

type frame struct {
	T string          `json:"t"`
	D json.RawMessage `json:"d,omitempty"`
}

type Client struct {
	id       Identity
	send     chan []byte
	conn     *websocket.Conn
	symbols  map[string]struct{} // guarded by hub.mu
	closed   atomic.Bool
	closeErr string
	since    time.Time
}

type Hub struct {
	log      *slog.Logger
	auth     Authenticator
	upgrader websocket.Upgrader
	onReady  func(c *Client)
	presence func(accountID string, online bool)

	mu        sync.RWMutex
	clients   map[*Client]struct{}
	bySymbol  map[string]map[*Client]struct{}
	byAccount map[string]map[*Client]struct{}

	Dropped atomic.Int64 // clients disconnected for being too slow

	maxConns, perAccount int
	unauth               atomic.Int64 // sockets waiting to log in
}

// SetLimits caps live connections in all and per account. Zero keeps the default.
func (h *Hub) SetLimits(maxConns, perAccount int) {
	h.mu.Lock()
	if maxConns > 0 {
		h.maxConns = maxConns
	}
	if perAccount > 0 {
		h.perAccount = perAccount
	}
	h.mu.Unlock()
}

// New builds a hub. allowedOrigins lists extra browser origins accepted for the upgrade (same-origin
// and non-browser clients are always accepted).
func New(log *slog.Logger, auth Authenticator, allowedOrigins []string, onReady func(*Client)) *Hub {
	h := &Hub{
		log: log, auth: auth, onReady: onReady,
		clients: map[*Client]struct{}{}, bySymbol: map[string]map[*Client]struct{}{}, byAccount: map[string]map[*Client]struct{}{},
		maxConns: defaultMaxConns, perAccount: defaultPerAccount,
	}
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[strings.TrimRight(strings.TrimSpace(o), "/")] = true
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			if o == "" || allowed[strings.TrimRight(o, "/")] {
				return true
			}
			u, err := url.Parse(o)
			return err == nil && u.Host == r.Host
		},
	}
	return h
}

func (c *Client) Identity() Identity { return c.id }

// OnPresence registers a function called when an account gets its first connection (online) and when it
// loses its last one (offline). It runs outside the hub's locks and must not block.
func (h *Hub) OnPresence(f func(accountID string, online bool)) { h.presence = f }

// Sockets is how many live connections an account has (a team may have several browsers open).
func (h *Hub) Sockets(accountID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byAccount[accountID])
}

// DisconnectAccount closes every connection an account has, with a close code the web app understands
// (4401 tells it to stop retrying and ask the person to sign in again).
func (h *Hub) DisconnectAccount(accountID string, code int, text string) {
	h.mu.RLock()
	cs := make([]*Client, 0, len(h.byAccount[accountID]))
	for c := range h.byAccount[accountID] {
		cs = append(cs, c)
	}
	h.mu.RUnlock()
	for _, c := range cs {
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
		_ = c.conn.Close()
	}
}

func encode(t string, d any) []byte {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	b, _ := json.Marshal(struct {
		T string          `json:"t"`
		D json.RawMessage `json:"d"`
	}{t, raw})
	return b
}

func (h *Hub) deliver(c *Client, b []byte) {
	if b == nil || c.closed.Load() {
		return
	}
	select {
	case c.send <- b:
	default:
		if c.closed.CompareAndSwap(false, true) {
			h.Dropped.Add(1)
			h.log.Warn("ws: dropping slow client", "account", c.id.AccountID)
			go c.conn.Close()
		}
	}
}

// Send delivers one frame to one client.
func (h *Hub) Send(c *Client, t string, d any) { h.deliver(c, encode(t, d)) }

func (h *Hub) ToSymbol(symbol, t string, d any) {
	b := encode(t, d)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.bySymbol[symbol] {
		h.deliver(c, b)
	}
}

func (h *Hub) ToAccount(account, t string, d any) {
	b := encode(t, d)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.byAccount[account] {
		h.deliver(c, b)
	}
}

// ToRole sends to every client with the given role.
func (h *Hub) ToRole(role, t string, d any) {
	b := encode(t, d)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.id.Role == role {
			h.deliver(c, b)
		}
	}
}

func (h *Hub) ToAll(t string, d any) {
	b := encode(t, d)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		h.deliver(c, b)
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Shutdown tells every client to reconnect later and closes them.
func (h *Hub) Shutdown() {
	h.mu.RLock()
	cs := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		cs = append(cs, c)
	}
	h.mu.RUnlock()
	for _, c := range cs {
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server restarting"), time.Now().Add(time.Second))
		_ = c.conn.Close()
	}
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	// One account may hold only a few sockets. A newer one replaces the oldest, so a page refreshed quickly
	// is never locked out by its own dying connection, and nobody can hold hundreds of sockets open.
	var evict []*Client
	for len(h.byAccount[c.id.AccountID])-len(evict) >= h.perAccount {
		var oldest *Client
		for o := range h.byAccount[c.id.AccountID] {
			if !contains(evict, o) && (oldest == nil || o.since.Before(oldest.since)) {
				oldest = o
			}
		}
		if oldest == nil {
			break
		}
		evict = append(evict, oldest)
	}
	h.clients[c] = struct{}{}
	if h.byAccount[c.id.AccountID] == nil {
		h.byAccount[c.id.AccountID] = map[*Client]struct{}{}
	}
	h.byAccount[c.id.AccountID][c] = struct{}{}
	first := len(h.byAccount[c.id.AccountID]) == 1
	h.mu.Unlock()
	for _, o := range evict {
		_ = o.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(CloseTooMany, "another page took over"), time.Now().Add(time.Second))
		_ = o.conn.Close()
	}
	if first && h.presence != nil {
		h.presence(c.id.AccountID, true)
	}
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	last := false
	if m := h.byAccount[c.id.AccountID]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.byAccount, c.id.AccountID)
			last = true
		}
	}
	for s := range c.symbols {
		if m := h.bySymbol[s]; m != nil {
			delete(m, c)
			if len(m) == 0 {
				delete(h.bySymbol, s)
			}
		}
	}
	h.mu.Unlock()
	if last && h.presence != nil {
		h.presence(c.id.AccountID, false)
	}
}

func (h *Hub) subscribe(c *Client, symbol string, on bool) {
	if symbol == "" || len(symbol) > 16 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if on {
		if len(c.symbols) >= 1000 {
			return
		}
		c.symbols[symbol] = struct{}{}
		if h.bySymbol[symbol] == nil {
			h.bySymbol[symbol] = map[*Client]struct{}{}
		}
		h.bySymbol[symbol][c] = struct{}{}
		return
	}
	delete(c.symbols, symbol)
	if m := h.bySymbol[symbol]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.bySymbol, symbol)
		}
	}
}

// Serve upgrades one HTTP request and runs the socket until it closes.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) {
	// Turn away what the server cannot hold before spending anything on it.
	h.mu.RLock()
	full := len(h.clients) >= h.maxConns
	h.mu.RUnlock()
	if full || h.unauth.Load() >= maxUnauthenticated {
		http.Error(w, "the server is full, try again in a moment", http.StatusServiceUnavailable)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(readLimit)

	// The first frame must be auth, within a short deadline.
	h.unauth.Add(1)
	_ = conn.SetReadDeadline(time.Now().Add(authDeadline))
	id, ok := h.readAuth(conn)
	h.unauth.Add(-1)
	if !ok {
		b := encode("error", map[string]string{"code": "unauthenticated"})
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_ = conn.WriteMessage(websocket.TextMessage, b)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(CloseUnauthenticated, "unauthenticated"), time.Now().Add(time.Second))
		_ = conn.Close()
		return
	}

	c := &Client{id: id, conn: conn, send: make(chan []byte, sendQueue), symbols: map[string]struct{}{}, since: time.Now()}
	h.add(c)
	defer func() {
		h.remove(c)
		c.closed.Store(true)
		_ = conn.Close()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go h.writePump(ctx, c)

	h.deliver(c, encode("ready", nil))
	if h.onReady != nil {
		h.onReady(c)
	}

	tokens, last := msgBurst, time.Now()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// A connection that sends messages faster than any real page would is cut off.
		now := time.Now()
		tokens = min(msgBurst, tokens+now.Sub(last).Seconds()*msgsPerSecond) - 1
		last = now
		if tokens < 0 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(CloseTooMany, "too many messages"), time.Now().Add(time.Second))
			return
		}
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.T {
		case "ping":
			h.deliver(c, encode("pong", nil))
		case "subscribe:symbol", "unsubscribe:symbol":
			var sym string
			if json.Unmarshal(f.D, &sym) == nil {
				h.subscribe(c, sym, f.T == "subscribe:symbol")
			}
		}
	}
}

func (h *Hub) readAuth(conn *websocket.Conn) (Identity, bool) {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return Identity{}, false
	}
	var f frame
	if json.Unmarshal(data, &f) != nil || f.T != "auth" {
		return Identity{}, false
	}
	var d struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(f.D, &d) != nil || d.Token == "" {
		return Identity{}, false
	}
	return h.auth(d.Token)
}

func (h *Hub) writePump(ctx context.Context, c *Client) {
	defer c.conn.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		}
	}
}

// SetRole changes the role of every open socket of an account, so a team promoted to fund manager (or moved
// back) gets the right feed on the connection it already has.
func (h *Hub) SetRole(account, role string) {
	h.mu.Lock()
	for c := range h.byAccount[account] {
		c.id.Role = role
	}
	h.mu.Unlock()
}

func contains(cs []*Client, c *Client) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}
