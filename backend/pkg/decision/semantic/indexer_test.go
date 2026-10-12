package semantic

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

var epoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

type fakeFile struct {
	name string
	text string
	size int64
	mod  time.Time
}

// fakeSource is an in-memory Source keyed by relative folder path.
type fakeSource struct {
	mu      sync.Mutex
	folders map[string][]fakeFile
}

func newFakeSource() *fakeSource { return &fakeSource{folders: map[string][]fakeFile{}} }

func (s *fakeSource) set(rel string, files ...fakeFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders[rel] = files
}

func (s *fakeSource) remove(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.folders, rel)
}

func (s *fakeSource) Folders(ctx context.Context, root string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for rel := range s.folders {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func (s *fakeSource) Files(ctx context.Context, root, rel string) ([]SourceFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, ok := s.folders[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	var out []SourceFile
	for _, f := range files {
		mod := f.mod
		if mod.IsZero() {
			mod = epoch
		}
		out = append(out, SourceFile{Name: f.name, Size: f.size, ModTime: mod})
	}
	return out, nil
}

func (s *fakeSource) Text(ctx context.Context, root, rel, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.folders[rel] {
		if f.name == name {
			return f.text, nil
		}
	}
	return "", fs.ErrNotExist
}

// fakeEmbedder maps text to hand-made vectors.
type fakeEmbedder struct {
	vecs  map[string][]float32
	calls atomic.Int64
	// block, if set, makes Embed announce itself on started and then wait
	// for ctx to be cancelled.
	block   bool
	started chan struct{}
}

func (e *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.calls.Add(1)
	if e.block {
		select {
		case e.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v, ok := e.vecs[text]
	if !ok {
		return nil, fmt.Errorf("fake embedder: no vector for %q", text)
	}
	return v, nil
}

func newIndexer(t testing.TB, tn Tunables, src Source, emb *fakeEmbedder) (*Indexer, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	ix, err := NewIndexer(store, emb, src, tn)
	if err != nil {
		t.Fatal(err)
	}
	return ix, store
}

// topicVecs gives every text "a<N>" the topic-A direction, "b<N>" topic B, and so on.
func topicEmbedder() *fakeEmbedder {
	e := &fakeEmbedder{vecs: map[string][]float32{}}
	for i := 0; i < 100; i++ {
		e.vecs[fmt.Sprintf("a%d", i)] = []float32{1, float32(i) / 1000, 0}
		e.vecs[fmt.Sprintf("b%d", i)] = []float32{float32(i) / 1000, 1, 0}
		e.vecs[fmt.Sprintf("c%d", i)] = []float32{0, float32(i) / 1000, 1}
	}
	return e
}

func files(prefix string, n int) []fakeFile {
	var out []fakeFile
	for i := 0; i < n; i++ {
		out = append(out, fakeFile{name: fmt.Sprintf("%s%02d.txt", prefix, i), text: fmt.Sprintf("%s%d", prefix, i), size: 100})
	}
	return out
}

func TestIndexRootBuildsCentroidsAndExamples(t *testing.T) {
	src := newFakeSource()
	src.set("Taxes/2025", files("a", 5)...)
	src.set("Photos", files("b", 3)...)
	ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())

	var last Progress
	err := ix.IndexRoot(context.Background(), "/r", func(p Progress) { last = p })
	if err != nil {
		t.Fatalf("IndexRoot: %v", err)
	}

	got, ok, _ := store.Get(context.Background(), "/r", "Taxes/2025")
	if !ok {
		t.Fatal("Taxes/2025 not stored")
	}
	if got.FileCount != 5 || len(got.Examples) != 5 {
		t.Errorf("FileCount %d, examples %d; want 5 and 5", got.FileCount, len(got.Examples))
	}
	if got.Fingerprint == "" {
		t.Error("empty fingerprint")
	}
	if n := dot(got.Centroid, got.Centroid); n < 0.999 || n > 1.001 {
		t.Errorf("centroid is not unit length: |c|^2 = %v", n)
	}
	if got.Centroid[0] < 0.99 {
		t.Errorf("centroid %v should point along topic A", got.Centroid)
	}

	if last.FoldersTotal != 2 || last.FoldersDone != 2 || last.FilesEmbedded != 8 || !last.Finished {
		t.Errorf("final progress = %+v", last)
	}
}

func TestIndexRootThenDecide(t *testing.T) {
	src := newFakeSource()
	src.set("Taxes", files("a", 4)...)
	src.set("Photos", files("b", 4)...)
	emb := topicEmbedder()
	ix, store := newIndexer(t, DefaultTunables(), src, emb)
	if err := ix.IndexRoot(context.Background(), "/r", nil); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(store, DefaultTunables())
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.Decide(context.Background(), decision.Facts{Path: "/r/new.pdf", Vector: emb.vecs["a7"]})
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "/r/Taxes" || d.Confidence < 0.8 {
		t.Errorf("decision = %+v, want a confident move to /r/Taxes", d)
	}
}

func TestIndexRootSamplesAtMostSampleSize(t *testing.T) {
	src := newFakeSource()
	src.set("Big", files("a", 20)...)
	tn := DefaultTunables()
	tn.SampleSize = 4
	emb := topicEmbedder()
	ix, store := newIndexer(t, tn, src, emb)
	if err := ix.IndexRoot(context.Background(), "/r", nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(context.Background(), "/r", "Big")
	if got.FileCount != 20 || len(got.Examples) != 4 {
		t.Errorf("FileCount %d, examples %d; want 20 and 4", got.FileCount, len(got.Examples))
	}
	if n := emb.calls.Load(); n != 4 {
		t.Errorf("embedder called %d times, want 4", n)
	}
}

func exampleNames(f FolderIndex) []string {
	var out []string
	for _, e := range f.Examples {
		out = append(out, e.Name)
	}
	return out
}

func TestSamplingIsDeterministicGivenSeed(t *testing.T) {
	run := func(seed uint64, reverse bool) []string {
		fl := files("a", 30)
		if reverse { // input order must not matter
			for i, j := 0, len(fl)-1; i < j; i, j = i+1, j-1 {
				fl[i], fl[j] = fl[j], fl[i]
			}
		}
		src := newFakeSource()
		src.set("Big", fl...)
		tn := DefaultTunables()
		tn.SampleSize = 5
		tn.Seed = seed
		ix, store := newIndexer(t, tn, src, topicEmbedder())
		if err := ix.IndexRoot(context.Background(), "/r", nil); err != nil {
			t.Fatal(err)
		}
		got, _, _ := store.Get(context.Background(), "/r", "Big")
		return exampleNames(got)
	}

	a1, a2, aRev := run(42, false), run(42, false), run(42, true)
	if !reflect.DeepEqual(a1, a2) {
		t.Errorf("same seed, different samples: %v vs %v", a1, a2)
	}
	if !reflect.DeepEqual(a1, aRev) {
		t.Errorf("sample depends on input order: %v vs %v", a1, aRev)
	}
	differs := false
	for seed := uint64(1); seed <= 10; seed++ {
		if !reflect.DeepEqual(a1, run(seed, false)) {
			differs = true
			break
		}
	}
	if !differs {
		t.Error("ten other seeds all produced the same sample; the seed has no effect")
	}
}

func TestIndexRootSkipsUnchangedFoldersAndReindexesChanged(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 3)...)
	src.set("B", files("b", 3)...)
	emb := topicEmbedder()
	ix, _ := newIndexer(t, DefaultTunables(), src, emb)
	ctx := context.Background()

	if err := ix.IndexRoot(ctx, "/r", nil); err != nil {
		t.Fatal(err)
	}
	if n := emb.calls.Load(); n != 6 {
		t.Fatalf("first run embedded %d files, want 6", n)
	}

	var p Progress
	if err := ix.IndexRoot(ctx, "/r", func(x Progress) { p = x }); err != nil {
		t.Fatal(err)
	}
	if n := emb.calls.Load(); n != 6 {
		t.Errorf("unchanged re-run embedded again: %d calls", n)
	}
	if p.FoldersSkipped != 2 {
		t.Errorf("FoldersSkipped = %d, want 2", p.FoldersSkipped)
	}

	// A new file appears in A only.
	src.set("A", append(files("a", 3), fakeFile{name: "new.txt", text: "a9", size: 5})...)
	if err := ix.IndexRoot(ctx, "/r", nil); err != nil {
		t.Fatal(err)
	}
	if n := emb.calls.Load(); n != 10 {
		t.Errorf("after one change got %d total calls, want 10 (only A re-embedded)", n)
	}
}

func TestIndexRootPrunesRemovedAndEmptiedFolders(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 2)...)
	src.set("B", files("b", 2)...)
	src.set("C", files("c", 2)...)
	ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())
	ctx := context.Background()
	if err := ix.IndexRoot(ctx, "/r", nil); err != nil {
		t.Fatal(err)
	}

	src.remove("B") // folder deleted
	src.set("C")    // folder still there, now empty
	if err := ix.IndexRoot(ctx, "/r", nil); err != nil {
		t.Fatal(err)
	}
	all, _ := store.List(ctx)
	if len(all) != 1 || all[0].Rel != "A" {
		t.Errorf("stored folders = %+v, want only A", all)
	}
}

