package sim_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/sim"
)

// writeTable writes a tiny price table and scenario and loads them.
func writeTable(t *testing.T, scenario string) sim.Scenario {
	t.Helper()
	dir := t.TempDir()
	table := `{"barSeconds":10,"symbols":["A","B","C","D"],"rows":[[100,200,300,400],[101,199,300,401.5],[102,198,300,403],[103,197,300,404.5]]}`
	if err := os.WriteFile(filepath.Join(dir, "prices.json"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := sim.Load(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(companies()); err != nil {
		t.Fatal(err)
	}
	return sc
}

const tableScenario = `{"newsClock":"market","pricesFile":"prices.json","events":[
 {"id":"n1","atMinute":0.5,"headline":"First","type":"REAL","category":"X"},
 {"id":"n2","atMinute":1,"headline":"Rumour","type":"FAKE","category":"RUMOUR"},
 {"id":"n3","atMinute":1.5,"headline":"Bull run","type":"REGIME","category":"REGIME"}]}`

func TestPricesFollowTheTableExactlyAndOnlyWhileTheMarketIsOpen(t *testing.T) {
	r := newRig(t, writeTable(t, tableScenario), companies(), 0)
	if !r.e.Status().FromTable || r.e.Status().TableSteps != 4 {
		t.Fatalf("status = %+v", r.e.Status())
	}
	r.run(10)
	if r.price("A") != money.FromRupees(101) || r.price("D") != money.FromRupees(401.5) {
		t.Fatalf("after 10 open seconds: A %v D %v", r.price("A"), r.price("D"))
	}
	r.clk.mu.Lock()
	r.clk.open = false
	r.clk.mu.Unlock()
	r.run(300) // closed: nothing moves, and the news does not run on event time
	if r.price("A") != money.FromRupees(101) {
		t.Fatalf("a closed market moved: %v", r.price("A"))
	}
	r.clk.mu.Lock()
	r.clk.open = true
	r.clk.mu.Unlock()
	r.run(10)
	if r.price("B") != money.FromRupees(198) {
		t.Fatalf("second step: B = %v", r.price("B"))
	}
	r.run(10) // the table's last row
	if r.price("A") != money.FromRupees(103) || r.price("D") != money.FromRupees(404.5) {
		t.Fatalf("last step: A = %v, D = %v", r.price("A"), r.price("D"))
	}
}

// If the event runs longer than the data, prices do not freeze: each company carries on from where the table ended,
// moving by about as much as it did in the table (a company that never moved stays put), the same way every time.
func TestPricesKeepMovingAfterTheTableEnds(t *testing.T) {
	run := func() *rig {
		r := newRig(t, writeTable(t, tableScenario), companies(), 0)
		r.run(30) // to the last row
		r.run(600)
		return r
	}
	r := run()
	moved := 0
	for sym, last := range map[string]float64{"A": 103, "B": 197, "D": 404.5} {
		p := r.price(sym)
		if p != money.FromRupees(last) {
			moved++
		}
		if d := float64(p)/float64(money.FromRupees(last)) - 1; d > 0.5 || d < -0.5 {
			t.Errorf("%s went from %v to %v in 60 steps: far more than it ever moved in the table", sym, last, p.Rupees())
		}
	}
	if moved != 3 {
		t.Fatalf("after the table ended only %d of 3 moving companies kept moving", moved)
	}
	if r.price("C") != money.FromRupees(300) {
		t.Errorf("C never moved in the table but moved after it: %v", r.price("C"))
	}
	again := run()
	for _, sym := range []string{"A", "B", "C", "D"} {
		if again.price(sym) != r.price(sym) {
			t.Fatalf("%s: %v one time, %v the next: the market after the table must be repeatable", sym, r.price(sym), again.price(sym))
		}
	}
}

func TestNewsRunsOnMarketTimeAndKindsAreRight(t *testing.T) {
	r := newRig(t, writeTable(t, tableScenario), companies(), 0)
	r.run(29)
	if r.wire.count() != 0 {
		t.Fatalf("after 29 open seconds: %d items, want none yet", r.wire.count())
	}
	r.run(1)
	if r.wire.count() != 1 {
		t.Fatalf("after 30 open seconds: %d items, want the 0.5 minute item", r.wire.count())
	}
	r.run(60) // 90 s of open time: all three
	if r.wire.count() != 3 {
		t.Fatalf("items = %d, want 3", r.wire.count())
	}
	if k := r.wire.items[2].kind; k != news.KindRegime {
		t.Fatalf("a bull run was announced as %v, not as a market regime", k)
	}
}

func TestOrganiserControlsOverAutomaticNews(t *testing.T) {
	r := newRig(t, writeTable(t, tableScenario), companies(), 0)
	// Hold one item, reword another and move it, then let the clock run.
	if err := r.e.SetSkipped("n2", true); err != nil {
		t.Fatal(err)
	}
	later := 2.0
	if err := r.e.EditItem("n1", "Reworded", &later); err != nil {
		t.Fatal(err)
	}
	if err := r.e.EditItem("nope", "x", nil); err == nil {
		t.Fatal("edited an item that does not exist")
	}
	r.run(60)
	if r.wire.count() != 0 {
		t.Fatalf("items after 60 s = %d, want 0 (the run is at 90 s)", r.wire.count())
	}
	r.run(60) // 120 s: n3 (1.5) and the moved n1 (2.0) are out, the held n2 is not
	if r.wire.count() != 2 {
		t.Fatalf("items = %v, want the run and the reworded item", r.wire.items)
	}
	found := false
	for _, it := range r.wire.items {
		if it.headline == "Reworded" {
			found = true
		}
		if it.headline == "Rumour" {
			t.Fatal("a held item was released")
		}
	}
	if !found {
		t.Fatalf("the reworded headline was not used: %v", r.wire.items)
	}
	// Released by hand even though it was held.
	if err := r.e.FireEvent("n2", r.now); err != nil || r.wire.count() != 3 {
		t.Fatalf("release by hand: %v, %d items", err, r.wire.count())
	}
	if err := r.e.SetSkipped("n2", false); err == nil {
		t.Fatal("changed an item that was already released")
	}

	// Manual mode: nothing comes out by itself.
	r2 := newRig(t, writeTable(t, tableScenario), companies(), 0)
	if err := r2.e.SetNewsManual(true); err != nil {
		t.Fatal(err)
	}
	r2.run(300)
	if r2.wire.count() != 0 {
		t.Fatalf("manual mode released %d items", r2.wire.count())
	}
	if !r2.e.Status().NewsManual {
		t.Fatal("status does not say manual")
	}

	// The choices survive a restart.
	last := r.saved[len(r.saved)-1]
	r3 := newRig(t, writeTable(t, tableScenario), companies(), 0)
	r3.e.Adopt(last)
	if st := r3.e.Status(); len(st.Items) != 3 {
		t.Fatalf("items after adopting = %d", len(st.Items))
	}
	_ = time.Second
}

func TestABrokenTableIsRefused(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prices.json"), []byte(`{"barSeconds":10,"symbols":["A","B","C","X"],"rows":[[1,2,3,4]]}`), 0o600)
	os.WriteFile(filepath.Join(dir, "s.json"), []byte(`{"pricesFile":"prices.json"}`), 0o600)
	sc, err := sim.Load(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(companies()); err == nil {
		t.Fatal("a table with the wrong companies was accepted")
	}
}

// A restart after the table has ended carries on with exactly the prices an uninterrupted server would have made.
func TestARestartAfterTheTableContinuesExactly(t *testing.T) {
	sc := writeTable(t, tableScenario)
	straight := newRig(t, sc, companies(), 0)
	straight.run(900)

	a := newRig(t, sc, companies(), 0)
	a.run(400)
	last := a.saved[len(a.saved)-1]
	b := newRig(t, sc, companies(), 0)
	b.now, b.clk.elapsed = a.now, a.clk.elapsed
	restored := map[string]money.Paise{}
	for _, c := range companies() {
		restored[c.Symbol] = a.price(c.Symbol)
	}
	for k, v := range last.Prices {
		restored[k] = money.Paise(v)
	}
	b.prices.SetAll(restored, a.now)
	b.e.Adopt(last)
	b.run(500)
	for _, c := range companies() {
		if b.price(c.Symbol) != straight.price(c.Symbol) {
			t.Errorf("%s after a restart = %d, uninterrupted = %d", c.Symbol, b.price(c.Symbol), straight.price(c.Symbol))
		}
	}
}
