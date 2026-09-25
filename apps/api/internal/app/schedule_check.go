package app

import (
	"fmt"
	"math"
	"strings"

	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/rulebook"
)

// BlockTime says where a block sits on the event clock and on the market clock (minutes of open trading).
type BlockTime struct {
	ID             string  `json:"id"`
	ClockStartMin  float64 `json:"clockStartMin"`
	MarketStartMin float64 `json:"marketStartMin"`
	MarketOpen     bool    `json:"marketOpen"`
	Minutes        float64 `json:"minutes"`
}

// ScheduleCheck compares the organiser's schedule with what the price data and the news need.
type ScheduleCheck struct {
	OpenMinutes    float64     `json:"openMinutes"`    // minutes of open trading the schedule gives
	DataMinutes    float64     `json:"dataMinutes"`    // minutes of open trading the price table covers (0 if none)
	LastNewsMinute float64     `json:"lastNewsMinute"` // when the last news item is due, in open-market minutes
	Blocks         []BlockTime `json:"blocks"`
	Breaks         []string    `json:"breaks"` // stretches where the market is closed between open blocks
	Warnings       []string    `json:"warnings"`
}

// timesOf lays the schedule out: for each block, its start on the event clock and on the market clock.
func (a *App) timesOf() []BlockTime {
	var out []BlockTime
	var clock, market float64
	for _, b := range a.Clock.Schedule() {
		m := b.Duration.Minutes()
		out = append(out, BlockTime{ID: b.Block.ID, ClockStartMin: clock, MarketStartMin: market, MarketOpen: b.Block.MarketOpen, Minutes: m})
		clock += m
		if b.Block.MarketOpen {
			market += m
		}
	}
	return out
}

// clockMinuteOfMarketMinute converts a moment measured in open-market minutes into event-clock minutes, assuming
// the market is open exactly when the schedule says. ok is false if the schedule has fewer open minutes.
func (a *App) clockMinuteOfMarketMinute(m float64) (clock float64, block eventclock.ScheduledBlock, ok bool) {
	var market float64
	var start float64
	for _, b := range a.Clock.Schedule() {
		d := b.Duration.Minutes()
		if b.Block.MarketOpen {
			if m < market+d || (m == market+d && false) {
				return start + (m - market), b, true
			}
			market += d
		}
		start += d
	}
	return 0, eventclock.ScheduledBlock{}, false
}

// CheckSchedule is the report the organiser sees under the schedule editor.
func (a *App) CheckSchedule() ScheduleCheck {
	c := ScheduleCheck{Blocks: a.timesOf(), Breaks: []string{}, Warnings: []string{}}
	for _, b := range c.Blocks {
		if b.MarketOpen {
			c.OpenMinutes += b.Minutes
		}
	}
	st := a.Sim.Status()
	if st.FromTable {
		c.DataMinutes = float64(st.TableSteps*st.TickSeconds) / 60
	}
	if c.OpenMinutes == 0 {
		c.Warnings = append(c.Warnings, "No block has the market open, so nobody can trade and no prices or news will move.")
	}
	if c.DataMinutes > 0 {
		switch {
		case c.OpenMinutes+0.01 < c.DataMinutes:
			c.Warnings = append(c.Warnings, fmt.Sprintf("Your schedule gives %.0f minutes of open trading but the price data covers %.0f. The last %.0f minutes of prices will not play, and neither will any news due after minute %.0f of trading.",
				c.OpenMinutes, c.DataMinutes, c.DataMinutes-c.OpenMinutes, c.OpenMinutes))
		case c.OpenMinutes > c.DataMinutes+0.01:
			c.Warnings = append(c.Warnings, fmt.Sprintf("Your schedule gives %.0f minutes of open trading but the price data covers %.0f. After minute %.0f prices stop changing.", c.OpenMinutes, c.DataMinutes, c.DataMinutes))
		}
	}
	// Breaks: closed stretches between open blocks. Nothing moves in them, and the news and prices carry on after.
	var run []string
	seenOpen := false
	for _, b := range c.Blocks {
		if b.MarketOpen {
			if seenOpen && len(run) > 0 {
				c.Breaks = append(c.Breaks, fmt.Sprintf("%s (%.0f min)", strings.Join(run, ", "), sumMinutes(c.Blocks, run)))
			}
			run, seenOpen = nil, true
		} else if seenOpen {
			run = append(run, b.ID)
		}
	}
	// News that will never go out, and news that lands in a block of another phase.
	stages := map[string]rulebook.Stage{}
	for _, b := range a.Clock.Schedule() {
		stages[b.Block.ID] = b.Block.Stage
	}
	never, wrong := []string{}, []string{}
	for _, it := range st.Items {
		if it.Kind != "news" || it.Fired || it.Skipped {
			continue
		}
		if st.NewsClock == "market" {
			c.LastNewsMinute = math.Max(c.LastNewsMinute, it.AtMinute)
			_, blk, ok := a.clockMinuteOfMarketMinute(it.AtMinute)
			if !ok {
				never = append(never, it.ID)
				continue
			}
			if want := it.Phase; want != "" && string(stages[blk.Block.ID]) != want {
				wrong = append(wrong, it.ID)
			}
		}
	}
	if len(never) > 0 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%d news items are due after the schedule runs out of open trading and will never go out by themselves (%s%s).", len(never), strings.Join(first(never, 5), ", "), more(len(never), 5)))
	}
	if len(wrong) > 0 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%d news items fall in a block of a different phase than the data was designed for (%s%s). The fund managers' 60 second head start only applies in Phase 2 blocks, so check the phase of the blocks around them.", len(wrong), strings.Join(first(wrong, 5), ", "), more(len(wrong), 5)))
	}
	return c
}

func sumMinutes(bs []BlockTime, ids []string) float64 {
	var t float64
	for _, id := range ids {
		for _, b := range bs {
			if b.ID == id {
				t += b.Minutes
			}
		}
	}
	return t
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func more(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" and %d more", total-shown)
	}
	return ""
}
