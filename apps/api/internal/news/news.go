// Package news is the two-tier dispatcher (Sec 11/13). In Phase 2 every news item goes to the Fund
// Manager feed at T=0 and to the public feed exactly the configured lead time later, with identical
// content and no exceptions. Phase 1 has no funds and no stagger, and market-regime announcements
// describe overall conditions, so both go to every feed at once. The actual delivery time of each
// item on each feed is recorded, because release-timing disputes are resolved against those logs
// (Sec 22).
package news

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/rulebook"
)

type Kind string

const (
	KindNews   Kind = "news"
	KindRegime Kind = "regime"
)

type Feed string

const (
	FundManagerFeed Feed = "fund_manager"
	PublicFeed      Feed = "public"
)

// Item is the content. It is one value delivered to both feeds, so the two can never differ.
type Item struct {
	ID        string
	Kind      Kind
	Headline  string
	Body      string
	CreatedAt time.Time
}

type Delivery struct {
	Feed Feed
	Item Item
	At   time.Time
}

// Release is the release log entry for one item. A zero time means "not yet delivered to that feed".
type Release struct {
	Item          Item
	FundManagerAt time.Time
	PublicAt      time.Time
}

// Scheduler runs f after d and returns a cancel func. It is injectable so tests control time.
type Scheduler func(d time.Duration, f func()) (cancel func())

func realScheduler(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

type Config struct {
	// Lead is how long after the Fund Manager feed the public feed receives an item (Sec 11: 60s).
	Lead  time.Duration
	Now   func() time.Time
	After Scheduler
	// Stage reports the current event stage; the stagger applies only in Phase 2.
	Stage func() rulebook.Stage
	// Held reports whether the event is paused. While it is, a staggered item's wait for the public stands still:
	// the public gets it only once the event has run for the full lead time after fund managers did.
	Held func() bool
	Log  *slog.Logger
}

type Dispatcher struct {
	cfg Config

	mu       sync.Mutex
	releases []*Release
	byID     map[string]*Release
	subs     []func(Delivery)
	cancels  map[string]func() // pending public deliveries, by item id
}

func New(cfg Config) *Dispatcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = realScheduler
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Dispatcher{cfg: cfg, byID: map[string]*Release{}, cancels: map[string]func(){}}
}

// Subscribe registers a delivery listener. Listeners run synchronously outside the dispatcher's lock
// and must not block; a panic in one is contained.
func (d *Dispatcher) Subscribe(f func(Delivery)) {
	d.mu.Lock()
	d.subs = append(d.subs, f)
	d.mu.Unlock()
}

// tiered reports whether an item published now gets the 60-second stagger.
func (d *Dispatcher) tiered(k Kind) bool {
	return k == KindNews && d.cfg.Stage != nil && d.cfg.Stage() == rulebook.StagePhase2
}

// Publish releases an item. It returns after the first delivery(ies); a staggered public delivery
// happens later via the scheduler.
func (d *Dispatcher) Publish(kind Kind, headline, body string) Item {
	item := Item{ID: ids.New(), Kind: kind, Headline: headline, Body: body, CreatedAt: d.cfg.Now()}
	d.mu.Lock()
	r := &Release{Item: item}
	d.releases = append(d.releases, r)
	d.byID[item.ID] = r
	d.mu.Unlock()

	d.deliver(FundManagerFeed, item)
	if !d.tiered(kind) {
		d.deliver(PublicFeed, item)
		return item
	}
	d.schedulePublic(item, d.cfg.Lead)
	return item
}

// holdStep is how often a waiting item checks whether the event is paused.
const holdStep = 250 * time.Millisecond

