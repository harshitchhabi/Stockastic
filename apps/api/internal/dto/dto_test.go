package dto_test

import (
	"testing"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/money"
)

func TestRupeesAndPaiseRoundTripExactly(t *testing.T) {
	for _, p := range []money.Paise{0, 1, 5, 99, 100, 10050, 123456789, 100000000} {
		if got := dto.Paise(dto.Rupees(p)); got != p {
			t.Errorf("%d paise -> %v rupees -> %d paise", p, dto.Rupees(p), got)
		}
	}
	// Prices typed by hand with two decimals land on the right paisa despite binary floats.
	for _, tc := range []struct {
		rs   float64
		want money.Paise
	}{{100.05, 10005}, {0.07, 7}, {19.99, 1999}, {1234.56, 123456}, {0.29, 29}} {
		if got := dto.Paise(tc.rs); got != tc.want {
			t.Errorf("%v rupees = %d paise, want %d", tc.rs, got, tc.want)
		}
	}
}
