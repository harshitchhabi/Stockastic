package sim_test

import (
	"os"
	"testing"

	"stockastic/api/internal/sim"
	"stockastic/api/internal/universe"
)

// The mock master data must always load, so a bad re-import is caught before an event.
func TestMockScenarioMatchesMockUniverse(t *testing.T) {
	cs, err := universe.Load("../../scenarios/mock/universe.json")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := sim.Load("../../scenarios/mock/scenario.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) != 150 {
		t.Fatalf("companies = %d, want 150", len(cs))
	}
}

// The organisers' final data is git-ignored (it holds the future prices), so this only runs where it is present.
func TestFinalDataLoadsIfPresent(t *testing.T) {
	const dir = "../../scenarios/final/"
	if _, err := os.Stat(dir + "scenario.json"); err != nil {
		t.Skip("no final data here")
	}
	cs, err := universe.Load(dir + "universe.json")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := sim.Load(dir + "scenario.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) != 150 || !sc.HasTable() || sc.TableSteps() != 1020 {
		t.Fatalf("companies %d, table %v, steps %d", len(cs), sc.HasTable(), sc.TableSteps())
	}
}
