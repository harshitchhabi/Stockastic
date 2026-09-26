// Package oauth signs people in with their Google account (the "authorization code" flow, done on the server).
//
// The browser is sent to Google, Google sends it back with a one-time code, and the server swaps that code for a
// signed identity token and checks it: Google's signature, that it was issued to this app, that it has not expired,
// that it answers this sign-in (the nonce), and that the email is verified. Nothing from Google's side is trusted
// until those checks pass, and no script from Google is ever loaded into our pages.
package oauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	googleAuth  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleToken = "https://oauth2.googleapis.com/token"
	googleJWKS  = "https://www.googleapis.com/oauth2/v3/certs"
)

// Config is how this server is registered with Google. Empty ClientID means Google sign-in is off.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is where Google sends people back; it must match what is registered with Google exactly, for
	// example https://event.example.com/api/auth/google/callback.
	RedirectURL string
	// AllowedDomains, if not empty, limits sign-in to emails at these domains (for example a college's).
	AllowedDomains []string
	// The three addresses below default to Google's. Tests point them at a fake.
	AuthURL, TokenURL, JWKSURL string
}

// Claims is what a checked identity token says about the person.
type Claims struct {
	Subject string
	Email   string
	Name    string
}

var (
	ErrDisabled   = errors.New("google sign-in is not set up")
	ErrBadToken   = errors.New("google identity could not be verified")
	ErrNotVerfied = errors.New("google says this email is not verified")
	ErrDomain     = errors.New("this email's domain is not allowed")
)

// Client talks to Google.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	slots     chan struct{}
}

// New builds a client. It returns nil if Google sign-in is not configured.
func New(cfg Config) *Client {
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return nil
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = googleAuth
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = googleToken
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = googleJWKS
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 10 * time.Second}, keys: map[string]*rsa.PublicKey{}, slots: make(chan struct{}, 32)}
}

// AuthURL is where to send the browser to start signing in.
func (c *Client) AuthURL(state, nonce string) string {
	v := url.Values{
		"client_id": {c.cfg.ClientID}, "redirect_uri": {c.cfg.RedirectURL}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce}, "prompt": {"select_account"},
	}
	return c.cfg.AuthURL + "?" + v.Encode()
}

// Exchange swaps the one-time code for an identity token. At most 32 exchanges run at once; the rest wait a few
// seconds and then give up, so a flood cannot pile up outgoing requests.
func (c *Client) Exchange(ctx context.Context, code string) (string, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-time.After(5 * time.Second):
		return "", errors.New("google sign-in is busy")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	form := url.Values{"code": {code}, "client_id": {c.cfg.ClientID}, "client_secret": {c.cfg.ClientSecret},
		"redirect_uri": {c.cfg.RedirectURL}, "grant_type": {"authorization_code"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("google refused the code (%d)", res.StatusCode)
	}
	var out struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(body, &out) != nil || out.IDToken == "" {
		return "", errors.New("google sent no identity token")
	}
	return out.IDToken, nil
}

// Verify checks an identity token and returns who it is for.
func (c *Client) Verify(ctx context.Context, idToken, nonce string) (Claims, error) {
	var cl struct {
		jwt.RegisteredClaims
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Name          string `json:"name"`
		Nonce         string `json:"nonce"`
	}
	_, err := jwt.ParseWithClaims(idToken, &cl, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return c.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithAudience(c.cfg.ClientID), jwt.WithExpirationRequired())
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrBadToken, err)
	}
	if iss := cl.Issuer; iss != "https://accounts.google.com" && iss != "accounts.google.com" && !c.customIssuer(iss) {
		return Claims{}, fmt.Errorf("%w: wrong issuer", ErrBadToken)
	}
	if cl.Nonce == "" || cl.Nonce != nonce {
		return Claims{}, fmt.Errorf("%w: this token is not for this sign-in", ErrBadToken)
	}
	if v, _ := cl.EmailVerified.(bool); !v && cl.EmailVerified != "true" {
		return Claims{}, ErrNotVerfied
	}
	email := strings.ToLower(strings.TrimSpace(cl.Email))
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return Claims{}, fmt.Errorf("%w: no email", ErrBadToken)
	}
	if len(c.cfg.AllowedDomains) > 0 {
		ok := false
		for _, d := range c.cfg.AllowedDomains {
			if strings.EqualFold(strings.TrimSpace(d), email[at+1:]) {
				ok = true
			}
		}
		if !ok {
			return Claims{}, ErrDomain
		}
	}
	return Claims{Subject: cl.Subject, Email: email, Name: strings.TrimSpace(cl.Name)}, nil
}

// customIssuer is true only when the addresses were overridden (tests): the issuer is then the fake's own name.
func (c *Client) customIssuer(iss string) bool {
	return c.cfg.JWKSURL != googleJWKS && iss == "https://fake-google.test"
}

// key finds Google's public key by id, fetching the list when it is missing or old (at most once a minute).
func (c *Client) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < time.Hour {
		return k, nil
	}
	if time.Since(c.fetchedAt) < time.Minute && len(c.keys) > 0 {
		return nil, errors.New("unknown signing key")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.JWKSURL, nil)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var set struct {
		Keys []struct {
			Kid, Kty, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	fresh := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, e1 := base64.RawURLEncoding.DecodeString(k.N)
		e, e2 := base64.RawURLEncoding.DecodeString(k.E)
		if e1 != nil || e2 != nil {
			continue
		}
		fresh[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	c.keys, c.fetchedAt = fresh, time.Now()
	if k, ok := fresh[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}
