package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ameetmadan/arca/pkg/parser"
)

func writeFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFactsBuilder_BuildsAllFields(t *testing.T) {
	root := t.TempDir()
	path := writeFile(t, root, "taxes/return.pdf", "pdf bytes")
	mod := time.Date(2024, 3, 9, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbed{}
	b := &FactsBuilder{
		Parser: &fakeParser{chunks: []parser.Chunk{{Content: "Form 1040 summary"}, {Content: "later page"}}},
		Embed:  emb,
		Roots:  []string{root},
	}

	f, err := b.Build(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != path || f.RelPath != filepath.Join("taxes", "return.pdf") {
		t.Errorf("Path/RelPath = %q / %q", f.Path, f.RelPath)
	}
	if f.Ext != ".pdf" || f.MIME != "application/pdf" {
		t.Errorf("Ext/MIME = %q / %q", f.Ext, f.MIME)
	}
	if f.Size != int64(len("pdf bytes")) || !f.Modified.Equal(mod) {
		t.Errorf("Size/Modified = %d / %v", f.Size, f.Modified)
	}
	if f.Snippet != "Form 1040 summary" {
		t.Errorf("Snippet = %q, want first chunk only", f.Snippet)
	}
	if len(f.Vector) == 0 {
		t.Error("Vector missing")
	}
	if len(emb.texts) != 1 || emb.texts[0] != "Form 1040 summary" {
		t.Errorf("embedded %q, want the snippet", emb.texts)
	}
}

func TestFactsBuilder_EmbedFailureStillYieldsFacts(t *testing.T) {
	path := writeFile(t, t.TempDir(), "a.txt", "hello")
	b := &FactsBuilder{
		Parser: &fakeParser{chunks: []parser.Chunk{{Content: "hello"}}},
		Embed:  &fakeEmbed{err: errors.New("model not loaded")},
	}

	f, err := b.Build(context.Background(), path)
	if err != nil {
		t.Fatalf("embed failure must not drop the file: %v", err)
	}
	if f.Vector != nil {
		t.Errorf("Vector = %v, want nil", f.Vector)
	}
	if f.Snippet != "hello" || f.Path != path {
		t.Errorf("other facts lost: %+v", f)
	}
}

func TestFactsBuilder_ParseFailureFallsBackToMetadataForEmbedding(t *testing.T) {
	path := writeFile(t, t.TempDir(), "photo.xyz", "data")
	emb := &fakeEmbed{}
	b := &FactsBuilder{Parser: &fakeParser{err: errors.New("unsupported")}, Embed: emb}

	f, err := b.Build(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Snippet != "" {
		t.Errorf("Snippet = %q, want empty", f.Snippet)
	}
	if len(emb.texts) != 1 || !strings.Contains(emb.texts[0], "filename: photo.xyz") {
		t.Errorf("embedded %q, want the metadata description", emb.texts)
	}
	if f.Vector == nil {
		t.Error("Vector missing")
	}
}

func TestFactsBuilder_SnippetIsTruncated(t *testing.T) {
	path := writeFile(t, t.TempDir(), "a.txt", "x")
	b := &FactsBuilder{
		Parser:     &fakeParser{chunks: []parser.Chunk{{Content: strings.Repeat("a", 50)}}},
		SnippetLen: 10,
	}
	f, err := b.Build(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Snippet) != 10 {
		t.Fatalf("len(Snippet) = %d, want 10", len(f.Snippet))
	}
}

func TestFactsBuilder_EmptyAndHiddenFilesAreNotReadOrEmbedded(t *testing.T) {
	dir := t.TempDir()
	emb := &fakeEmbed{}
	fp := &fakeParser{chunks: []parser.Chunk{{Content: "x"}}}
	b := &FactsBuilder{Parser: fp, Embed: emb}

	for _, p := range []string{writeFile(t, dir, "empty.txt", ""), writeFile(t, dir, ".hidden.txt", "data")} {
		f, err := b.Build(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if f.Path != p || f.Snippet != "" || f.Vector != nil {
			t.Errorf("%s: %+v", p, f)
		}
	}
	if len(emb.texts) != 0 || len(fp.paths) != 0 {
		t.Errorf("read or embedded a skipped file: parse=%v embed=%v", fp.paths, emb.texts)
	}
}

func TestFactsBuilder_MissingFileIsAnError(t *testing.T) {
	b := &FactsBuilder{Parser: &fakeParser{}, Embed: &fakeEmbed{}}
	if _, err := b.Build(context.Background(), filepath.Join(t.TempDir(), "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestFactsBuilder_CancelledContextIsAnError(t *testing.T) {
	path := writeFile(t, t.TempDir(), "a.txt", "hello")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &FactsBuilder{
		Parser: &fakeParser{chunks: []parser.Chunk{{Content: "hello"}}},
		Embed:  &fakeEmbed{err: ctx.Err()},
	}
	if _, err := b.Build(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFactsBuilder_RelPathOutsideRootsIsBaseName(t *testing.T) {
	path := writeFile(t, t.TempDir(), "sub/a.txt", "x")
	b := &FactsBuilder{Roots: []string{t.TempDir()}}
	f, err := b.Build(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if f.RelPath != "a.txt" {
		t.Fatalf("RelPath = %q, want a.txt", f.RelPath)
	}
}
