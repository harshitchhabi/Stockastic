// Package money defines the integer currency type used on every money path.
// All balances, prices and notionals are int64 paise: no floating point where
// value moves. (Ratios and scores elsewhere may use float64.)
package money

import "math"

// Paise is an amount in 1/100 of a rupee.
type Paise int64

// FromRupees converts a rupee amount (as written in rulebook.json) to paise,
// rounding to the nearest paisa.
func FromRupees(r float64) Paise { return Paise(math.Round(r * 100)) }

// Rupees returns the amount in rupees, for display and ratio math only.
func (p Paise) Rupees() float64 { return float64(p) / 100 }
