package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeIndex struct {
	mu        sync.Mutex
	indexed   []string
	forgotten []string
	fail      map[string]error
}

func (f *fakeIndex) index(_ context.Context, path string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[filepath.Base(path)]; err != nil {
		return 0, err
	}
	f.indexed = append(f.indexed, filepath.Base(path))
	return 2, nil
}

func (f *fakeIndex) forget(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, filepath.Base(path))
	return nil
}

func (f *fakeIndex) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.indexed, f.forgotten = nil, nil
}

func txtOnly(path string) bool { return strings.HasSuffix(path, ".txt") }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newScanner(t *testing.T, f *fakeIndex) *Scanner {
	return &Scanner{
		Index:     f.index,
		Forget:    f.forget,
		Supported: txtOnly,
		StatePath: filepath.Join(t.TempDir(), "state.json"),
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestScanIndexesSupportedFilesAndSkipsHiddenAndPackages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	write(t, filepath.Join(root, "sub", "b.txt"), "b")
	write(t, filepath.Join(root, "c.jpg"), "c")
	write(t, filepath.Join(root, ".hidden.txt"), "h")
	write(t, filepath.Join(root, ".git", "d.txt"), "d")
	write(t, filepath.Join(root, "node_modules", "e.txt"), "e")
	write(t, filepath.Join(root, "Thing.app", "f.txt"), "f")
	write(t, filepath.Join(root, "empty.txt"), "")

	f := &fakeIndex{}
	st := newScanner(t, f).Scan(context.Background(), []string{root})

	if got, want := sorted(f.indexed), []string{"a.txt", "b.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("indexed %v, want %v", got, want)
	}
	if st.Found != 2 || st.Indexed != 2 || st.Chunks != 4 || st.Running || st.FinishedAt == nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestRescanSkipsUnchangedAndReindexesChanged(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	write(t, a, "a")
	write(t, b, "b")
	f := &fakeIndex{}
	s := newScanner(t, f)
	s.Scan(context.Background(), []string{root})

	f.reset()
	write(t, b, "b changed")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(b, later, later); err != nil {
		t.Fatal(err)
	}
	st := s.Scan(context.Background(), []string{root})
	if strings.Join(f.indexed, ",") != "b.txt" {
		t.Fatalf("indexed %v, want only b.txt", f.indexed)
	}
	if st.Unchanged != 1 || st.Indexed != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestScanForgetsDeletedFilesAndRemovedRoots(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	write(t, filepath.Join(r1, "keep.txt"), "k")
	write(t, filepath.Join(r1, "gone.txt"), "g")
	write(t, filepath.Join(r2, "other.txt"), "o")
	f := &fakeIndex{}
	s := newScanner(t, f)
	s.Scan(context.Background(), []string{r1, r2})

	if err := os.Remove(filepath.Join(r1, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	f.reset()
	st := s.Scan(context.Background(), []string{r1}) // r2 no longer watched
	if got := strings.Join(sorted(f.forgotten), ","); got != "gone.txt,other.txt" {
		t.Fatalf("forgot %q", got)
	}
	if st.Removed != 2 {
		t.Fatalf("status = %+v", st)
	}

	// Forgotten files are dropped from the state, so nothing is forgotten twice.
	f.reset()
	s.Scan(context.Background(), []string{r1})
	if len(f.forgotten) != 0 {
		t.Fatalf("forgot again: %v", f.forgotten)
	}
}

func TestFatalErrorStopsScanWithoutForgetting(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	write(t, filepath.Join(root, "b.txt"), "b")
	f := &fakeIndex{}
	s := newScanner(t, f)
	s.Scan(context.Background(), []string{root})

	// A later scan where the model is missing must not touch the state.
	write(t, filepath.Join(root, "c.txt"), "c")
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	errNoModel := errors.New("no model")
	f.fail = map[string]error{"c.txt": errNoModel}
	s.Fatal = func(err error) string {
		if errors.Is(err, errNoModel) {
			return "embedding model not available"
		}
		return ""
	}
	f.reset()
	st := s.Scan(context.Background(), []string{root})
	if st.Stopped != "embedding model not available" {
		t.Fatalf("status = %+v", st)
	}
	if len(f.forgotten) != 0 {
		t.Fatalf("forgot %v during a stopped scan", f.forgotten)
	}

	// Once the model is back, c.txt is indexed (it was not recorded).
	f.fail = nil
	f.reset()
	s.Scan(context.Background(), []string{root})
	if strings.Join(f.indexed, ",") != "c.txt" {
		t.Fatalf("indexed %v", f.indexed)
	}
}

func TestNonFatalFailureIsRecordedAndNotRetried(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "bad.txt"), "x")
	f := &fakeIndex{fail: map[string]error{"bad.txt": errors.New("parse")}}
	s := newScanner(t, f)
	if st := s.Scan(context.Background(), []string{root}); st.Failed != 1 {
		t.Fatalf("status = %+v", st)
	}
	f.fail = nil
	s.Scan(context.Background(), []string{root})
	if len(f.indexed) != 0 {
		t.Fatalf("retried unchanged failed file: %v", f.indexed)
	}
}

func TestMaxFilesLimitDoesNotForget(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		write(t, filepath.Join(root, n), n)
	}
	f := &fakeIndex{}
	s := newScanner(t, f)
	s.Scan(context.Background(), []string{root})

	s.MaxFiles = 1
	f.reset()
	s.Scan(context.Background(), []string{root})
	if len(f.forgotten) != 0 {
		t.Fatalf("forgot %v after a truncated walk", f.forgotten)
	}
}

func TestRunScansAtStartAndOnTrigger(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	f := &fakeIndex{}
	s := newScanner(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, []string{root}); close(done) }()

	waitFor(t, func() bool { st := s.Status(); return st.FinishedAt != nil && !st.Running })
	write(t, filepath.Join(root, "b.txt"), "b")
	s.Trigger()
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.indexed) == 2
	})
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
