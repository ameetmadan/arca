package watcher

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A notification that is still waiting on a slow indexer must be abandoned
// when the context is cancelled, and Run must not return before it has.
func TestRun_CancelsInFlightNotifications(t *testing.T) {
	received := make(chan struct{}, 1)
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) //nolint:errcheck // the server only notices a closed connection once the body is read
		received <- struct{}{}
		<-r.Context().Done()
		close(handlerDone)
	}))
	defer srv.Close()

	w, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := w.AddFolder(dir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() { w.Run(ctx); close(runDone) }()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("indexer was never notified")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel; notification goroutine is stuck")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("in-flight notification was not cancelled")
	}
}
