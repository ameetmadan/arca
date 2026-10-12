package pipeline

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const wait = 3 * time.Second

// recorder is a Handler that records every event it sees.
type recorder struct {
	mu     sync.Mutex
	events []Event
	seen   chan Event
}

func newRecorder() *recorder { return &recorder{seen: make(chan Event, 1024)} }

func (r *recorder) handle(_ context.Context, ev Event) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	r.seen <- ev
	return nil
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func start(t *testing.T, p *Pipeline) (cancel func(), done <-chan struct{}) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() { p.Run(ctx); close(d) }()
	t.Cleanup(func() {
		c()
		select {
		case <-d:
		case <-time.After(wait):
			t.Error("Run did not return after cancel")
		}
	})
	return c, d
}

func expect(t *testing.T, r *recorder) Event {
	t.Helper()
	select {
	case ev := <-r.seen:
		return ev
	case <-time.After(wait):
		t.Fatal("timed out waiting for an event to be handled")
		return Event{}
	}
}

func TestPipeline_DeliversEventExactlyAsSubmitted(t *testing.T) {
	r := newRecorder()
	p := New(r.handle, Options{Debounce: time.Millisecond})
	start(t, p)

	// A path with a double quote and a newline used to break the hand-built
	// JSON the watcher POSTed to itself.
	path := "/tmp/we\"ird\nname.txt"
	p.Submit(Event{Op: Created, Path: path})

	got := expect(t, r)
	if got.Path != path || got.Op != Created {
		t.Fatalf("got %+v, want path %q op Created", got, path)
	}
}

func TestPipeline_RapidDuplicatesCollapseToOne(t *testing.T) {
	r := newRecorder()
	p := New(r.handle, Options{Debounce: 50 * time.Millisecond})
	start(t, p)

	for i := 0; i < 20; i++ {
		p.Submit(Event{Op: Modified, Path: "/a.txt"})
	}
	expect(t, r)
	time.Sleep(200 * time.Millisecond) // well past the debounce window
	if n := len(r.all()); n != 1 {
		t.Fatalf("handled %d events, want 1", n)
	}
}

// The debounce is trailing-edge: a burst that keeps going is handled once,
// after it goes quiet, so the final write is never lost.
func TestPipeline_DebounceWaitsForQuietPeriod(t *testing.T) {
	r := newRecorder()
	p := New(r.handle, Options{Debounce: 80 * time.Millisecond})
	start(t, p)

	begin := time.Now()
	for i := 0; i < 5; i++ {
		p.Submit(Event{Op: Modified, Path: "/a.txt"})
		time.Sleep(40 * time.Millisecond)
	}
	expect(t, r)
	if elapsed := time.Since(begin); elapsed < 5*40*time.Millisecond {
		t.Fatalf("handled after %v, before the burst ended", elapsed)
	}
}

func TestMergeOp(t *testing.T) {
	cases := []struct{ old, new, want Op }{
		{Created, Modified, Created},
		{Modified, Modified, Modified},
		{Modified, Removed, Removed},
		{Created, Removed, Removed},
		{Removed, Created, Modified},
		{Removed, Modified, Modified},
		{Modified, Created, Created},
	}
	for _, c := range cases {
		if got := mergeOp(c.old, c.new); got != c.want {
			t.Errorf("mergeOp(%v, %v) = %v, want %v", c.old, c.new, got, c.want)
		}
	}
}

func TestPipeline_DistinctPathsAreAllHandled(t *testing.T) {
	r := newRecorder()
	p := New(r.handle, Options{Debounce: time.Millisecond, Workers: 2})
	start(t, p)

	paths := map[string]bool{"/a": true, "/b": true, "/c": true}
	for path := range paths {
		p.Submit(Event{Op: Created, Path: path})
	}
	for range 3 {
		delete(paths, expect(t, r).Path)
	}
	if len(paths) != 0 {
		t.Fatalf("never handled: %v", paths)
	}
}

// A slow worker must never block Submit (and so never block the watcher).
// With a full queue the oldest pending events are dropped, the newest kept.
func TestPipeline_SlowWorkerDoesNotBlockSubmit_DropsOldest(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var handled sync.Map
	var count atomic.Int32
	handler := func(_ context.Context, ev Event) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		handled.Store(ev.Path, true)
		count.Add(1)
		return nil
	}
	p := New(handler, Options{Workers: 1, QueueSize: 3, Debounce: 0})
	start(t, p)

	p.Submit(Event{Op: Created, Path: "/first"})
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("worker never started")
	}

	const total = 200
	submitted := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			p.Submit(Event{Op: Created, Path: fmt.Sprintf("/p%d", i)})
		}
		close(submitted)
	}()
	select {
	case <-submitted:
	case <-time.After(wait):
		t.Fatal("Submit blocked behind a slow worker")
	}

	// Let the scheduler settle, then check that overflow was dropped.
	deadline := time.Now().Add(wait)
	for p.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.Dropped() == 0 {
		t.Fatal("expected events to be dropped when the queue is full")
	}

	close(release)
	deadline = time.Now().Add(wait)
	newest := fmt.Sprintf("/p%d", total-1)
	for time.Now().Before(deadline) {
		if _, ok := handled.Load(newest); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := handled.Load(newest); !ok {
		t.Fatalf("newest event %s was not handled; drop-oldest should keep it", newest)
	}
	var n int
	handled.Range(func(_, _ any) bool { n++; return true })
	// Only a handful of events fit in the buffers; the rest were dropped.
	if n > total/4 {
		t.Fatalf("handled %d of %d events; the queue should be bounded", n, total)
	}
	if got := int(count.Load()) + int(p.Dropped()); got < total {
		t.Fatalf("handled+dropped = %d, want >= %d", got, total)
	}
}

