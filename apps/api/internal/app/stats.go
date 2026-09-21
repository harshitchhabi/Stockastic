package app

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// perMinute counts events over a sliding 60 seconds using one-second buckets.
type perMinute struct {
	mu      sync.Mutex
	buckets [60]int64
	stamps  [60]int64 // unix second each bucket belongs to
}

func (p *perMinute) Add(n int64, now time.Time) {
	sec := now.Unix()
	i := int(sec % 60)
	p.mu.Lock()
	if p.stamps[i] != sec {
		p.stamps[i], p.buckets[i] = sec, 0
	}
	p.buckets[i] += n
	p.mu.Unlock()
}

func (p *perMinute) Sum(now time.Time) (total int64) {
	sec := now.Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.buckets {
		if sec-p.stamps[i] < 60 && p.stamps[i] <= sec {
			total += p.buckets[i]
		}
	}
	return total
}

// durations keeps the most recent N durations for percentile reporting.
type durations struct {
	mu  sync.Mutex
	buf []time.Duration
	pos int
	n   int
}

func newDurations(n int) *durations { return &durations{buf: make([]time.Duration, n)} }

func (d *durations) Add(x time.Duration) {
	d.mu.Lock()
	d.buf[d.pos] = x
	d.pos = (d.pos + 1) % len(d.buf)
	if d.n < len(d.buf) {
		d.n++
	}
	d.mu.Unlock()
}

// Percentiles returns the p50 and p99 of what has been recorded (zero if nothing yet).
func (d *durations) Percentiles() (p50, p99 time.Duration) {
	d.mu.Lock()
	cp := append([]time.Duration(nil), d.buf[:d.n]...)
	d.mu.Unlock()
	if len(cp) == 0 {
		return 0, 0
	}
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	at := func(q float64) time.Duration { return cp[min(len(cp)-1, int(q*float64(len(cp))))] }
	return at(0.50), at(0.99)
}

// ErrorEntry is one recent server error, shown on the organiser Systems page.
type ErrorEntry struct {
	At      time.Time
	Message string
}

type errRing struct {
	mu   sync.Mutex
	list []ErrorEntry
}

func (r *errRing) add(msg string, at time.Time) {
	r.mu.Lock()
	r.list = append(r.list, ErrorEntry{At: at, Message: msg})
	if len(r.list) > 50 {
		r.list = r.list[len(r.list)-50:]
	}
	r.mu.Unlock()
}

func (r *errRing) recent(n int) []ErrorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := max(0, len(r.list)-n)
	out := append([]ErrorEntry(nil), r.list[from:]...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// captureHandler passes every record on to inner and remembers the errors for the Systems page.
type captureHandler struct {
	inner slog.Handler
	ring  *errRing
}

func (h captureHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}
func (h captureHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		h.ring.add(r.Message, r.Time)
	}
	return h.inner.Handle(ctx, r)
}
func (h captureHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return captureHandler{h.inner.WithAttrs(a), h.ring}
}
func (h captureHandler) WithGroup(n string) slog.Handler {
	return captureHandler{h.inner.WithGroup(n), h.ring}
}