func (d *Dispatcher) schedulePublic(item Item, after time.Duration) {
	held := func() bool { return d.cfg.Held != nil && d.cfg.Held() }
	if after <= 0 && !held() {
		d.deliver(PublicFeed, item)
		return
	}
	if d.cfg.Held == nil {
		d.setCancel(item.ID, d.cfg.After(after, func() { d.finish(item) }))
		return
	}
	// Count the wait down only while the event is running.
	var step func(left time.Duration)
	step = func(left time.Duration) {
		if left <= 0 && !held() {
			d.finish(item)
			return
		}
		wait := min(max(left, 0), holdStep)
		if held() {
			wait = time.Second // paused: check once a second for the resume
		} else if wait <= 0 {
			wait = holdStep
		}
		d.setCancel(item.ID, d.cfg.After(wait, func() {
			if !d.pending(item.ID) {
				return // stopped or reset meanwhile
			}
			if !held() {
				left -= wait
			}
			step(left)
		}))
	}
	step(after)
}

func (d *Dispatcher) setCancel(id string, c func()) {
	d.mu.Lock()
	d.cancels[id] = c
	d.mu.Unlock()
}

func (d *Dispatcher) pending(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.cancels[id]
	return ok
}

func (d *Dispatcher) finish(item Item) {
	d.mu.Lock()
	delete(d.cancels, item.ID)
	d.mu.Unlock()
	d.deliver(PublicFeed, item)
}

func (d *Dispatcher) deliver(feed Feed, item Item) {
	at := d.cfg.Now()
	d.mu.Lock()
	r := d.byID[item.ID]
	if r == nil {
		d.mu.Unlock()
		return
	}
	switch feed {
	case FundManagerFeed:
		if !r.FundManagerAt.IsZero() {
			d.mu.Unlock()
			return
		}
		r.FundManagerAt = at
	case PublicFeed:
		if !r.PublicAt.IsZero() {
			d.mu.Unlock()
			return
		}
		r.PublicAt = at
	}
	subs := append([]func(Delivery){}, d.subs...)
	d.mu.Unlock()

	for _, s := range subs {
		d.safe(s, Delivery{Feed: feed, Item: item, At: at})
	}
}

func (d *Dispatcher) safe(s func(Delivery), dl Delivery) {
	defer func() {
		if r := recover(); r != nil {
			d.cfg.Log.Error("news: subscriber panicked", "feed", dl.Feed, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	s(dl)
}

// History returns only what has actually been delivered to a feed, oldest first. The public feed
// therefore never exposes an item before its public release time.
func (d *Dispatcher) History(feed Feed) []Item {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Item
	for _, r := range d.releases {
		if (feed == FundManagerFeed && !r.FundManagerAt.IsZero()) || (feed == PublicFeed && !r.PublicAt.IsZero()) {
			out = append(out, r.Item)
		}
	}
	return out
}

// Releases is the full release log (per-feed delivery timestamps) for dispute resolution.
func (d *Dispatcher) Releases() []Release {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Release, len(d.releases))
	for i, r := range d.releases {
		out[i] = *r
	}
	return out
}

// Recover reinstates the release log after a restart and completes any public delivery that was
// still pending: an item already sent to Fund Managers whose public time has passed goes out now,
// otherwise it is rescheduled for the remaining time.
func (d *Dispatcher) Recover(log []Release) {
	d.mu.Lock()
	for i := range log {
		r := log[i]
		d.releases = append(d.releases, &r)
		d.byID[r.Item.ID] = &r
	}
	d.mu.Unlock()
	now := d.cfg.Now()
	for _, r := range log {
		if r.FundManagerAt.IsZero() {
			d.deliver(FundManagerFeed, r.Item)
		}
		if r.PublicAt.IsZero() {
			due := r.FundManagerAt.Add(d.cfg.Lead)
			if r.FundManagerAt.IsZero() || !d.tiered(r.Item.Kind) {
				due = now
			}
			d.schedulePublic(r.Item, due.Sub(now))
		}
	}
}

// Stop cancels every pending staggered delivery (shutdown). Pending items are recovered on restart.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	cs := d.cancels
	d.cancels = map[string]func(){}
	d.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

// Reset cancels pending deliveries and forgets every release (a new event).
func (d *Dispatcher) Reset() {
	d.Stop()
	d.mu.Lock()
	d.releases = nil
	d.byID = map[string]*Release{}
	d.mu.Unlock()
}