// Two events for the same path must never run concurrently on two workers.
func TestPipeline_SamePathIsNotProcessedConcurrently(t *testing.T) {
	var active, maxActive atomic.Int32
	var calls atomic.Int32
	handler := func(_ context.Context, _ Event) error {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		active.Add(-1)
		calls.Add(1)
		return nil
	}
	p := New(handler, Options{Workers: 4, Debounce: 0})
	start(t, p)

	p.Submit(Event{Op: Modified, Path: "/a"})
	time.Sleep(20 * time.Millisecond) // first is now in flight
	p.Submit(Event{Op: Modified, Path: "/a"})

	deadline := time.Now().Add(wait)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (the change during processing must not be lost)", calls.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("same path processed by %d workers at once", maxActive.Load())
	}
}

func TestPipeline_HandlerErrorDoesNotStopWorkers(t *testing.T) {
	var errs atomic.Int32
	r := newRecorder()
	handler := func(ctx context.Context, ev Event) error {
		if ev.Path == "/bad" {
			return context.DeadlineExceeded
		}
		return r.handle(ctx, ev)
	}
	p := New(handler, Options{Workers: 1, Debounce: time.Millisecond, OnError: func(Event, error) { errs.Add(1) }})
	start(t, p)

	p.Submit(Event{Op: Created, Path: "/bad"})
	p.Submit(Event{Op: Created, Path: "/good"})
	if got := expect(t, r); got.Path != "/good" {
		t.Fatalf("got %+v", got)
	}
	if errs.Load() != 1 {
		t.Fatalf("OnError called %d times, want 1", errs.Load())
	}
}

func TestPipeline_HandlerPanicIsContained(t *testing.T) {
	r := newRecorder()
	handler := func(ctx context.Context, ev Event) error {
		if ev.Path == "/boom" {
			panic("boom")
		}
		return r.handle(ctx, ev)
	}
	p := New(handler, Options{Workers: 1, Debounce: time.Millisecond, OnError: func(Event, error) {}})
	start(t, p)

	p.Submit(Event{Op: Created, Path: "/boom"})
	p.Submit(Event{Op: Created, Path: "/ok"})
	if got := expect(t, r); got.Path != "/ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestPipeline_IgnoresInvalidEvents(t *testing.T) {
	r := newRecorder()
	p := New(r.handle, Options{Debounce: time.Millisecond})
	start(t, p)

	p.Submit(Event{Op: Created, Path: ""})
	p.Submit(Event{Op: 0, Path: "/a"})
	p.Submit(Event{Op: Created, Path: "/ok"})
	if got := expect(t, r); got.Path != "/ok" {
		t.Fatalf("got %+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.all()); n != 1 {
		t.Fatalf("handled %d events, want 1", n)
	}
}

// Cancelling must abandon queued work, cancel the context of in-flight
// handlers, and wait for them before Run returns.
func TestPipeline_CancelStopsInFlightAndDiscardsQueued(t *testing.T) {
	inHandler := make(chan struct{})
	handlerCtxDone := make(chan struct{})
	var calls atomic.Int32
	handler := func(ctx context.Context, _ Event) error {
		calls.Add(1)
		close(inHandler)
		<-ctx.Done()
		close(handlerCtxDone)
		return ctx.Err()
	}
	p := New(handler, Options{Workers: 1, Debounce: 0})
	cancel, done := start(t, p)

	p.Submit(Event{Op: Created, Path: "/a"})
	p.Submit(Event{Op: Created, Path: "/b"})
	select {
	case <-inHandler:
	case <-time.After(wait):
		t.Fatal("handler never ran")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Run did not return after cancel")
	}
	select {
	case <-handlerCtxDone:
	default:
		t.Fatal("Run returned before the in-flight handler finished")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times; queued work should be discarded on cancel", calls.Load())
	}
}

func TestPipeline_NoGoroutineLeakAfterCancel(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		r := newRecorder()
		p := New(r.handle, Options{Workers: 4, Debounce: time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()
		for j := 0; j < 50; j++ {
			p.Submit(Event{Op: Created, Path: fmt.Sprintf("/f%d", j)})
		}
		expect(t, r)
		cancel()
		select {
		case <-done:
		case <-time.After(wait):
			t.Fatal("Run did not return")
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

func TestPipeline_RunTwiceIsRejected(t *testing.T) {
	p := New(newRecorder().handle, Options{})
	start(t, p)
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { p.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("second Run should return immediately")
	}
}
