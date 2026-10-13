package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/vector"
)

// Stage names the step of indexing that failed.
type Stage string

// Indexing stages, in the order they run.
const (
	StageParse  Stage = "parse"
	StageEmbed  Stage = "embed"
	StageUpsert Stage = "upsert"
	// StageCleanup removes chunks left over from an earlier, longer version
	// of the file.
	StageCleanup Stage = "cleanup"
)

// IndexError reports which stage of indexing failed. Its message matches what
// POST /api/v1/index has always returned.
type IndexError struct {
	Stage Stage
	// Chunk is the index of the failing chunk (unused for StageParse).
	Chunk int
	Err   error
}

func (e *IndexError) Error() string {
	switch e.Stage {
	case StageParse:
		return e.Err.Error()
	default:
		return fmt.Sprintf("%s chunk %d: %v", e.Stage, e.Chunk, e.Err)
	}
}

func (e *IndexError) Unwrap() error { return e.Err }

// Indexer is the parse -> embed -> VectorStore.Upsert stage. The same code
// serves the watcher pipeline and the POST /api/v1/index endpoint.
type Indexer struct {
	Parser parser.DocumentParser
	Embed  embed.EmbeddingProvider
	Store  vector.VectorStore
}

// ChunkID is the vector-store id of chunk i of the file at path.
func ChunkID(path string, i int) string { return chunkPrefix(path) + strconv.Itoa(i) }

// chunkPrefix is what every ChunkID of the file at path starts with.
func chunkPrefix(path string) string { return path + "#" }

// deleteChunks removes chunks from..end of the file at path, if the store can.
// A store without vector.ChunkDeleter keeps its stale chunks, as before.
func (ix *Indexer) deleteChunks(ctx context.Context, path string, from int) error {
	d, ok := ix.Store.(vector.ChunkDeleter)
	if !ok {
		return nil
	}
	return d.DeleteChunks(ctx, chunkPrefix(path), from)
}

// IndexFile parses the file at path, embeds each chunk and upserts it. It
// returns the number of chunks indexed. Failures are *IndexError values.
// Chunks upserted before a failure stay in the store. Once every chunk is
// in, chunks beyond the last one from an earlier, longer version of the file
// are removed.
func (ix *Indexer) IndexFile(ctx context.Context, path string) (int, error) {
	chunks, err := ix.Parser.Parse(path)
	if err != nil {
		return 0, &IndexError{Stage: StageParse, Err: err}
	}

	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		vec, err := ix.Embed.Embed(ctx, chunk.Content)
		if err != nil {
			return i, &IndexError{Stage: StageEmbed, Chunk: i, Err: err}
		}
		if err := ix.Store.Upsert(ctx, ChunkID(path, i), vec, chunk.Metadata); err != nil {
			return i, &IndexError{Stage: StageUpsert, Chunk: i, Err: err}
		}
	}
	if err := ix.deleteChunks(ctx, path, len(chunks)); err != nil {
		return len(chunks), &IndexError{Stage: StageCleanup, Chunk: len(chunks), Err: err}
	}
	return len(chunks), nil
}

// Handle is the pipeline Handler that keeps the index up to date.
//
// Created and Modified events index the file; directories are skipped.
// Removed events (which the watcher also sends for the old name of a renamed
// file; the new name arrives as Created) delete the file's chunks, so deleted
// and moved files stop appearing in answers. That needs a store implementing
// vector.ChunkDeleter; chunks of removed directories' contents are not found
// this way.
func (ix *Indexer) Handle(ctx context.Context, ev Event) error {
	if ev.Op == Removed {
		if _, err := os.Stat(ev.Path); err == nil {
			return nil // recreated since; its Created event reindexes it
		}
		return ix.deleteChunks(ctx, ev.Path, 0)
	}
	if info, err := os.Stat(ev.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // gone again before we got to it
		}
		return err
	} else if info.IsDir() {
		return nil
	}
	_, err := ix.IndexFile(ctx, ev.Path)
	return err
}
