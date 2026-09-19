package news

import (
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeTime is a controllable clock plus scheduler.
type fakeTime struct {
	mu      sync.Mutex
	t       time.Time
	pending []*job
}

type job struct {
	due       time.Time
	f         func()
	cancelled bool
}

func (f *fakeTime) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

func (f *fakeTime) after(d time.Duration, fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := &job{due: f.t.Add(d), f: fn}
	f.pending = append(f.pending, j)
	return func() { f.mu.Lock(); j.cancelled = true; f.mu.Unlock() }
}

func (f *fakeTime) advance(d time.Duration) {
	f.mu.Lock()
	target := f.t.Add(d)
	f.mu.Unlock()
	for {
		f.mu.Lock()
		sort.Slice(f.pending, func(i, j int) bool { return f.pending[i].due.Before(f.pending[j].due) })
		var next *job
		for _, j := range f.pending {
			if !j.cancelled && !j.due.After(target) {
				next = j
				break
			}
		}
		if next == nil {
			f.t = target
			f.mu.Unlock()
			return
		}
		f.t = next.due
		next.cancelled = true
		f.mu.Unlock()
		next.f()
	}
}

type rig struct {
	d     *Dispatcher
	ft    *fakeTime
	stage rulebook.Stage
	got   []Delivery
	mu    sync.Mutex
}

func newRig(stage rulebook.Stage) *rig {
	r := &rig{ft: &fakeTime{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}, stage: stage}
	r.d = New(Config{Lead: 60 * time.Second, Now: r.ft.now, After: r.ft.after, Stage: func() rulebook.Stage { return r.stage }, Log: quiet})
	r.d.Subscribe(func(dl Delivery) { r.mu.Lock(); r.got = append(r.got, dl); r.mu.Unlock() })
	return r
}

func (r *rig) deliveries() []Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Delivery(nil), r.got...)
}

func TestPhase2NewsGoesToFundManagersNowAndThePublicExactlySixtySecondsLater(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	item := r.d.Publish(KindNews, "RBI cuts rates", "25bps")

	if got := r.deliveries(); len(got) != 1 || got[0].Feed != FundManagerFeed {
		t.Fatalf("at T=0 only the Fund Manager feed may have it: %+v", got)
	}
	if h := r.d.History(PublicFeed); len(h) != 0 {
		t.Fatalf("the public feed must not expose the item early: %+v", h)
	}
	r.ft.advance(59*time.Second + 999*time.Millisecond)
	if len(r.deliveries()) != 1 {
		t.Fatal("59.999s: still too early for the public")
	}
	r.ft.advance(time.Millisecond)
	got := r.deliveries()
	if len(got) != 2 || got[1].Feed != PublicFeed {
		t.Fatalf("at T=60s the public gets it: %+v", got)
	}
	if got[1].At.Sub(got[0].At) != 60*time.Second {
		t.Errorf("lead = %v, want exactly 60s", got[1].At.Sub(got[0].At))
	}
	if got[0].Item != got[1].Item || got[0].Item.ID != item.ID {
		t.Error("content must be identical on both feeds")
	}
	if h := r.d.History(PublicFeed); len(h) != 1 {
		t.Errorf("public history after release: %+v", h)
	}
}

func TestNoFeedEverGetsAnItemBeforeTheFundManagerFeed(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	for i := 0; i < 20; i++ {
		r.d.Publish(KindNews, "n", "")
		r.ft.advance(7 * time.Second)
	}
	r.ft.advance(2 * time.Minute)
	rel := r.d.Releases()
	if len(rel) != 20 {
		t.Fatal("setup")
	}
	for _, x := range rel {
		if x.FundManagerAt.IsZero() || x.PublicAt.IsZero() || x.PublicAt.Sub(x.FundManagerAt) != 60*time.Second {
			t.Errorf("release log wrong: %+v", x)
		}
	}
}

