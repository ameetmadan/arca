package sorter

import (
	"math"
	"testing"
)

func TestCosineSim_Identical(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{1, 0})
	if math.Abs(float64(got-1.0)) > 1e-6 {
		t.Errorf("got %v, want 1.0", got)
	}
}

func TestCosineSim_Orthogonal(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{0, 1})
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("got %v, want 0.0", got)
	}
}

func TestCosineSim_Opposite(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{-1, 0})
	if math.Abs(float64(got+1.0)) > 1e-6 {
		t.Errorf("got %v, want -1.0", got)
	}
}

func TestCosineSim_ZeroVector(t *testing.T) {
	got := cosineSim([]float32{0, 0}, []float32{1, 0})
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestCluster_TwoGroups(t *testing.T) {
	// 3 vectors near [1,0,0], 3 near [0,1,0] — should split cleanly into 2 clusters
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	vecs := [][]float32{a, a, a, b, b, b}

	clusters := cluster(vecs)

	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	for _, c := range clusters {
		if len(c) != 3 {
			t.Errorf("expected cluster size 3, got %d", len(c))
		}
	}
}

func TestCluster_TwoFiles(t *testing.T) {
	// Minimum valid input: 2 distinct vectors → 2 clusters of 1
	vecs := [][]float32{{1, 0}, {0, 1}}
	clusters := cluster(vecs)
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
}

func TestClampInt(t *testing.T) {
	tests := []struct{ v, lo, hi, want int }{
		{1, 2, 20, 2},
		{25, 2, 20, 20},
		{10, 2, 20, 10},
	}
	for _, tc := range tests {
		if got := clampInt(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}
