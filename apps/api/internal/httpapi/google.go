package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"stockastic/api/internal/app"
	"stockastic/api/internal/oauth"
)

const (
	stateCookie = "g_nonce"
	stateTTL    = 10 * time.Minute
)

// googleFlow holds what is needed to tell a real return from Google apart from a forged one: a signed state that
// carries a random value, the same random value in a cookie only this browser has, and a note of values already used.
type googleFlow struct {
	key    []byte
	secure bool
	mu     sync.Mutex
	used   map[string]time.Time
}

func newGoogleFlow(key []byte, redirect string) *googleFlow { // redirect: the registered return address
	return &googleFlow{key: key, secure: strings.HasPrefix(redirect, "https://"), used: map[string]time.Time{}}
}

func (g *googleFlow) sign(payload string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// makeState is nonce.expiry.eventCode signed. The event code the person typed rides along so a new account can be
// checked against it after the round trip.
func (g *googleFlow) makeState(nonce, eventCode string) string {
	p := nonce + "." + strconv.FormatInt(time.Now().Add(stateTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString([]byte(eventCode))
	return p + "." + g.sign(p)
}

func (g *googleFlow) readState(state string) (nonce, eventCode string, ok bool) {
	i := strings.LastIndex(state, ".")
	if i < 0 || !hmac.Equal([]byte(g.sign(state[:i])), []byte(state[i+1:])) {
		return "", "", false
	}
	parts := strings.Split(state[:i], ".")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	code, err2 := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || err2 != nil || time.Now().Unix() > exp {
		return "", "", false
	}
	return parts[0], string(code), true
}

// use marks a nonce as spent; it returns false if it already was, so a return cannot be replayed.
func (g *googleFlow) use(nonce string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for k, t := range g.used {
		if now.After(t) {
			delete(g.used, k)
		}
	}
	if _, done := g.used[nonce]; done {
		return false
	}
	g.used[nonce] = now.Add(2 * stateTTL)
	return true
}

func (s *Server) googleRoutes(api *gin.RouterGroup) {
	if s.opt.Google == nil {
		return
	}
	api.GET("/auth/google/start", s.googleStart)
	api.GET("/auth/google/callback", s.googleCallback)
}

// googleStart sends the browser to Google.
func (s *Server) googleStart(c *gin.Context) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := hex.EncodeToString(b)
	code := c.Query("code")
	if len(code) > 40 {
		code = code[:40]
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: stateCookie, Value: nonce, Path: "/api/auth/google", MaxAge: int(stateTTL.Seconds()),
		HttpOnly: true, Secure: s.google.secure, SameSite: http.SameSiteLaxMode})
	c.Redirect(http.StatusFound, s.opt.Google.AuthURL(s.google.makeState(nonce, code), nonce))
}

// googleFail sends the person back to the sign-in page with a short reason (never any detail from Google).
func googleFail(c *gin.Context, reason string) {
	c.Redirect(http.StatusFound, "/#/signin-error="+url.QueryEscape(reason))
}

// googleCallback is where Google sends the browser back with a one-time code.
func (s *Server) googleCallback(c *gin.Context) {
	if e := c.Query("error"); e != "" {
		googleFail(c, "cancelled")
		return
	}
	nonce, eventCode, ok := s.google.readState(c.Query("state"))
	cookie, err := c.Cookie(stateCookie)
	if !ok || err != nil || !hmac.Equal([]byte(cookie), []byte(nonce)) || !s.google.use(nonce) {
		googleFail(c, "expired")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: stateCookie, Value: "", Path: "/api/auth/google", MaxAge: -1, HttpOnly: true, Secure: s.google.secure, SameSite: http.SameSiteLaxMode})

	idToken, err := s.opt.Google.Exchange(c.Request.Context(), c.Query("code"))
	if err != nil {
		s.log.Warn("google sign-in: code exchange failed", "err", err)
		googleFail(c, "google")
		return
	}
	claims, err := s.opt.Google.Verify(c.Request.Context(), idToken, nonce)
	switch {
	case errors.Is(err, oauth.ErrDomain):
		googleFail(c, "domain")
		return
	case errors.Is(err, oauth.ErrNotVerfied):
		googleFail(c, "unverified")
		return
	case err != nil:
		s.log.Warn("google sign-in: identity token refused", "err", err)
		googleFail(c, "google")
		return
	}
	u, err := s.a.ExternalSignIn(claims.Email, claims.Name, eventCode)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrAccountLocked):
			googleFail(c, "locked")
		case errors.Is(err, app.ErrNotOnList):
			googleFail(c, "not_on_list")
		case errors.Is(err, app.ErrBadEventCode):
			googleFail(c, "code")
		case errors.Is(err, app.ErrSignupClosed):
			googleFail(c, "closed")
		case errors.Is(err, app.ErrAccountsFull):
			googleFail(c, "full")
		default:
			s.log.Warn("google sign-in: could not sign in", "err", err)
			googleFail(c, "failed")
		}
		return
	}
	tok, err := s.a.Signer.Issue(u.ID, u.SessionVersion)
	if err != nil {
		googleFail(c, "failed")
		return
	}
	// The token goes in the part of the address after # : it is never sent to any server or logged, and the page
	// removes it from the address bar as soon as it has stored it.
	c.Redirect(http.StatusFound, "/#/signin="+tok)
}