func TestPhase1HasNoStagger(t *testing.T) {
	r := newRig(rulebook.StagePhase1)
	r.d.Publish(KindNews, "level field", "")
	got := r.deliveries()
	if len(got) != 2 {
		t.Fatalf("Phase 1 is a level playing field: both feeds at once, got %+v", got)
	}
	if got[0].At != got[1].At {
		t.Error("same instant")
	}
}

func TestRegimeAnnouncementsGoToEveryoneSimultaneouslyEvenInPhase2(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	r.d.Publish(KindRegime, "The market has entered a Bull Run", "")
	if got := r.deliveries(); len(got) != 2 {
		t.Fatalf("a broad regime shift is announced to all at once (Sec 13): %+v", got)
	}
	// ...but a specific news item explaining it still follows the stagger.
	r.d.Publish(KindNews, "Explaining item", "")
	if got := r.deliveries(); len(got) != 3 {
		t.Fatalf("news is staggered: %+v", got)
	}
}

func TestReleaseLogRecordsActualDeliveryTimes(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	r.ft.advance(3 * time.Second)
	r.d.Publish(KindNews, "x", "")
	r.ft.advance(time.Minute)
	rel := r.d.Releases()[0]
	base := time.Date(2026, 3, 1, 12, 0, 3, 0, time.UTC)
	if !rel.FundManagerAt.Equal(base) || !rel.PublicAt.Equal(base.Add(time.Minute)) {
		t.Errorf("%+v", rel)
	}
}

func TestRecoverCompletesPendingPublicDeliveriesAfterARestart(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	sent := time.Date(2026, 3, 1, 11, 59, 30, 0, time.UTC) // Fund Managers got it 30s ago
	pending := Release{Item: Item{ID: "n1", Kind: KindNews, Headline: "pending"}, FundManagerAt: sent}
	overdue := Release{Item: Item{ID: "n2", Kind: KindNews, Headline: "overdue"}, FundManagerAt: sent.Add(-10 * time.Minute)}
	r.d.Recover([]Release{pending, overdue})

	pubNow := func() (ids []string) {
		for _, d := range r.deliveries() {
			if d.Feed == PublicFeed {
				ids = append(ids, d.Item.ID)
			}
		}
		return
	}
	if got := pubNow(); len(got) != 1 || got[0] != "n2" {
		t.Fatalf("the overdue item goes out immediately, the other waits: %v", got)
	}
	r.ft.advance(29 * time.Second)
	if len(pubNow()) != 1 {
		t.Error("30s remain on the pending item")
	}
	r.ft.advance(time.Second)
	if got := pubNow(); len(got) != 2 {
		t.Errorf("the pending item is released at its original T+60s: %v", got)
	}
	if h := r.d.History(FundManagerFeed); len(h) != 2 {
		t.Errorf("recovered items must show in the FM history: %+v", h)
	}
}

func TestStopCancelsPendingDeliveries(t *testing.T) {
	r := newRig(rulebook.StagePhase2)
	r.d.Publish(KindNews, "x", "")
	r.d.Stop()
	r.ft.advance(2 * time.Minute)
	if h := r.d.History(PublicFeed); len(h) != 0 {
		t.Error("a stopped dispatcher must not release later (the item is recovered on restart)")
	}
}

func TestASubscriberPanicDoesNotBreakDelivery(t *testing.T) {
	r := newRig(rulebook.StagePhase1)
	r.d.Subscribe(func(Delivery) { panic("bad subscriber") })
	r.d.Publish(KindNews, "x", "")
	if len(r.deliveries()) != 2 {
		t.Error("healthy subscriber must still get both deliveries")
	}
}

func TestDuplicateDeliveryIsIgnored(t *testing.T) {
	r := newRig(rulebook.StagePhase1)
	item := r.d.Publish(KindNews, "x", "")
	r.d.deliver(PublicFeed, item)
	r.d.deliver(FundManagerFeed, item)
	if len(r.deliveries()) != 2 {
		t.Errorf("each feed delivers an item once: %d", len(r.deliveries()))
	}
}
