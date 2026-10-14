package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/vector"
)

type fakeParser struct {
	mu     sync.Mutex
	chunks []parser.Chunk
	err    error
	paths  []string
}

func (f *fakeParser) Parse(path string) ([]parser.Chunk, error) {
	f.mu.Lock()
	f.paths = append(f.paths, path)
	f.mu.Unlock()
	return f.chunks, f.err
}

type fakeEmbed struct {
	err   error
	texts []string
}

func (f *fakeEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	f.texts = append(f.texts, text)
	if f.err != nil {
		return nil, f.err
	}
	return []float32{float32(len(text)), 1}, nil
}

type fakeStore struct {
	mu   sync.Mutex
	ids  []string
	meta []map[string]interface{}
	err  error
}

func (f *fakeStore) Upsert(_ context.Context, id string, _ []float32, m map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.ids = append(f.ids, id)
	f.meta = append(f.meta, m)
	return nil
}
func (f *fakeStore) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return nil, nil
}
func (f *fakeStore) Delete(context.Context, string) error { return nil }

func touch(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoChunks() []parser.Chunk {
	return []parser.Chunk{
		{Content: "one", Metadata: map[string]interface{}{"n": 1}},
		{Content: "two", Metadata: map[string]interface{}{"n": 2}},
	}
}

func TestIndexer_IndexesEveryChunk(t *testing.T) {
	store := &fakeStore{}
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: store}

	n, err := ix.IndexFile(context.Background(), "/docs/a.txt")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	want := []string{"/docs/a.txt#0", "/docs/a.txt#1"}
	if len(store.ids) != 2 || store.ids[0] != want[0] || store.ids[1] != want[1] {
		t.Fatalf("ids = %v, want %v", store.ids, want)
	}
	if store.meta[1]["n"] != 2 {
		t.Fatalf("metadata not forwarded: %v", store.meta)
	}
}

func TestIndexer_StageErrors(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		ix      *Indexer
		stage   Stage
		message string
	}{
		{"parse", &Indexer{Parser: &fakeParser{err: boom}, Embed: &fakeEmbed{}, Store: &fakeStore{}}, StageParse, "boom"},
		{"embed", &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{err: boom}, Store: &fakeStore{}}, StageEmbed, "embed chunk 0: boom"},
		{"upsert", &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: &fakeStore{err: boom}}, StageUpsert, "upsert chunk 0: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.ix.IndexFile(context.Background(), "/a")
			var ie *IndexError
			if !errors.As(err, &ie) || ie.Stage != c.stage {
				t.Fatalf("err = %v, want IndexError at stage %s", err, c.stage)
			}
			if !errors.Is(err, boom) {
				t.Error("underlying error should be unwrappable")
			}
			if err.Error() != c.message {
				t.Errorf("message = %q, want %q", err.Error(), c.message)
			}
		})
	}
}

func TestIndexer_StopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeStore{}
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: store}
	if _, err := ix.IndexFile(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(store.ids) != 0 {
		t.Fatalf("indexed %v after cancel", store.ids)
	}
}

