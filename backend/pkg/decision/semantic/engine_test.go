package semantic

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
)

const testRoot = "/r"

// folder builds a stored FolderIndex from hand-made example vectors.
func folder(t testing.TB, rel string, fileCount int, examples ...[]float32) FolderIndex {
	t.Helper()
	f := FolderIndex{Root: testRoot, Rel: rel, FileCount: fileCount}
	var vs [][]float32
	for i, e := range examples {
		v, ok := normalize(e)
		if !ok {
			t.Fatalf("bad example vector %v", e)
		}
		vs = append(vs, v)
		f.Examples = append(f.Examples, Example{Name: string(rune('a' + i)), Vector: v})
	}
	if c, ok := centroidOf(vs); ok {
		f.Centroid = c
	}
	return f
}

func newTestEngine(t testing.TB, tn Tunables, folders ...FolderIndex) *Engine {
	t.Helper()
	s := NewMemoryStore()
	for _, f := range folders {
		if err := s.Put(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	e, err := NewEngine(s, tn)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func facts(v ...float32) decision.Facts {
	return decision.Facts{Path: testRoot + "/Inbox/file.pdf", Vector: v}
}

// axes are three orthogonal "topics" so cosine similarities are easy to reason about.
var (
	topicA = []float32{1, 0, 0}
	topicB = []float32{0, 1, 0}
	topicC = []float32{0, 0, 1}
)

func TestEngineImplementsDecisionEngine(t *testing.T) {
	var _ decision.Engine = (*Engine)(nil)
}

func TestDecideClearWinner(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(),
		folder(t, "Taxes/2025", 40, topicA, []float32{0.9, 0.1, 0}),
		folder(t, "Photos", 12, topicB),
		folder(t, "Recipes", 7, topicC),
	)

	d, err := e.Decide(context.Background(), facts(0.95, 0.1, 0))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Action != decision.Move {
		t.Errorf("Action = %q, want move", d.Action)
	}
	if d.Target != "Taxes/2025" {
		t.Errorf("Target = %q, want Taxes/2025", d.Target)
	}
	if d.Source != "semantic" {
		t.Errorf("Source = %q, want semantic", d.Source)
	}
	if want := "closest to the 40 files in Taxes/2025"; d.Reason != want {
		t.Errorf("Reason = %q, want %q", d.Reason, want)
	}
	if d.Confidence < 0.8 || d.Confidence > 0.95 {
		t.Errorf("Confidence = %v, want in [0.8, 0.95]", d.Confidence)
	}
}

func TestDecideConfidenceMatchesFormula(t *testing.T) {
	tn := DefaultTunables()
	e := newTestEngine(t, tn,
		folder(t, "A", 3, topicA),
		folder(t, "B", 3, topicB),
	)
	v := []float32{0.8, 0.6, 0} // unit length: cosine to A is 0.8, to B is 0.6
	d, err := e.Decide(context.Background(), facts(v...))
	if err != nil {
		t.Fatal(err)
	}
	if want := tn.Confidence(0.8, 0.6); math.Abs(d.Confidence-want) > 1e-6 {
		t.Errorf("Confidence = %v, want %v", d.Confidence, want)
	}
}

func TestDecideAmbiguousHasLowConfidence(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(),
		folder(t, "Invoices", 20, topicA),
		folder(t, "Receipts", 20, topicB),
	)
	// Equally close to both folders: the engine answers, but admits it is unsure.
	d, err := e.Decide(context.Background(), facts(1, 1, 0))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Confidence >= 0.3 {
		t.Errorf("Confidence = %v for a tie, want < 0.3", d.Confidence)
	}
	// A typical chain threshold must therefore reject it.
	if d.Confidence >= 0.7 {
		t.Errorf("Confidence %v would clear a 0.7 chain threshold", d.Confidence)
	}
}

func TestDecideTieBreakIsDeterministic(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(),
		folder(t, "B", 1, topicB),
		folder(t, "A", 1, topicA),
	)
	for i := 0; i < 20; i++ {
		d, err := e.Decide(context.Background(), facts(1, 1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if d.Target != "A" {
			t.Fatalf("Target = %q, want A (first by path on a tie)", d.Target)
		}
	}
}

func TestDecideNoDecision(t *testing.T) {
	tests := []struct {
		name    string
		folders []FolderIndex
		facts   decision.Facts
	}{
		{"no folders at all", nil, facts(1, 0, 0)},
		{"one folder only", []FolderIndex{folder(t, "A", 5, topicA)}, facts(1, 0, 0)},
		{
			"second folder is empty",
			[]FolderIndex{folder(t, "A", 5, topicA), folder(t, "Empty", 0)},
			facts(1, 0, 0),
		},
		{
			"second folder has files but no usable vectors",
			[]FolderIndex{folder(t, "A", 5, topicA), folder(t, "Blank", 3)},
			facts(1, 0, 0),
		},
		{"nil vector", []FolderIndex{folder(t, "A", 5, topicA), folder(t, "B", 5, topicB)}, facts()},
		{"zero vector", []FolderIndex{folder(t, "A", 5, topicA), folder(t, "B", 5, topicB)}, facts(0, 0, 0)},
		{"NaN vector", []FolderIndex{folder(t, "A", 5, topicA), folder(t, "B", 5, topicB)}, facts(float32(math.NaN()), 0, 0)},
		{
			"best similarity below the floor",
			[]FolderIndex{folder(t, "A", 5, topicA), folder(t, "B", 5, topicB)},
			facts(0.1, 0.1, 1),
		},
		{
			"vector of a different dimension",
			[]FolderIndex{folder(t, "A", 5, topicA), folder(t, "B", 5, topicB)},
			facts(1, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEngine(t, DefaultTunables(), tt.folders...)
			_, err := e.Decide(context.Background(), tt.facts)
			if !errors.Is(err, decision.ErrNoDecision) {
				t.Errorf("err = %v, want ErrNoDecision", err)
			}
		})
	}
}

func TestDecideOnlyConsidersFoldersOfTheFilesRoot(t *testing.T) {
	other := folder(t, "A", 5, topicA)
	other.Root = "/elsewhere"
	// Only one folder lives under /r, so there is nothing to compare.
	e := newTestEngine(t, DefaultTunables(), folder(t, "B", 5, topicB), other)
	_, err := e.Decide(context.Background(), facts(0, 1, 0))
	if !errors.Is(err, decision.ErrNoDecision) {
		t.Errorf("err = %v, want ErrNoDecision", err)
	}

	// A file outside every indexed root is not placed either.
	e = newTestEngine(t, DefaultTunables(), folder(t, "A", 5, topicA), folder(t, "B", 5, topicB))
	f := facts(1, 0, 0)
	f.Path = "/somewhere/else/file.pdf"
	if _, err := e.Decide(context.Background(), f); !errors.Is(err, decision.ErrNoDecision) {
		t.Errorf("file outside root: err = %v, want ErrNoDecision", err)
	}
}

func TestDecideWithoutPathConsidersEveryRoot(t *testing.T) {
	a := folder(t, "A", 5, topicA)
	b := folder(t, "B", 5, topicB)
	b.Root = "/other"
	e := newTestEngine(t, DefaultTunables(), a, b)
	d, err := e.Decide(context.Background(), decision.Facts{Vector: topicA})
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "A" {
		t.Errorf("Target = %q, want A", d.Target)
	}
}

func TestDecideSkipsFoldersWithMismatchedDimension(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(),
		folder(t, "A", 5, topicA),
		folder(t, "B", 5, topicB),
		folder(t, "Old", 5, []float32{1, 0}), // indexed with a different model
	)
	d, err := e.Decide(context.Background(), facts(1, 0.1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "A" {
		t.Errorf("Target = %q, want A", d.Target)
	}
}

func TestDecideKNNStrategy(t *testing.T) {
	// Mixed holds two unrelated kinds of file, so its centroid sits between
	// them and is a poor match for either. Tidy is a single tight cluster
	// that is a moderate match.
	mixed := folder(t, "Mixed", 10, []float32{1, 0}, []float32{0, 1})
	tidy := folder(t, "Tidy", 10, []float32{0.8, 0.6})
	file := facts(1, 0)

	centroid := DefaultTunables()
	e := newTestEngine(t, centroid, mixed, tidy)
	d, err := e.Decide(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "Tidy" {
		t.Errorf("centroid strategy: Target = %q, want Tidy", d.Target)
	}

	knn := DefaultTunables()
	knn.Strategy = KNN
	knn.K = 1
	e = newTestEngine(t, knn, mixed, tidy)
	d, err = e.Decide(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "Mixed" {
		t.Errorf("knn strategy: Target = %q, want Mixed", d.Target)
	}
}

func TestDecideKNNAveragesTopK(t *testing.T) {
	tn := DefaultTunables()
	tn.Strategy = KNN
	tn.K = 2
	// Examples with cosines 1, 0.6 and 0 to the file; K=2 averages the best two.
	f := folder(t, "A", 3, []float32{1, 0}, []float32{0.6, 0.8}, []float32{0, 1})
	g := folder(t, "B", 1, []float32{0.5, 0.866025})
	e := newTestEngine(t, tn, f, g)
	d, err := e.Decide(context.Background(), facts(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// s1 = (1+0.6)/2 = 0.8, s2 = 0.5 (one example, K larger than the set).
	if want := tn.Confidence(0.8, 0.5); math.Abs(d.Confidence-want) > 1e-4 {
		t.Errorf("Confidence = %v, want %v", d.Confidence, want)
	}
}

func TestDecideKNNFallsBackToCentroidWithoutExamples(t *testing.T) {
	tn := DefaultTunables()
	tn.Strategy = KNN
	a := folder(t, "A", 5, topicA)
	a.Examples = nil
	b := folder(t, "B", 5, topicB)
	b.Examples = nil
	e := newTestEngine(t, tn, a, b)
	d, err := e.Decide(context.Background(), facts(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "A" {
		t.Errorf("Target = %q, want A", d.Target)
	}
}

func TestDecideHonoursContext(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(), folder(t, "A", 1, topicA), folder(t, "B", 1, topicB))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Decide(ctx, facts(1, 0, 0)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

type failingStore struct{ FolderStore }

func (failingStore) List(context.Context) ([]FolderIndex, error) {
	return nil, errors.New("disk on fire")
}

func TestDecideStoreFailureIsNotNoDecision(t *testing.T) {
	e, err := NewEngine(failingStore{NewMemoryStore()}, DefaultTunables())
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Decide(context.Background(), facts(1, 0, 0))
	if err == nil || errors.Is(err, decision.ErrNoDecision) {
		t.Errorf("err = %v, want a real failure that is not ErrNoDecision", err)
	}
	if err != nil && !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("err = %v, want it to wrap the store error", err)
	}
}

func TestDecideDoesNotModifyFacts(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(), folder(t, "A", 1, topicA), folder(t, "B", 1, topicB))
	v := []float32{3, 4, 0}
	if _, err := e.Decide(context.Background(), facts(v...)); err != nil {
		t.Fatal(err)
	}
	if v[0] != 3 || v[1] != 4 {
		t.Errorf("Decide modified Facts.Vector: %v", v)
	}
}

func TestNewEngineValidates(t *testing.T) {
	if _, err := NewEngine(nil, DefaultTunables()); err == nil {
		t.Error("NewEngine(nil store) = nil error")
	}
	if _, err := NewEngine(NewMemoryStore(), Tunables{}); err == nil {
		t.Error("NewEngine(zero tunables) = nil error")
	}
}

func TestDecideConcurrently(t *testing.T) {
	e := newTestEngine(t, DefaultTunables(), folder(t, "A", 1, topicA), folder(t, "B", 1, topicB))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Decide(context.Background(), facts(1, 0.2, 0)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
