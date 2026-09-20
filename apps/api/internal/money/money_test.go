package money

import "testing"

func TestFromRupeesRoundsToTheNearestPaisa(t *testing.T) {
	cases := []struct {
		rupees float64
		want   Paise
	}{
		{0, 0},
		{1, 100},
		{100.50, 10050},
		{0.01, 1},
		{0.005, 1}, // half a paisa rounds up
		{0.004, 0}, // below half rounds down
		{1_000_000, 100_000_000},
		{-2.5, -250},
		{19.99, 1999}, // 19.99 is not exactly representable in binary; must still land on 1999
		{0.1 + 0.2, 30},
	}
	for _, c := range cases {
		if got := FromRupees(c.rupees); got != c.want {
			t.Errorf("FromRupees(%v) = %d, want %d", c.rupees, got, c.want)
		}
	}
}

func TestRupeesRoundTripsWholePaise(t *testing.T) {
	for _, p := range []Paise{0, 1, 99, 100, 10050, 123_456_789, 100_000_000_000} {
		if got := FromRupees(p.Rupees()); got != p {
			t.Errorf("%d paise -> %v rupees -> %d paise", p, p.Rupees(), got)
		}
	}
}

func TestLargeBalancesStayExact(t *testing.T) {
	// A whole event's money: 750 teams x 10,00,000 rupees, in paise, is far below the point where a
	// float64 (used only for display) would lose a paisa (2^53), and well inside int64.
	total := Paise(750) * FromRupees(1_000_000)
	if total != 75_000_000_000 {
		t.Fatalf("total = %d", total)
	}
	if float64(total) != float64(int64(total)) {
		t.Error("float64 must represent the event total exactly")
	}
}