func TestIndexer_HandleSkipsRemovedMissingAndDirectories(t *testing.T) {
	fp := &fakeParser{chunks: twoChunks()}
	ix := &Indexer{Parser: fp, Embed: &fakeEmbed{}, Store: &fakeStore{}}
	ctx := context.Background()

	if err := ix.Handle(ctx, Event{Op: Removed, Path: touch(t, "a.txt")}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Handle(ctx, Event{Op: Created, Path: filepath.Join(t.TempDir(), "gone.txt")}); err != nil {
		t.Fatalf("a file that vanished is not an error: %v", err)
	}
	if err := ix.Handle(ctx, Event{Op: Created, Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if len(fp.paths) != 0 {
		t.Fatalf("parser called for %v; none of these should be indexed", fp.paths)
	}
}

// A path containing a double quote and a newline must reach the parser and
// the vector store unchanged (the old watcher built JSON by hand and broke).
func TestIndexer_HandleIndexesPathWithQuoteAndNewline(t *testing.T) {
	path := touch(t, "we\"ird\nname.txt")
	fp := &fakeParser{chunks: twoChunks()[:1]}
	store := &fakeStore{}
	ix := &Indexer{Parser: fp, Embed: &fakeEmbed{}, Store: store}

	if err := ix.Handle(context.Background(), Event{Op: Created, Path: path}); err != nil {
		t.Fatal(err)
	}
	if len(fp.paths) != 1 || fp.paths[0] != path {
		t.Fatalf("parser saw %q, want %q", fp.paths, path)
	}
	if len(store.ids) != 1 || store.ids[0] != path+"#0" {
		t.Fatalf("store ids = %q, want %q", store.ids, path+"#0")
	}
}

// chunkStore is an in-memory store that implements vector.ChunkDeleter with
// the same matching rules as the real ones.
type chunkStore struct {
	fakeStore
	have map[string]bool
}

func newChunkStore() *chunkStore { return &chunkStore{have: map[string]bool{}} }

func (c *chunkStore) Upsert(ctx context.Context, id string, v []float32, m map[string]interface{}) error {
	if err := c.fakeStore.Upsert(ctx, id, v, m); err != nil {
		return err
	}
	c.have[id] = true
	return nil
}

func (c *chunkStore) DeleteChunks(_ context.Context, prefix string, from int) error {
	for id := range c.have {
		rest, ok := strings.CutPrefix(id, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil && n >= from && strconv.Itoa(n) == rest {
			delete(c.have, id)
		}
	}
	return nil
}

func (c *chunkStore) ids() []string {
	out := make([]string, 0, len(c.have))
	for id := range c.have {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestIndexer_RemovedDeletesAllChunksOfTheFile(t *testing.T) {
	store := newChunkStore()
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: store}
	ctx := context.Background()
	dir := t.TempDir()
	a, other, lookalike := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt"), filepath.Join(dir, "a.txt#1")
	for _, p := range []string{a, other, lookalike} {
		if _, err := ix.IndexFile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	// a.txt is not on disk, as after a delete.
	if err := ix.Handle(ctx, Event{Op: Removed, Path: a}); err != nil {
		t.Fatal(err)
	}
	want := []string{lookalike + "#0", lookalike + "#1", other + "#0", other + "#1"}
	if got := store.ids(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids after delete = %v, want %v", got, want)
	}
}

// fsnotify reports a rename as a Remove-like event for the old name and a
// Create for the new one: together they move the chunks.
func TestIndexer_RenameMovesChunks(t *testing.T) {
	store := newChunkStore()
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: store}
	ctx := context.Background()
	dir := t.TempDir()
	oldPath, newPath := filepath.Join(dir, "old.txt"), filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.IndexFile(ctx, oldPath); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []Event{{Op: Removed, Path: oldPath}, {Op: Created, Path: newPath}} {
		if err := ix.Handle(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{newPath + "#0", newPath + "#1"}
	if got := store.ids(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids after rename = %v, want %v", got, want)
	}
}

func TestIndexer_RemovedKeepsChunksOfAFileThatCameBack(t *testing.T) {
	store := newChunkStore()
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{}, Store: store}
	path := touch(t, "a.txt")
	if _, err := ix.IndexFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := ix.Handle(context.Background(), Event{Op: Removed, Path: path}); err != nil {
		t.Fatal(err)
	}
	if len(store.ids()) != 2 {
		t.Fatalf("chunks of a file that exists again were deleted: %v", store.ids())
	}
}

func TestIndexer_ShrunkFileDropsItsTailChunks(t *testing.T) {
	store := newChunkStore()
	fp := &fakeParser{chunks: twoChunks()}
	ix := &Indexer{Parser: fp, Embed: &fakeEmbed{}, Store: store}
	ctx := context.Background()
	if _, err := ix.IndexFile(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	fp.chunks = twoChunks()[:1]
	if _, err := ix.IndexFile(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got, want := store.ids(), []string{"/a.txt#0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestIndexer_FailedIndexKeepsOldChunks(t *testing.T) {
	store := newChunkStore()
	fp := &fakeParser{chunks: twoChunks()}
	em := &fakeEmbed{}
	ix := &Indexer{Parser: fp, Embed: em, Store: store}
	ctx := context.Background()
	if _, err := ix.IndexFile(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	em.err = errors.New("boom")
	if _, err := ix.IndexFile(ctx, "/a.txt"); err == nil {
		t.Fatal("want an error")
	}
	if len(store.ids()) != 2 {
		t.Fatalf("a failed re-index must not lose the old chunks: %v", store.ids())
	}
}

type failingDeleter struct{ chunkStore }

func (*failingDeleter) DeleteChunks(context.Context, string, int) error { return errors.New("boom") }

func TestIndexer_CleanupFailureIsReported(t *testing.T) {
	ix := &Indexer{Parser: &fakeParser{chunks: twoChunks()}, Embed: &fakeEmbed{},
		Store: &failingDeleter{chunkStore{have: map[string]bool{}}}}
	_, err := ix.IndexFile(context.Background(), "/a.txt")
	var ie *IndexError
	if !errors.As(err, &ie) || ie.Stage != StageCleanup {
		t.Fatalf("err = %v, want IndexError at stage %s", err, StageCleanup)
	}
}
