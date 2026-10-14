package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/pkg/vector"
)

type fakeEmbed struct{}

func (fakeEmbed) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }

type fakeVectors struct{}

func (fakeVectors) Upsert(context.Context, string, []float32, map[string]interface{}) error {
	return nil
}
func (fakeVectors) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return []vector.SearchResult{{ID: "/docs/a.txt#0", Metadata: map[string]interface{}{
		"content": "an excerpt", "source": "/docs/a.txt",
	}}}, nil
}
func (fakeVectors) Delete(context.Context, string) error { return nil }

// endlessReader yields a token forever and ignores any context, like a
// generator that is slow to notice cancellation.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return copy(p, "tok"), nil
}

type fakeLLM struct{}

func (fakeLLM) Generate(context.Context, string) (io.Reader, error) { return endlessReader{}, nil }
func (fakeLLM) GenerateSync(context.Context, string) (string, error) {
	return "", nil
}

func TestHandleChat_StopsStreamingWhenRequestContextIsCancelled(t *testing.T) {
	s := &server{app: &config.App{
		EmbedProvider: fakeEmbed{},
		VectorStore:   fakeVectors{},
		LLMProvider:   fakeLLM{},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/chat?q=hi", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { s.handleChat(rec, req); close(done) }()

	time.Sleep(50 * time.Millisecond) // let some tokens stream
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleChat kept streaming after its request context was cancelled")
	}
}
