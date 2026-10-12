package vector

import "context"

type SearchResult struct {
	ID       string
	Score    float32
	Metadata map[string]interface{}
}

type VectorStore interface {
	Upsert(ctx context.Context, id string, vec []float32, metadata map[string]interface{}) error
	Search(ctx context.Context, query []float32, topK int) ([]SearchResult, error)
	Delete(ctx context.Context, id string) error
}

// Pinger is optionally implemented by stores backed by a database so the
// server's /ready endpoint can verify the connection is usable.
type Pinger interface {
	Ping(ctx context.Context) error
}
