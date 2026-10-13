package modelmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
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
	AvailableModels = []Model{{ID: "test-model", URL: srv.URL + "/test.gguf", SHA256: strings.Repeat("0", 64)}}
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

// withTestModel serves body at /test.gguf and registers it as the only model.
func withTestModel(t *testing.T, sha string, body []byte) *Manager {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	orig := AvailableModels
	AvailableModels = []Model{{ID: "test-model", URL: srv.URL + "/test.gguf", SHA256: sha}}
	t.Cleanup(func() { AvailableModels = orig })
	return New(t.TempDir())
}

func drain(ch <-chan ProgressEvent) (last ProgressEvent) {
	for ev := range ch {
		last = ev
	}
	return last
}

func TestDownload_HashMatchSucceeds(t *testing.T) {
	body := []byte("model bytes")
	sum := sha256.Sum256(body)
	m := withTestModel(t, hex.EncodeToString(sum[:]), body)

	ch, err := m.Download(context.Background(), "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if last := drain(ch); last.Error != "" || !last.Done {
		t.Fatalf("expected success, got %+v", last)
	}
	if !m.IsPresent("test-model") {
		t.Fatal("model file missing after verified download")
	}
}

func TestDownload_HashMismatchFailsAndRemovesFile(t *testing.T) {
	m := withTestModel(t, strings.Repeat("0", 64), []byte("model bytes"))

	ch, err := m.Download(context.Background(), "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if last := drain(ch); !strings.Contains(last.Error, "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch error, got %+v", last)
	}
	if m.IsPresent("test-model") {
		t.Fatal("final file must not exist after a failed verification")
	}
	entries, _ := os.ReadDir(m.Dir)
	if len(entries) != 0 {
		t.Fatalf("partial files left behind: %v", entries)
	}
}

func TestDownload_EmptyHashIsAnError(t *testing.T) {
	m := withTestModel(t, "", []byte("model bytes"))
	if _, err := m.Download(context.Background(), "test-model"); err == nil {
		t.Fatal("a model without a SHA256 must not be downloadable")
	}
}

func TestAvailableModels_AreHashedAndPinned(t *testing.T) {
	for _, mod := range AvailableModels {
		if len(mod.SHA256) != 64 {
			t.Errorf("%s: want a 64-char SHA256, got %q", mod.ID, mod.SHA256)
		}
		if strings.Contains(mod.URL, "/resolve/main/") {
			t.Errorf("%s: URL must be pinned to a commit, not main", mod.ID)
		}
	}
}
