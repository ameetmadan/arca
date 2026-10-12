package semantic

import (
	"math"
	"testing"
)

func TestDefaultTunablesAreValid(t *testing.T) {
	if err := DefaultTunables().Validate(); err != nil {
		t.Fatalf("DefaultTunables().Validate() = %v", err)
	}
}

func TestTunablesValidate(t *testing.T) {
	mutate := func(f func(*Tunables)) Tunables {
		tn := DefaultTunables()
		f(&tn)
		return tn
	}
	tests := map[string]Tunables{
		"zero sample size":       mutate(func(t *Tunables) { t.SampleSize = 0 }),
		"negative sample size":   mutate(func(t *Tunables) { t.SampleSize = -1 }),
		"floor at one":           mutate(func(t *Tunables) { t.SimilarityFloor = 1 }),
		"negative floor":         mutate(func(t *Tunables) { t.SimilarityFloor = -0.1 }),
		"NaN floor":              mutate(func(t *Tunables) { t.SimilarityFloor = math.NaN() }),
		"zero margin for full":   mutate(func(t *Tunables) { t.MarginForFull = 0 }),
		"margin weight above 1":  mutate(func(t *Tunables) { t.MarginWeight = 1.1 }),
		"max confidence zero":    mutate(func(t *Tunables) { t.MaxConfidence = 0 }),
		"max confidence above 1": mutate(func(t *Tunables) { t.MaxConfidence = 1.5 }),
		"unknown strategy":       mutate(func(t *Tunables) { t.Strategy = Strategy("magic") }),
		"zero k":                 mutate(func(t *Tunables) { t.K = 0 }),
	}
	for name, tn := range tests {
		t.Run(name, func(t *testing.T) {
			if err := tn.Validate(); err == nil {
				t.Errorf("Validate() = nil, want an error for %+v", tn)
			}
		})
	}
}

func TestConfidence(t *testing.T) {
	tn := DefaultTunables() // floor 0.30, full margin 0.25, weight 0.7, max 0.95
	tests := []struct {
		name   string
		s1, s2 float64
		want   float64
	}{
		// m = 1 (relative margin 0.667 >= 0.25); q = (0.9-0.3)/0.7
		{"clear winner", 0.9, 0.3, 0.95 * (0.7 + 0.3*(0.6/0.7))},
		// relative margin 0.025 -> m = 0.1; q = 0.5/0.7
		{"near tie", 0.8, 0.78, 0.95 * (0.7*0.1 + 0.3*(0.5/0.7))},
		{"exact tie at the floor", 0.3, 0.3, 0},
		{"perfect match, no runner-up", 1, 0, 0.95},
		// runner-up above the winner clamps the margin to 0
		{"inverted scores", 0.5, 0.6, 0.95 * 0.3 * (0.2 / 0.7)},
		{"zero best", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tn.Confidence(tt.s1, tt.s2)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Confidence(%v, %v) = %v, want %v", tt.s1, tt.s2, got, tt.want)
			}
		})
	}
}

func TestConfidenceStaysInRange(t *testing.T) {
	tn := DefaultTunables()
	for s1 := -0.5; s1 <= 1.5; s1 += 0.1 {
		for s2 := -0.5; s2 <= 1.5; s2 += 0.1 {
			c := tn.Confidence(s1, s2)
			if math.IsNaN(c) || c < 0 || c > tn.MaxConfidence {
				t.Fatalf("Confidence(%v, %v) = %v, outside [0, %v]", s1, s2, c, tn.MaxConfidence)
			}
		}
	}
}

// TestDocumentedConfidenceExamples pins the examples in the package doc so the
// documentation cannot drift away from the formula.
func TestDocumentedConfidenceExamples(t *testing.T) {
	tn := DefaultTunables()
	for _, tt := range []struct{ s1, s2, want float64 }{
		{0.90, 0.30, 0.91},
		{0.80, 0.60, 0.87},
		{0.80, 0.78, 0.27},
		{0.50, 0.10, 0.75},
	} {
		if got := tn.Confidence(tt.s1, tt.s2); math.Abs(got-tt.want) > 0.005 {
			t.Errorf("Confidence(%v, %v) = %.3f, documented as %.2f", tt.s1, tt.s2, got, tt.want)
		}
	}
	// A perfect tie of two perfect matches is the most a tie can score.
	if got, max := tn.Confidence(1, 1), (1-tn.MarginWeight)*tn.MaxConfidence; math.Abs(got-max) > 1e-9 {
		t.Errorf("Confidence(1, 1) = %v, want %v", got, max)
	}
}

func TestConfidenceGrowsWithMargin(t *testing.T) {
	tn := DefaultTunables()
	prev := -1.0
	for s2 := 0.8; s2 >= 0.0; s2 -= 0.1 {
		c := tn.Confidence(0.8, s2)
		if c < prev {
			t.Fatalf("Confidence dropped from %v to %v as the runner-up got worse", prev, c)
		}
		prev = c
	}
}
