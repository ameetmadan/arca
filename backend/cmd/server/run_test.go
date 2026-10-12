package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventLog records the order in which shutdown steps happen.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) index(e string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, v := range l.events {
		if v == e {
			return i
		}
	}
	return -1
}

func newListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}

// startRun launches run in the background and returns a function that
// triggers the shutdown signal plus the channel carrying run's result.
func startRun(t *testing.T, cfg runConfig) (trigger context.CancelFunc, result <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := make(chan error, 1)
	go func() { out <- run(ctx, cfg) }()
	return cancel, out
}

func waitResult(t *testing.T, result <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(within):
		t.Fatalf("run did not return within %s", within)
		return nil
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestRun_InFlightRequestCompletesOnShutdown(t *testing.T) {
	ln := newListener(t)
	entered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-time.After(300 * time.Millisecond):
			io.WriteString(w, "finished") //nolint:errcheck
		case <-r.Context().Done():
			t.Error("request context cancelled while still within the grace period")
		}
	})

	trigger, result := startRun(t, runConfig{
		Server:          &http.Server{Handler: mux},
		Listener:        ln,
		ShutdownTimeout: 2 * time.Second,
	})

	type resp struct {
		body string
		err  error
	}
	got := make(chan resp, 1)
	go func() {
		r, err := newClient().Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- resp{err: err}
			return
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		got <- resp{body: string(b), err: err}
	}()

	waitFor(t, entered, "handler to start")
	trigger()

	if err := waitResult(t, result, 3*time.Second); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.body != "finished" {
			t.Fatalf("in-flight request not completed: body=%q err=%v", r.body, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("client never got a response")
	}

	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("server still accepting connections after run returned")
	}
}

func TestRun_StuckStreamIsCancelledAfterTimeout(t *testing.T) {
	ln := newListener(t)
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hello\n\n") //nolint:errcheck
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done() // SSE handlers must exit on context cancellation
	})

	trigger, result := startRun(t, runConfig{
		Server:          &http.Server{Handler: mux},
		Listener:        ln,
		ShutdownTimeout: 150 * time.Millisecond,
	})

	resp, err := newClient().Get("http://" + ln.Addr().String() + "/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: hello") {
		t.Fatalf("first event: %q err=%v", line, err)
	}
	waitFor(t, started, "stream to start")

	begin := time.Now()
	trigger()
	if err := waitResult(t, result, 3*time.Second); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("shutdown took %s, expected roughly the 150ms timeout", d)
	}
	waitFor(t, handlerDone, "handler to observe cancellation")

	// The stream must be terminated from the client's point of view too.
	if _, err := io.ReadAll(resp.Body); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Logf("stream ended with: %v", err)
	}
}

func TestRun_StopsWorkersBeforeClosingResources(t *testing.T) {
	ln := newListener(t)
	log := &eventLog{}
	workerStarted := make(chan struct{})

	cfg := runConfig{
		Server:          &http.Server{Handler: http.NewServeMux()},
		Listener:        ln,
		ShutdownTimeout: time.Second,
		Workers: []func(context.Context){
			func(ctx context.Context) {
				close(workerStarted)
				<-ctx.Done()
				time.Sleep(50 * time.Millisecond) // slow cleanup must still be waited for
				log.add("worker-stopped")
			},
		},
		Closers: []func(){
			func() { log.add("closed-first-registered") },
			func() { log.add("closed-last-registered") },
		},
	}
	trigger, result := startRun(t, cfg)
	waitFor(t, workerStarted, "worker to start")
	trigger()
	if err := waitResult(t, result, 3*time.Second); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	w := log.index("worker-stopped")
	a := log.index("closed-first-registered")
	b := log.index("closed-last-registered")
	if w < 0 || a < 0 || b < 0 {
		t.Fatalf("missing events: %v", log.events)
	}
	if w > b || w > a {
		t.Fatalf("resources closed before worker finished: %v", log.events)
	}
	if b > a {
		t.Fatalf("closers should run in reverse registration order: %v", log.events)
	}
}

func TestRun_ListenerFailureStillRunsCleanup(t *testing.T) {
	ln := newListener(t)
	ln.Close() // Serve on a closed listener fails immediately

	log := &eventLog{}
	workerStopped := make(chan struct{})
	cfg := runConfig{
		Server:          &http.Server{Handler: http.NewServeMux()},
		Listener:        ln,
		ShutdownTimeout: time.Second,
		Workers: []func(context.Context){
			func(ctx context.Context) { <-ctx.Done(); log.add("worker-stopped"); close(workerStopped) },
		},
		Closers: []func(){func() { log.add("closed") }},
	}
	_, result := startRun(t, cfg)

	if err := waitResult(t, result, 3*time.Second); err == nil {
		t.Fatal("expected the listener error to be returned")
	}
	if log.index("worker-stopped") < 0 || log.index("closed") < 0 {
		t.Fatalf("cleanup skipped after listener failure: %v", log.events)
	}
	if log.index("worker-stopped") > log.index("closed") {
		t.Fatalf("wrong cleanup order: %v", log.events)
	}
}

func TestRun_AddrInUseReturnsErrorAndCleansUp(t *testing.T) {
	busy := newListener(t)
	defer busy.Close()

	closed := false
	_, result := startRun(t, runConfig{
		Server:  &http.Server{Addr: busy.Addr().String(), Handler: http.NewServeMux()},
		Closers: []func(){func() { closed = true }},
	})
	if err := waitResult(t, result, 3*time.Second); err == nil {
		t.Fatal("expected an error when the address is already in use")
	}
	if !closed {
		t.Fatal("closers were not run")
	}
}

func TestRun_NoGoroutineLeaks(t *testing.T) {
	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(20 * time.Millisecond)
		}
		return n
	}
	before := settle()

	ln := newListener(t)
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: x\n\n") //nolint:errcheck
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	client := newClient()

	trigger, result := startRun(t, runConfig{
		Server:          &http.Server{Handler: mux},
		Listener:        ln,
		ShutdownTimeout: 100 * time.Millisecond,
		Workers:         []func(context.Context){func(ctx context.Context) { <-ctx.Done() }},
	})

	resp, err := client.Get("http://" + ln.Addr().String() + "/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	waitFor(t, started, "stream to start")
	trigger()
	if err := waitResult(t, result, 3*time.Second); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	client.CloseIdleConnections()

	deadline := time.Now().Add(3 * time.Second)
	var after int
	for {
		after = runtime.NumGoroutine()
		if after <= before || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if after > before {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutine leak: before=%d after=%d\n%s", before, after, buf)
	}
}

type closableThing struct{ closed *bool }

func (c closableThing) Close() { *c.closed = true }

func TestClosersOf(t *testing.T) {
	var closed bool
	got := closersOf(struct{}{}, nil, closableThing{&closed})
	if len(got) != 1 {
		t.Fatalf("want 1 closer, got %d", len(got))
	}
	got[0]()
	if !closed {
		t.Fatal("closer did not invoke Close")
	}
}
