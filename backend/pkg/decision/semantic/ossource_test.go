package semantic

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/parser"
)

type stubParser struct{ chunks map[string]string }

func (p stubParser) Parse(path string) ([]parser.Chunk, error) {
	c, ok := p.chunks[filepath.Base(path)]
	if !ok {
		return nil, errors.New("unsupported")
	}
	return []parser.Chunk{{Content: c}}, nil
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOSSourceFolders(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "top.txt"), "x")
	write(t, filepath.Join(root, "Taxes", "2025", "a.pdf"), "x")
	write(t, filepath.Join(root, "Taxes", "b.pdf"), "x")
	write(t, filepath.Join(root, "Photos", "c.jpg"), "x")
	write(t, filepath.Join(root, ".git", "obj", "d"), "x")
	write(t, filepath.Join(root, "Photos", ".thumbs", "e"), "x")
	write(t, filepath.Join(root, "A", "B", "C", "D", "deep.txt"), "x")

	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret", "s.txt"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	src := &OSSource{MaxDepth: 3}
	got, err := src.Folders(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "A/B", "A/B/C", "Photos", "Taxes", "Taxes/2025"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Folders = %v\nwant     %v", got, want)
	}
}

func TestOSSourceFolderDefaultDepth(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "1", "2", "3", "4", "5", "6", "7", "8")
	write(t, filepath.Join(deep, "f.txt"), "x")
	got, err := (&OSSource{}).Folders(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != DefaultMaxDepth {
		t.Errorf("got %d folders, want %d (default depth): %v", len(got), DefaultMaxDepth, got)
	}
}

func TestOSSourceFolderOfMissingRoot(t *testing.T) {
	_, err := (&OSSource{}).Folders(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Error("Folders of a missing root = nil error")
	}
}

func TestOSSourceFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "F", "a.txt"), "hello")
	write(t, filepath.Join(root, "F", "b.txt"), "hi")
	write(t, filepath.Join(root, "F", ".hidden"), "x")
	write(t, filepath.Join(root, "F", "sub", "nested.txt"), "x")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	write(t, outside, "x")
	_ = os.Symlink(outside, filepath.Join(root, "F", "link.txt"))

	src := &OSSource{}
	got, err := src.Files(context.Background(), root, "F")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
		if f.Size == 0 || f.ModTime.IsZero() {
			t.Errorf("%s: size %d, mtime %v", f.Name, f.Size, f.ModTime)
		}
	}
	if want := []string{"a.txt", "b.txt"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Files = %v, want %v", names, want)
	}

	if _, err := src.Files(context.Background(), root, "Missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Files of a missing folder = %v, want fs.ErrNotExist", err)
	}
}

func TestOSSourceRefusesToEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	src := &OSSource{}
	for _, rel := range []string{"..", "../x", "/etc", "a/../../x", ".hidden", ""} {
		if _, err := src.Files(context.Background(), root, rel); err == nil {
			t.Errorf("Files(%q) = nil error", rel)
		}
		if _, err := src.Text(context.Background(), root, rel, "x"); err == nil {
			t.Errorf("Text(%q) = nil error", rel)
		}
	}
	if _, err := src.Text(context.Background(), root, "F", "../x"); err == nil {
		t.Error("Text with a path in the file name = nil error")
	}
}

func TestOSSourceText(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "F", "doc.md"), "raw bytes")
	write(t, filepath.Join(root, "F", "pic.bin"), "binary")
	write(t, filepath.Join(root, "F", "empty.txt"), "")
	write(t, filepath.Join(root, "F", "long.md"), "raw")

	src := &OSSource{
		Parser:       stubParser{chunks: map[string]string{"doc.md": "parsed content", "long.md": strings.Repeat("é", 100)}},
		MaxTextBytes: 11,
	}
	ctx := context.Background()

	if got, _ := src.Text(ctx, root, "F", "doc.md"); got != "parsed content"[:11] {
		t.Errorf("parsed text = %q", got)
	}
	// Truncation never splits a multi-byte rune.
	got, err := src.Text(ctx, root, "F", "long.md")
	if err != nil || got != strings.Repeat("é", 5) {
		t.Errorf("long text = %q, %v", got, err)
	}
	// No parser result: fall back to file metadata.
	got, err = src.Text(ctx, root, "F", "pic.bin")
	if err != nil || !strings.HasPrefix(got, "filename: pic.bin | type: .bin | size:") {
		t.Errorf("fallback text = %q, %v", got, err)
	}
	// Zero-byte files carry no evidence.
	if got, err := src.Text(ctx, root, "F", "empty.txt"); err != nil || got != "" {
		t.Errorf("empty file text = %q, %v", got, err)
	}
	if _, err := src.Text(ctx, root, "F", "missing.txt"); err == nil {
		t.Error("Text of a missing file = nil error")
	}
}

func TestOSSourceWithNilParserUsesMetadata(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "F", "a.txt"), "hello")
	got, err := (&OSSource{}).Text(context.Background(), root, "F", "a.txt")
	if err != nil || !strings.HasPrefix(got, "filename: a.txt") {
		t.Errorf("text = %q, %v", got, err)
	}
}

func TestOSSourceIndexesEndToEnd(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Taxes", "t1.txt"), "tax a1")
	write(t, filepath.Join(root, "Taxes", "t2.txt"), "tax a2")
	write(t, filepath.Join(root, "Photos", "p1.txt"), "photo b1")
	write(t, filepath.Join(root, "Photos", "p2.txt"), "photo b2")

	p := stubParser{chunks: map[string]string{"t1.txt": "a1", "t2.txt": "a2", "p1.txt": "b1", "p2.txt": "b2"}}
	ix, store := newIndexer(t, DefaultTunables(), &OSSource{Parser: p}, topicEmbedder())
	if err := ix.IndexRoot(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	all, _ := store.List(context.Background())
	if len(all) != 2 || all[0].Rel != "Photos" || all[1].Rel != "Taxes" {
		t.Errorf("stored = %+v", all)
	}
}