func TestIndexRootLeavesOtherRootsAlone(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 2)...)
	ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())
	ctx := context.Background()
	_ = store.Put(ctx, FolderIndex{Root: "/other", Rel: "Z", FileCount: 1, Centroid: []float32{1, 0, 0}})
	if err := ix.IndexRoot(ctx, "/r", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(ctx, "/other", "Z"); !ok {
		t.Error("indexing /r removed a folder of /other")
	}
}

func TestIndexFolderEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("empty folder has no entry", func(t *testing.T) {
		src := newFakeSource()
		src.set("Empty")
		ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())
		changed, err := ix.IndexFolder(ctx, "/r", "Empty")
		if err != nil || changed {
			t.Fatalf("IndexFolder = %v, %v; want false, nil", changed, err)
		}
		if all, _ := store.List(ctx); len(all) != 0 {
			t.Errorf("stored %d folders for an empty folder", len(all))
		}
	})

	t.Run("files that cannot be embedded are skipped", func(t *testing.T) {
		src := newFakeSource()
		src.set("Mixed",
			fakeFile{name: "ok.txt", text: "a1"},
			fakeFile{name: "blank.txt", text: ""},
			fakeFile{name: "unknown.txt", text: "no vector for this"},
		)
		ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())
		if _, err := ix.IndexFolder(ctx, "/r", "Mixed"); err != nil {
			t.Fatal(err)
		}
		got, _, _ := store.Get(ctx, "/r", "Mixed")
		if got.FileCount != 3 || len(got.Examples) != 1 {
			t.Errorf("FileCount %d, examples %d; want 3 and 1", got.FileCount, len(got.Examples))
		}
	})

	t.Run("inconsistent dimension is skipped", func(t *testing.T) {
		src := newFakeSource()
		src.set("D", fakeFile{name: "a.txt", text: "x"}, fakeFile{name: "b.txt", text: "y"})
		emb := &fakeEmbedder{vecs: map[string][]float32{"x": {1, 0, 0}, "y": {1, 0}}}
		ix, store := newIndexer(t, DefaultTunables(), src, emb)
		if _, err := ix.IndexFolder(ctx, "/r", "D"); err != nil {
			t.Fatal(err)
		}
		got, _, _ := store.Get(ctx, "/r", "D")
		if len(got.Examples) != 1 {
			t.Errorf("examples = %d, want 1", len(got.Examples))
		}
	})

	t.Run("embedder failure is not cached as an empty folder", func(t *testing.T) {
		src := newFakeSource()
		src.set("F", fakeFile{name: "a.txt", text: "no vector for this"})
		emb := topicEmbedder()
		ix, store := newIndexer(t, DefaultTunables(), src, emb)
		if _, err := ix.IndexFolder(ctx, "/r", "F"); err == nil {
			t.Error("IndexFolder = nil error although every embedding failed")
		}
		if _, ok, _ := store.Get(ctx, "/r", "F"); ok {
			t.Error("a failed folder was stored, so it would never be retried")
		}
	})
}

