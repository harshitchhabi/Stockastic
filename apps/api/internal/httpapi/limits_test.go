package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestBucketsRefillAndAreSeparatePerKey(t *testing.T) {
	b := newBuckets(10, 5)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if ok, _ := b.allow("a", now); !ok {
			t.Fatalf("request %d of the burst was refused", i)
		}
	}
	if ok, wait := b.allow("a", now); ok || wait <= 0 {
		t.Fatalf("the sixth request got through (%v, wait %v)", ok, wait)
	}
	if ok, _ := b.allow("b", now); !ok {
		t.Fatal("another key was slowed by the first")
	}
	if ok, _ := b.allow("a", now.Add(300*time.Millisecond)); !ok {
		t.Fatal("the bucket did not refill")
	}
	// Memory stays bounded: many idle keys are dropped once they have refilled.
	c := newBuckets(1000, 1)
	for i := 0; i < 60000; i++ {
		c.allow(string(rune(i))+"k", now)
	}
	c.allow("last", now.Add(time.Hour))
	if len(c.m) > 60001 {
		t.Fatalf("%d buckets kept", len(c.m))
	}
}

func TestTheAddressGateOnlyCountsAnonymousTraffic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{lim: newLimits()}
	s.lim.ip = newBuckets(1, 3) // three anonymous requests, then one a second
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(s.ipGate())
	ok := func(c *gin.Context) { c.String(200, "ok") }
	r.GET("/anything", ok)
	r.POST("/api/auth/login", ok)
	r.GET("/ws", ok)
	do := func(method, path, auth, xff string) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "198.51.100.9:5555"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < 3; i++ {
		if do("GET", "/anything", "", "") != 200 {
			t.Fatalf("anonymous request %d refused", i)
		}
	}
	if do("GET", "/anything", "", "") != http.StatusTooManyRequests {
		t.Fatal("an anonymous flood was not slowed")
	}
	// Faking the forwarding header does not give a fresh allowance: nobody is trusted to set it here.
	if do("GET", "/anything", "", "203.0.113.5") != http.StatusTooManyRequests {
		t.Fatal("a spoofed X-Forwarded-For got a new allowance")
	}
	// Honest people on the same address are not affected: signed-in requests, the doors and the socket pass.
	for _, c := range []struct{ m, p, a string }{{"GET", "/anything", "Bearer abc"}, {"POST", "/api/auth/login", ""}, {"GET", "/ws", ""}} {
		if got := do(c.m, c.p, c.a, ""); got != 200 {
			t.Fatalf("%s %s (%q) was refused while a flood used up the anonymous allowance: %d", c.m, c.p, c.a, got)
		}
	}
}
