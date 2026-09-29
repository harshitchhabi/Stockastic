package scoring

// Herfindahl is the HHI of holding weights (cash excluded): 1 = a single holding, toward 0 = spread thin. The
// once-a-minute fund sample uses it to record how widely each investor's money is spread.
func Herfindahl(holdingValues []float64) float64 {
	var total float64
	for _, v := range holdingValues {
		if v > 0 {
			total += v
		}
	}
	if total <= 0 {
		return 1
	}
	var h float64
	for _, v := range holdingValues {
		if v > 0 {
			h += (v / total) * (v / total)
		}
	}
	return h
}