func TestIndexRejectsBadPaths(t *testing.T) {
	ix, _ := newIndexer(t, DefaultTunables(), newFakeSource(), topicEmbedder())
	ctx := context.Background()
	for _, root := range []string{"", "relative/root", "/r/../x", "/r/"} {
		if err := ix.IndexRoot(ctx, root, nil); err == nil {
			t.Errorf("IndexRoot(%q) = nil error", root)
		}
	}
	for _, rel := range []string{"", ".", "..", "../x", "/abs", "a/../../x", ".hidden", "a/.hidden/b"} {
		if _, err := ix.IndexFolder(ctx, "/r", rel); err == nil {
			t.Errorf("IndexFolder(%q) = nil error", rel)
		}
	}
}

func TestNewIndexerValidates(t *testing.T) {
	store, emb, src := NewMemoryStore(), topicEmbedder(), newFakeSource()
	if _, err := NewIndexer(nil, emb, src, DefaultTunables()); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := NewIndexer(store, nil, src, DefaultTunables()); err == nil {
		t.Error("nil embedder accepted")
	}
	if _, err := NewIndexer(store, emb, nil, DefaultTunables()); err == nil {
		t.Error("nil source accepted")
	}
	if _, err := NewIndexer(store, emb, src, Tunables{}); err == nil {
		t.Error("zero tunables accepted")
	}
}

