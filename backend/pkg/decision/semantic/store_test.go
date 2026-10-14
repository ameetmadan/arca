package semantic

import (
	"math"
	"testing"
)

func TestNormalize(t *testing.T) {
	v, ok := normalize([]float32{3, 4})
	if !ok || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("normalize([3 4]) = %v, %v; want [0.6 0.8], true", v, ok)
	}

	in := []float32{3, 4}
	normalize(in)
	if in[0] != 3 || in[1] != 4 {
		t.Errorf("normalize modified its input: %v", in)
	}

	for name, bad := range map[string][]float32{
		"empty": nil,
		"zeros": {0, 0},
		"NaN":   {1, float32(math.NaN())},
		"Inf":   {1, float32(math.Inf(1))},
	} {
		if _, ok := normalize(bad); ok {
			t.Errorf("normalize(%s) ok = true, want false", name)
		}
	}
}

func TestDot(t *testing.T) {
	if got := dot([]float32{1, 2, 3}, []float32{4, 5, 6}); got != 32 {
		t.Errorf("dot = %v, want 32", got)
	}
}

func TestCentroidOf(t *testing.T) {
	a, _ := normalize([]float32{1, 0})
	b, _ := normalize([]float32{0, 1})
	c, ok := centroidOf([][]float32{a, b})
	if !ok {
		t.Fatal("centroidOf ok = false")
	}
	want := float32(1 / math.Sqrt2)
	if math.Abs(float64(c[0]-want)) > 1e-6 || math.Abs(float64(c[1]-want)) > 1e-6 {
		t.Errorf("centroid = %v, want [%v %v]", c, want, want)
	}

	// Opposite vectors cancel out: there is no meaningful centroid.
	if _, ok := centroidOf([][]float32{{1, 0}, {-1, 0}}); ok {
		t.Error("centroidOf of opposite vectors ok = true, want false")
	}
	if _, ok := centroidOf(nil); ok {
		t.Error("centroidOf(nil) ok = true, want false")
	}
}
