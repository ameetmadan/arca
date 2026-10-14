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

// ChunkDeleter is implemented by stores that can delete a file's chunks
// without knowing how many there are. Chunk ids have the form prefix + n,
// where n is a decimal chunk number (for a file, prefix is "<path>#").
//
// It is a separate interface so VectorStore implementations that cannot do
// this keep compiling; callers fall back to leaving stale chunks in place.
type ChunkDeleter interface {
	// DeleteChunks deletes every id equal to prefix followed by a decimal
	// number >= from. Ids that merely start with prefix, such as
	// "<path>#1#0" when prefix is "<path>#", are not matched. Deleting
	// nothing is not an error.
	DeleteChunks(ctx context.Context, prefix string, from int) error
}
