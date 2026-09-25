package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"stockastic/api/internal/store"
)

// Tracking keeps a record of what people did and how each team's wallet moved, for the organisers to look back on.
// It is written to the same durable record as everything else but is best effort: it is queued and written in the
// background, and it is dropped rather than allowed to slow trading or sign-in down. Replay ignores it.

const (
	trackQueue    = 8192
	maxTrackField = 300
)

type tracker struct {
	ch   chan store.Activity
	mu   sync.Mutex
	last map[string]time.Time // throttle for repeated failed sign-ins from one address
	wg   sync.WaitGroup
}

func cut(s string) string {
	if len(s) > maxTrackField {
		s = s[:maxTrackField]
	}
	return strings.ToValidUTF8(s, "")
}

// Track notes something a person did. account may be empty (a failed sign-in for an unknown email). It never blocks.
func (a *App) Track(account, typ, ip, userAgent, detail string) {
	t := a.tracker
	if t == nil {
		return
	}
	now := a.now()
	if typ == "login_failed" {
		t.mu.Lock()
		k := ip + "|" + account
		if now.Sub(t.last[k]) < 10*time.Second {
			t.mu.Unlock()
			return
		}
		if len(t.last) > 20000 {
			t.last = map[string]time.Time{}
		}
		t.last[k] = now
		t.mu.Unlock()
	}
	select {
	case t.ch <- store.Activity{Account: account, Type: typ, IP: cut(ip), UserAgent: cut(userAgent), Detail: cut(detail), At: now.UnixMilli()}:
	default: // full: drop rather than wait
	}
}

func (a *App) startTracking(ctx context.Context) {
	t := a.tracker
	t.wg.Add(2)
	go func() {
		defer t.wg.Done()
		write := func(x store.Activity) {
			if err := a.wal.Append(store.KindActivity, x); err != nil {
				a.log.Warn("could not save an activity record", "err", err)
			}
		}
		for {
			select {
			case x := <-t.ch:
				write(x)
			case <-ctx.Done():
				for { // save what is already queued, then stop
					select {
					case x := <-t.ch:
						write(x)
					default:
						return
					}
				}
			}
		}
	}()
	go func() {
		defer t.wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a.recordWallets()
			}
		}
	}()
}

// recordWallets writes every team's cash and total value while the event is running.
func (a *App) recordWallets() {
	if p := a.Clock.Position(); !p.Started || p.Ended {
		return
	}
	navs := a.navs()
	w := store.Wallets{At: a.now().UnixMilli(), W: map[string][2]int64{}}
	for _, id := range a.teamIDs() {
		snap, err := a.Ledger.Snapshot(id)
		if err != nil {
			continue
		}
		w.W[id] = [2]int64{int64(snap.Cash), int64(a.totalValue(id, navs))}
	}
	if len(w.W) == 0 {
		return
	}
	if err := a.wal.Append(store.KindWallets, w); err != nil {
		a.log.Warn("could not save wallet history", "err", err)
	}
}

// PastRecords returns the lookup for past records, or nil when the platform is not using PostgreSQL.
func (a *App) PastRecords() store.History { return a.cfg.History }

// DBHealthy is false when the database has stopped accepting writes.
func (a *App) DBHealthy() bool {
	return a.cfg.Health == nil || a.cfg.Health() == nil
}
