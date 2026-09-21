package sim_test

import (
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
