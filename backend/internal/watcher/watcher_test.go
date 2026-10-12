package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/vector"
)

const wait = 3 * time.Second

type sinkLog struct {
	mu     sync.Mutex
	events []pipeline.Event
	signal chan struct{}
}

func newSinkLog() *sinkLog { return &sinkLog{signal: make(chan struct{}, 64)} }

func (s *sinkLog) put(ev pipeline.Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// waitFor blocks until an event satisfying match has arrived.
func (s *sinkLog) waitFor(t *testing.T, match func(pipeline.Event) bool) pipeline.Event {
	t.Helper()
	deadline := time.After(wait)
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if match(ev) {
				s.mu.Unlock()
				return ev
			}
		}
		s.mu.Unlock()
		select {
		case <-s.signal:
		case <-deadline:
			t.Fatal("timed out waiting for a matching watcher event")
		}
	}
}

func runWatcher(t *testing.T, w *Watcher) (cancel func(), done <-chan struct{}) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() { w.Run(ctx); close(d) }()
	t.Cleanup(c)
	return c, d
}

func TestNew_RequiresSink(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) should fail")
	}
}

func TestRun_PublishesCreateModifyRemove(t *testing.T) {
	log := newSinkLog()
	w, err := New(log.put)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := w.AddFolder(dir); err != nil {
		t.Fatal(err)
	}
	runWatcher(t, w)

	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, func(e pipeline.Event) bool { return e.Path == path && e.Op == pipeline.Created })

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("more"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	log.waitFor(t, func(e pipeline.Event) bool { return e.Path == path && e.Op == pipeline.Modified })

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, func(e pipeline.Event) bool { return e.Path == path && e.Op == pipeline.Removed })
}

func TestOpFor(t *testing.T) {
	cases := []struct {
		in     fsnotify.Op
		want   pipeline.Op
		wantOK bool
	}{
		{fsnotify.Create, pipeline.Created, true},
		{fsnotify.Write, pipeline.Modified, true},
		{fsnotify.Remove, pipeline.Removed, true},
		{fsnotify.Create | fsnotify.Write, pipeline.Created, true},
		{fsnotify.Write | fsnotify.Remove, pipeline.Removed, true},
		{fsnotify.Chmod, 0, false},
		{fsnotify.Rename, 0, false},
	}
	for _, c := range cases {
		got, ok := opFor(fsnotify.Event{Name: "x", Op: c.in})
		if got != c.want || ok != c.wantOK {
			t.Errorf("opFor(%v) = %v,%v want %v,%v", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

type recParser struct {
	mu    sync.Mutex
	paths []string
}

func (p *recParser) Parse(path string) ([]parser.Chunk, error) {
	p.mu.Lock()
	p.paths = append(p.paths, path)
	p.mu.Unlock()
	return []parser.Chunk{{Content: "c"}}, nil
}

type okEmbed struct{}

func (okEmbed) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }

type recStore struct{ ids chan string }

func (s *recStore) Upsert(_ context.Context, id string, _ []float32, _ map[string]interface{}) error {
	s.ids <- id
	return nil
}
func (s *recStore) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return nil, nil
}
func (s *recStore) Delete(context.Context, string) error { return nil }

// End to end: a file whose name holds a double quote and a newline travels
// watcher -> pipeline -> indexer and is stored under its exact path. The old
// watcher built {"path":"..."} by hand and broke on exactly this.
func TestWatcherToIndex_PathWithQuoteAndNewline(t *testing.T) {
	store := &recStore{ids: make(chan string, 8)}
	par := &recParser{}
	ix := &pipeline.Indexer{Parser: par, Embed: okEmbed{}, Store: store}
	p := pipeline.New(ix.Handle, pipeline.Options{Debounce: 20 * time.Millisecond})

	w, err := New(p.Submit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := w.AddFolder(dir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, run := range []func(context.Context){p.Run, w.Run} {
		wg.Add(1)
		go func() { defer wg.Done(); run(ctx) }()
	}
	defer func() { cancel(); wg.Wait() }()

	path := filepath.Join(dir, "we\"ird\nname.txt")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Skipf("filesystem rejects this file name: %v", err)
	}

	select {
	case id := <-store.ids:
		if id != path+"#0" {
			t.Fatalf("stored id %q, want %q", id, path+"#0")
		}
	case <-time.After(wait):
		t.Fatal("file was never indexed")
	}
	par.mu.Lock()
	defer par.mu.Unlock()
	for _, got := range par.paths {
		if got != path {
			t.Errorf("parser saw %q, want %q", got, path)
		}
	}
}

// Run must return promptly on cancel and leave no goroutines behind.
func TestRun_StopsOnCancelWithoutLeaks(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		w, err := New(func(pipeline.Event) {})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.AddFolder(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		cancel, done := runWatcher(t, w)
		cancel()
		select {
		case <-done:
		case <-time.After(wait):
			t.Fatal("Run did not return after cancel")
		}
	}

	deadline := time.Now().Add(wait)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines: %d before, %d after\n%s", before, after, buf)
	}
}

// The watcher hands events straight to the sink; it must not spawn a goroutine
// per event the way the old HTTP notifier did.
func TestRun_DoesNotSpawnGoroutinePerEvent(t *testing.T) {
	log := newSinkLog()
	w, err := New(log.put)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := w.AddFolder(dir); err != nil {
		t.Fatal(err)
	}
	runWatcher(t, w)
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log.waitFor(t, func(pipeline.Event) bool { return true })
	if n := runtime.NumGoroutine(); n > base+3 {
		t.Fatalf("goroutines grew from %d to %d under load", base, n)
	}
}
