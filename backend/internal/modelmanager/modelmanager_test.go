package modelmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// If the consumer of the progress channel goes away (client disconnected or
// server shutting down), the download goroutine must still exit once its
// context is cancelled instead of blocking on a send nobody will receive.
func TestDownload_AbandonedConsumerDoesNotLeakGoroutine(t *testing.T) {
	chunk := make([]byte, 4096)
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000000")
		for {
			select {
			case <-r.Context().Done():
				return
			case <-stop:
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()
	defer close(stop)

	orig := AvailableModels
	AvailableModels = []Model{{ID: "test-model", URL: srv.URL + "/test.gguf"}}
	defer func() { AvailableModels = orig }()

	m := New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.Download(ctx, "test-model"); err != nil {
		t.Fatal(err)
	}

	// Never read from the channel: let it fill up, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Wait for the download goroutine itself (not the http plumbing around
	// it) to disappear from the stack dump.
	running := func() bool {
		buf := make([]byte, 1<<16)
		return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "modelmanager.(*Manager).Download")
	}
	deadline := time.Now().Add(3 * time.Second)
	for running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if running() {
		t.Fatal("download goroutine still running after its context was cancelled")
	}
}
