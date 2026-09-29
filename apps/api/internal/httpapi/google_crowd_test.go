package httpapi_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The start of the event: 1000 people press "Continue with Google" in the same moment, and Google takes 300ms to
// answer each. Every one of them must get through to the create-or-join screen, none turned away as busy.
func TestACrowdSignsInWithGoogleAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	f := newFakeGoogle(t)
	f.delay = 300 * time.Millisecond
	e := googleEnv(t, f, nil)
	const people = 1000
	type result struct{ loc, err string }
	res := make([]result, people)
	// each person's start (and cookie) first, then every callback at once
	type pending struct {
		c           *http.Client
		state, code string
	}
	ps := make([]pending, people)
	for i := range ps {
		// each person has their own connection, opened in advance (a burst of new connections to one port is
		// refused by Windows itself, which says nothing about the server)
		tr := &http.Transport{IdleConnTimeout: time.Minute}
		defer tr.CloseIdleConnections()
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := c.Get(e.srv.URL + "/api/auth/google/start")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body) // read to the end so the connection is kept for the callback
		r.Body.Close()
		loc, _ := url.Parse(r.Header.Get("Location"))
		ps[i] = pending{c, loc.Query().Get("state"), fmt.Sprintf("crowd|person%d@college.edu|%s", i, loc.Query().Get("nonce"))}
	}
	var reused atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range ps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			p := ps[i]
			req, _ := http.NewRequest("GET", e.srv.URL+"/api/auth/google/callback?code="+url.QueryEscape(p.code)+"&state="+url.QueryEscape(p.state), nil)
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(g httptrace.GotConnInfo) {
				if g.Reused {
					reused.Add(1)
				}
			}}))
			r, err := p.c.Do(req)
			if err != nil {
				res[i].err = err.Error()
				return
			}
			r.Body.Close()
			res[i].loc = r.Header.Get("Location")
		}(i)
	}
	t0 := time.Now()
	close(gate)
	wg.Wait()
	bad := map[string]int{}
	for _, r := range res {
		switch {
		case r.err != "":
			bad["error: "+r.err[max(0, len(r.err)-60):]]++
		case !strings.HasPrefix(r.loc, "/#/onboard="):
			bad[r.loc]++
		}
	}
	if len(bad) > 0 {
		t.Logf("reused connections %d", reused.Load())
		t.Fatalf("%d people pressing Google at once, after %s: turned away %v", people, time.Since(t0).Round(time.Millisecond), bad)
	}
	t.Logf("reused connections %d", reused.Load())
	t.Logf("%d people through Google in %s", people, time.Since(t0).Round(time.Millisecond))
}
