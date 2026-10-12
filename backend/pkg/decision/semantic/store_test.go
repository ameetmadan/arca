package semantic

import (
	"context"
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

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if _, ok, err := s.Get(ctx, "/r", "a"); err != nil || ok {
		t.Fatalf("Get on empty store = ok %v, err %v", ok, err)
	}

	a := FolderIndex{Root: "/r", Rel: "a", FileCount: 2, Fingerprint: "f1", Centroid: []float32{1, 0}}
	b := FolderIndex{Root: "/r", Rel: "b", FileCount: 1, Centroid: []float32{0, 1}}
	other := FolderIndex{Root: "/other", Rel: "a", FileCount: 1, Centroid: []float32{0, 1}}
	for _, f := range []FolderIndex{a, b, other} {
		if err := s.Put(ctx, f); err != nil {
			t.Fatalf("Put(%s %s): %v", f.Root, f.Rel, err)
		}
	}

	got, ok, err := s.Get(ctx, "/r", "a")
	if err != nil || !ok || got.Fingerprint != "f1" {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}

	all, err := s.List(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("List = %d folders, err %v; want 3", len(all), err)
	}
	// List is ordered so results are deterministic.
	if all[0].Root != "/other" || all[1].Rel != "a" || all[2].Rel != "b" {
		t.Errorf("List order = %v %v %v", all[0], all[1], all[2])
	}

	// Put replaces.
	a.Fingerprint = "f2"
	if err := s.Put(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, "/r", "a"); got.Fingerprint != "f2" {
		t.Errorf("Put did not replace: %+v", got)
	}

	if err := s.Delete(ctx, "/r", "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "/r", "a"); ok {
		t.Error("folder still present after Delete")
	}
	if err := s.Delete(ctx, "/r", "missing"); err != nil {
		t.Errorf("Delete of a missing folder = %v, want nil", err)
	}
}

func TestMemoryStoreHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewMemoryStore()
	if err := s.Put(ctx, FolderIndex{Root: "/r", Rel: "a"}); err == nil {
		t.Error("Put with cancelled context = nil error")
	}
	if _, err := s.List(ctx); err == nil {
		t.Error("List with cancelled context = nil error")
	}
}
