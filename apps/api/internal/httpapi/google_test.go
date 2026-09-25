package httpapi_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"stockastic/api/internal/app"
	"stockastic/api/internal/httpapi"
	"stockastic/api/internal/oauth"
	"stockastic/api/internal/store"
)

// fakeGoogle stands in for Google: it signs identity tokens with its own key and serves that key.
type fakeGoogle struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	mu  sync.Mutex
	// what the next token says
	email, name, nonce, aud string
	verified                bool
	signWith                *rsa.PrivateKey
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGoogle{key: k, verified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/certs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.FormValue("code") != "good-code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		key := f.key
		if f.signWith != nil {
			key = f.signWith
		}
		aud := f.aud
		if aud == "" {
			aud = "client-123"
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://fake-google.test", "aud": aud, "sub": "sub-" + f.email, "exp": time.Now().Add(time.Hour).Unix(),
			"email": f.email, "email_verified": f.verified, "name": f.name, "nonce": f.nonce,
		})
		tok.Header["kid"] = "k1"
		s, _ := tok.SignedString(key)
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": s})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// signIn runs the whole round trip in a browser-like client and returns where the callback sent it.
func signIn(t *testing.T, e *env, f *fakeGoogle, start string, tweak func(state string) string) (location string, jar http.CookieJar) {
	t.Helper()
	jar, _ = cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(e.srv.URL + start)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("start: %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), f.srv.URL+"/auth") || loc.Query().Get("client_id") != "client-123" || loc.Query().Get("scope") != "openid email profile" {
		t.Fatalf("redirect to Google = %s", loc)
	}
	state, nonce := loc.Query().Get("state"), loc.Query().Get("nonce")
	f.mu.Lock()
	if f.nonce == "" {
		f.nonce = nonce
	}
	f.mu.Unlock()
	if tweak != nil {
		state = tweak(state)
	}
	cb, err := c.Get(e.srv.URL + "/api/auth/google/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	return cb.Header.Get("Location"), jar
}

func googleEnv(t *testing.T, f *fakeGoogle, domains []string, cfg ...func(*app.Config)) *env {
	t.Helper()
	extraHTTP = func(o *httpapi.Options) {
		o.Google = oauth.New(oauth.Config{ClientID: "client-123", ClientSecret: "secret", RedirectURL: "https://event.test/api/auth/google/callback",
			AllowedDomains: domains, AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token", JWKSURL: f.srv.URL + "/certs"})
		o.GoogleRedirect = "https://event.test/api/auth/google/callback"
		o.StateKey = []byte(strings.Repeat("s", 40))
	}
	t.Cleanup(func() { extraHTTP = nil })
	return newEnvWith(t, store.NewMem(), rb(t, 100), testScenario(), cfg...)
}

func tokenIn(t *testing.T, loc string) string {
	t.Helper()
	if !strings.HasPrefix(loc, "/#/signin=") {
		t.Fatalf("expected a sign-in, got %q", loc)
	}
	return strings.TrimPrefix(loc, "/#/signin=")
}

func TestGoogleSignInCreatesAndFindsTheAccount(t *testing.T) {
	f := newFakeGoogle(t)
	f.email, f.name = "asha@college.edu", "Asha Rao"
	e := googleEnv(t, f, nil)
	if st := e.call("GET", "/api/status", "", nil).Body; st["googleEnabled"] != true {
		t.Fatalf("status = %v", st)
	}
	loc, _ := signIn(t, e, f, "/api/auth/google/start", nil)
	tok := tokenIn(t, loc)
	me := e.call("GET", "/api/auth/me", tok, nil).Body
	if me["displayName"] != "Asha Rao" || me["email"] != "asha@college.edu" || me["role"] != "investor" || num(me["cashBalance"]) != 1_000_000 {
		t.Fatalf("the new account = %v", me)
	}
	// Signing in again finds the same account, and it can trade like any other.
	f.nonce = ""
	loc2, _ := signIn(t, e, f, "/api/auth/google/start", nil)
	me2 := e.call("GET", "/api/auth/me", tokenIn(t, loc2), nil).Body
	if me2["id"] != me["id"] {
		t.Fatalf("a second sign-in made a second account: %v vs %v", me["id"], me2["id"])
	}
	e.openMarket(e.admin())
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("trade: %d %s", r.Status, r.Raw)
	}
	// The password door stays shut for it: nobody knows a password.
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "asha@college.edu", "password": "password-123"}); r.Status != 401 {
		t.Fatalf("password login on a Google account: %d", r.Status)
	}
}

