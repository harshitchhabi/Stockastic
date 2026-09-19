// Package scoring is pure calculation: Phase-1 ranking and tie-breaks, mirror pairing,
// management/performance fees with clawback, allocation rules, and Prizes 1/2/4 scoring.
// No I/O and no clocks, so it is unit-testable against the rulebook's worked examples.
package scoring

import "fmt"

// FundPairing is one Fund Management Team's two source teams (Sec 6).
type FundPairing[T any] struct {
	// FundNumber follows pairing order (1..N), independent of the fund's chosen name.
	FundNumber int
	Stronger   T
	Weaker     T
}

// MirrorPairing implements Sec 6: Fund Team k merges Phase-1 Rank k with Rank (N+1-k).
// The input must be sorted best-first and contain a non-zero even number of qualifiers.
func MirrorPairing[T any](bestFirst []T) ([]FundPairing[T], error) {
	n := len(bestFirst)
	if n == 0 || n%2 != 0 {
		return nil, fmt.Errorf("mirror pairing needs a non-zero even number of qualifiers, got %d", n)
	}
	out := make([]FundPairing[T], n/2)
	for k := 0; k < n/2; k++ {
		out[k] = FundPairing[T]{FundNumber: k + 1, Stronger: bestFirst[k], Weaker: bestFirst[n-1-k]}
	}
	return out, nil
}
