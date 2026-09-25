package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// bucketSet is a set of token buckets, one per key (an address or an account). A bucket holds up to burst
// tokens and refills at rate tokens a second; a request costs one. Memory is bounded: when there are many
// keys, buckets that have refilled completely (nobody is using them) are dropped.
type bucketSet struct {
	mu    sync.Mutex
	m     map[string]*bucket
	rate  float64
	burst float64
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newBuckets(rate, burst float64) *bucketSet {
	return &bucketSet{m: map[string]*bucket{}, rate: rate, burst: burst}
}

func (b *bucketSet) get(key string, now time.Time) *bucket {
	k := b.m[key]
	if k == nil {
		if len(b.m) > 50000 {
			for x, v := range b.m {
				if v.tokens+now.Sub(v.at).Seconds()*b.rate >= b.burst {
					delete(b.m, x)
				}
			}
		}
		k = &bucket{tokens: b.burst, at: now}
		b.m[key] = k
	}
	if el := now.Sub(k.at).Seconds(); el > 0 {
		k.tokens = min(b.burst, k.tokens+el*b.rate)
		k.at = now
	}
	return k
}

// allow takes one token. If there is none it says how long until there is.
func (b *bucketSet) allow(key string, now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.get(key, now)
	if k.tokens >= 1 {
		k.tokens--
		return true, 0
	}
	return false, time.Duration((1 - k.tokens) / b.rate * float64(time.Second))
}

// peek reports whether a token is available without taking it.
func (b *bucketSet) peek(key string, now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.get(key, now)
	if k.tokens >= 1 {
		return true, 0
	}
	return false, time.Duration((1 - k.tokens) / b.rate * float64(time.Second))
}

// spend takes one token whether or not there is one to spare (a bucket can go below zero).
func (b *bucketSet) spend(key string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.get(key, now)
	k.tokens = max(k.tokens-1, -b.burst)
}

func tooMany(c *gin.Context, wait time.Duration, code, msg string) {
	secs := int(wait.Seconds()) + 1
	c.Header("Retry-After", strconv.Itoa(secs))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": code, "retryAfterSeconds": secs, "message": msg})
}

// limits are the server's protection against one address or one account flooding it. The numbers are generous
// for honest use (a whole venue can share one address; a real browser makes a few requests a second) and
// small next to what the server can do, so they only bite on abuse.
type limits struct {
	ip     *bucketSet // every request from one address
	user   *bucketSet // every request from one signed-in account
	admin  *bucketSet // the organiser's console polls a lot
	signup *bucketSet // sign-ups from one address
}

func newLimits() *limits {
	return &limits{
		ip:     newBuckets(3000, 6000),
		user:   newBuckets(40, 80),
		admin:  newBuckets(300, 600),
		signup: newBuckets(50, 1000),
	}
}

// ipGate is the outermost limit: a flood from one address is turned away before any work is done for it.
//
// It only counts anonymous traffic. A whole venue can share one address, so a rival flooding from inside it must
// not be able to use up the allowance of everyone else: requests that carry a login are limited per account
// instead (accountGate), and the login and sign-up doors have their own protection.
func (s *Server) ipGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch p := c.Request.URL.Path; {
		case strings.HasPrefix(c.GetHeader("Authorization"), "Bearer "), p == "/ws", p == "/api/auth/login", p == "/api/auth/signup":
			c.Next()
			return
		}
		if ok, wait := s.lim.ip.allow(c.ClientIP(), time.Now()); !ok {
			tooMany(c, wait, "too_many_requests", "Too many requests from this connection. Slow down.")
			return
		}
		c.Next()
	}
}

// accountGate limits one signed-in account, so one team cannot jam the server however it is written.
func (s *Server) accountGate(c *gin.Context) {
	u := user(c)
	b := s.lim.user
	if u.IsAdmin {
		b = s.lim.admin
	}
	if ok, wait := b.allow(u.ID, time.Now()); !ok {
		tooMany(c, wait, "too_many_requests", "You are sending requests too fast. Slow down.")
		return
	}
	c.Next()
}
