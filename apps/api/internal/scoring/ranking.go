package scoring

import (
	"sort"

	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
)

// Standing is one team's Phase-1 result (Sec 4/5).
type Standing struct {
	AccountID string
	// FinalValue is cash + holdings at the freeze price.
	FinalValue money.Paise
	// PeakValue is the highest portfolio value seen during Phase 1 (tie-break 1).
	PeakValue money.Paise
	// Transactions is the executed-transaction count (tie-break 2: fewer wins).
	Transactions int
}

// DecidedBy names what separated a team from the one ranked immediately above it.
type DecidedBy string

const (
	DecidedByValue      DecidedBy = "value"
	DecidedByPeak       DecidedBy = "peak_portfolio_value"
	DecidedByFewerTxns  DecidedBy = "fewer_transactions"
	DecidedByCoinToss   DecidedBy = "coin_toss"
	DecidedByUnresolved DecidedBy = "unresolved"
)

type Ranked struct {
	Standing
	Rank      int
	DecidedBy DecidedBy
}

// RankPhase1 ranks by final value descending (Sec 4), breaking ties with the configured step
// order (Sec 5). The coin toss is an organiser-supervised manual step, so it is injected as a
// stable per-team key rather than randomised here. A tie that no configured step separates is
// reported as DecidedByUnresolved and ordered by account id so the output is deterministic.
func RankPhase1(standings []Standing, tieBreak []rulebook.TieBreakStep, coinToss func(accountID string) int64) []Ranked {
	if coinToss == nil {
		coinToss = func(string) int64 { return 0 }
	}
	decide := func(a, b Standing) (int, DecidedBy) {
		if a.FinalValue != b.FinalValue {
			if a.FinalValue > b.FinalValue {
				return -1, DecidedByValue
			}
			return 1, DecidedByValue
		}
		for _, step := range tieBreak {
			switch step {
			case rulebook.TieBreakPeakValue:
				if a.PeakValue != b.PeakValue {
					if a.PeakValue > b.PeakValue {
						return -1, DecidedByPeak
					}
					return 1, DecidedByPeak
				}
			case rulebook.TieBreakFewerTransactions:
				if a.Transactions != b.Transactions {
					if a.Transactions < b.Transactions {
						return -1, DecidedByFewerTxns
					}
					return 1, DecidedByFewerTxns
				}
			case rulebook.TieBreakCoinToss:
				ca, cb := coinToss(a.AccountID), coinToss(b.AccountID)
				if ca != cb {
					if ca < cb {
						return -1, DecidedByCoinToss
					}
					return 1, DecidedByCoinToss
				}
			}
		}
		if a.AccountID < b.AccountID {
			return -1, DecidedByUnresolved
		}
		return 1, DecidedByUnresolved
	}

	sorted := append([]Standing(nil), standings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		o, _ := decide(sorted[i], sorted[j])
		return o < 0
	})
	out := make([]Ranked, len(sorted))
	for i, s := range sorted {
		by := DecidedByValue
		if i > 0 {
			_, by = decide(sorted[i-1], s)
		}
		out[i] = Ranked{Standing: s, Rank: i + 1, DecidedBy: by}
	}
	return out
}