func TestBackgroundIndexingIsCancellableAndDoesNotBlockDecide(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 3)...)
	src.set("B", files("b", 3)...)
	emb := &fakeEmbedder{block: true, started: make(chan struct{}, 1)}
	ix, store := newIndexer(t, DefaultTunables(), src, emb)
	eng, err := NewEngine(store, DefaultTunables())
	if err != nil {
		t.Fatal(err)
	}

	job := ix.Start(context.Background(), "/r")
	select {
	case <-emb.started:
	case <-time.After(5 * time.Second):
		t.Fatal("indexing never started")
	}

	// The indexer is stuck inside the embedder; Decide must still answer at once.
	answered := make(chan error, 1)
	go func() {
		_, err := eng.Decide(context.Background(), decision.Facts{Vector: []float32{1, 0, 0}})
		answered <- err
	}()
	select {
	case err := <-answered:
		if !errors.Is(err, decision.ErrNoDecision) {
			t.Errorf("Decide during indexing = %v, want ErrNoDecision", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Decide blocked while indexing was running")
	}

	select {
	case <-job.Done():
		t.Fatal("job finished while the embedder was blocked")
	default:
	}
	if p := job.Progress(); p.Finished {
		t.Errorf("progress says finished: %+v", p)
	}

	job.Cancel()
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not stop after Cancel")
	}
	if err := job.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait = %v, want context.Canceled", err)
	}
	if all, _ := store.List(context.Background()); len(all) != 0 {
		t.Errorf("a cancelled run left %d half-built folders", len(all))
	}
}

func TestBackgroundIndexingStopsWhenParentContextIsCancelled(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 1)...)
	emb := &fakeEmbedder{block: true, started: make(chan struct{}, 1)}
	ix, _ := newIndexer(t, DefaultTunables(), src, emb)

	ctx, cancel := context.WithCancel(context.Background())
	job := ix.Start(ctx, "/r")
	<-emb.started
	cancel()
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not stop when its context was cancelled")
	}
}

func TestBackgroundIndexingCompletes(t *testing.T) {
	src := newFakeSource()
	src.set("A", files("a", 3)...)
	src.set("B", files("b", 3)...)
	ix, store := newIndexer(t, DefaultTunables(), src, topicEmbedder())

	job := ix.Start(context.Background(), "/r")
	if err := job.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	p := job.Progress()
	if !p.Finished || p.FoldersDone != 2 || p.FoldersTotal != 2 || p.Root != "/r" {
		t.Errorf("progress = %+v", p)
	}
	if all, _ := store.List(context.Background()); len(all) != 2 {
		t.Errorf("stored %d folders, want 2", len(all))
	}
}

func TestProgressIsMonotonic(t *testing.T) {
	src := newFakeSource()
	for _, rel := range []string{"A", "B", "C", "D"} {
		src.set(rel, files("a", 2)...)
	}
	ix, _ := newIndexer(t, DefaultTunables(), src, topicEmbedder())
	var seen []Progress
	if err := ix.IndexRoot(context.Background(), "/r", func(p Progress) { seen = append(seen, p) }); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 4 {
		t.Fatalf("only %d progress reports for 4 folders", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].FoldersDone < seen[i-1].FoldersDone || seen[i].FilesEmbedded < seen[i-1].FilesEmbedded {
			t.Errorf("progress went backwards: %+v then %+v", seen[i-1], seen[i])
		}
	}
	if !seen[len(seen)-1].Finished {
		t.Error("last report is not marked Finished")
	}
}