func TestGoogleSignInRefusesForgeries(t *testing.T) {
	f := newFakeGoogle(t)
	f.email, f.name = "asha@college.edu", "Asha"
	e := googleEnv(t, f, nil)
	fail := func(name, want string, loc string) {
		t.Helper()
		if loc != "/#/signin-error="+want {
			t.Errorf("%s: got %q, want the error %q", name, loc, want)
		}
	}
	// A return that was not started in this browser (the cookie is missing).
	starter := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sres, _ := starter.Get(e.srv.URL + "/api/auth/google/start")
	sres.Body.Close()
	su, _ := url.Parse(sres.Header.Get("Location"))
	f.nonce = su.Query().Get("nonce")
	stranger := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	nres, _ := stranger.Get(e.srv.URL + "/api/auth/google/callback?code=good-code&state=" + url.QueryEscape(su.Query().Get("state")))
	nres.Body.Close()
	fail("no cookie", "expired", nres.Header.Get("Location"))
	f.nonce = ""
	// Tampered state.
	loc, _ := signIn(t, e, f, "/api/auth/google/start", func(s string) string { return "x" + s })
	fail("tampered state", "expired", loc)
	f.nonce = ""
	// A token issued for another app.
	f.aud = "someone-elses-app"
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	fail("wrong audience", "google", loc)
	f.aud, f.nonce = "", ""
	// A token signed by a key Google does not publish.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	f.signWith = other
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	fail("unknown signing key", "google", loc)
	f.signWith, f.nonce = nil, ""
	// A token meant for a different sign-in (wrong nonce).
	f.nonce = "not-this-one"
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	fail("wrong nonce", "google", loc)
	f.nonce = ""
	// An email Google has not verified.
	f.verified = false
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	fail("unverified email", "unverified", loc)
	f.verified, f.nonce = true, ""
	// A return that is replayed after it worked.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ := c.Get(e.srv.URL + "/api/auth/google/start")
	res.Body.Close()
	u, _ := url.Parse(res.Header.Get("Location"))
	f.nonce = u.Query().Get("nonce")
	cb := e.srv.URL + "/api/auth/google/callback?code=good-code&state=" + url.QueryEscape(u.Query().Get("state"))
	first, _ := c.Get(cb)
	first.Body.Close()
	if !strings.HasPrefix(first.Header.Get("Location"), "/#/signin=") {
		t.Fatalf("the first return: %q", first.Header.Get("Location"))
	}
	second, _ := c.Get(cb)
	second.Body.Close()
	fail("replayed return", "expired", second.Header.Get("Location"))
	// A code Google refuses.
	res, _ = c.Get(e.srv.URL + "/api/auth/google/start")
	res.Body.Close()
	u, _ = url.Parse(res.Header.Get("Location"))
	bad, _ := c.Get(e.srv.URL + "/api/auth/google/callback?code=made-up&state=" + url.QueryEscape(u.Query().Get("state")))
	bad.Body.Close()
	fail("a code Google refuses", "google", bad.Header.Get("Location"))
}

func TestGoogleSignInFollowsTheRegistrationRules(t *testing.T) {
	f := newFakeGoogle(t)
	f.email, f.name = "kim@gmail.com", "Kim"
	e := googleEnv(t, f, []string{"college.edu"}, func(c *app.Config) { c.SignupCode = "ROOM-42" })
	adm := e.admin()

	loc, _ := signIn(t, e, f, "/api/auth/google/start?code=ROOM-42", nil)
	if loc != "/#/signin-error=domain" {
		t.Fatalf("an email outside the allowed domains: %q", loc)
	}
	f.email, f.nonce = "lee@college.edu", ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	if loc != "/#/signin-error=code" {
		t.Fatalf("a new person without the event code: %q", loc)
	}
	f.nonce = ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start?code=WRONG", nil)
	if loc != "/#/signin-error=code" {
		t.Fatalf("a new person with the wrong event code: %q", loc)
	}
	f.nonce = ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start?code=ROOM-42", nil)
	tok := tokenIn(t, loc)
	id := e.call("GET", "/api/auth/me", tok, nil).Body["id"].(string)

	// The approved list applies to people who are new.
	e.call("POST", "/api/admin/settings/allowlist", adm, map[string]any{"emails": []string{"someone.else@college.edu"}})
	f.email, f.nonce = "new@college.edu", ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start?code=ROOM-42", nil)
	if loc != "/#/signin-error=not_on_list" {
		t.Fatalf("a new person who is not on the list: %q", loc)
	}
	// Someone who already has an account can always come back, without the code.
	f.email, f.nonce = "lee@college.edu", ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	tokenIn(t, loc)
	// A locked account cannot come back this way either.
	if r := e.call("POST", "/api/admin/accounts/"+id+"/lock", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("lock: %d", r.Status)
	}
	f.nonce = ""
	loc, _ = signIn(t, e, f, "/api/auth/google/start", nil)
	if loc != "/#/signin-error=locked" {
		t.Fatalf("a locked account: %q", loc)
	}
	// The event code cannot be changed on the way: the state is signed.
	f.email, f.nonce = "sam@college.edu", ""
	e.call("POST", "/api/admin/settings/allowlist", adm, map[string]any{"emails": []string{}})
	loc, _ = signIn(t, e, f, "/api/auth/google/start?code=ROOM-42", func(s string) string {
		parts := strings.Split(s, ".")
		parts[2] = base64.RawURLEncoding.EncodeToString([]byte("ROOM-42")) // same code, but re-encoded: the signature no longer matches
		return strings.Join(parts[:3], ".") + "." + parts[3][:len(parts[3])-2] + "AA"
	})
	if loc != "/#/signin-error=expired" {
		t.Fatalf("a state with a damaged signature: %q", loc)
	}
}

func TestGoogleSignInIsOffUntilConfigured(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	if st := e.call("GET", "/api/status", "", nil).Body; st["googleEnabled"] != false {
		t.Fatalf("status = %v", st)
	}
	res, err := http.Get(e.srv.URL + "/api/auth/google/start")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusFound {
		t.Fatal("Google sign-in answered without being configured")
	}
}
