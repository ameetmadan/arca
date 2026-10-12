package filefacts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/parser"
)

type fakeParser struct {
	chunks []parser.Chunk
	err    error
}

func (f fakeParser) Parse(string) ([]parser.Chunk, error) { return f.chunks, f.err }

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestText_UsesFirstChunkAndTruncates(t *testing.T) {
	p := write(t, "a.txt", "x")
	fp := fakeParser{chunks: []parser.Chunk{{Content: "abcdefgh"}, {Content: "second"}}}
	if got := Text(fp, p, 4); got != "abcd" {
		t.Fatalf("got %q, want %q", got, "abcd")
	}
	if got := Text(fp, p, 0); got != "abcdefgh" {
		t.Fatalf("maxContent 0 should not truncate, got %q", got)
	}
}

func TestText_SkipsEmptyHiddenAndMissing(t *testing.T) {
	fp := fakeParser{chunks: []parser.Chunk{{Content: "hello"}}}
	for name, path := range map[string]string{
		"empty":   write(t, "e.txt", ""),
		"hidden":  write(t, ".hidden.txt", "data"),
		"missing": filepath.Join(t.TempDir(), "nope.txt"),
	} {
		if got := Text(fp, path, 0); got != "" {
			t.Errorf("%s: got %q, want empty", name, got)
		}
	}
}

func TestText_FallsBackToMetadataWhenParserFails(t *testing.T) {
	p := write(t, "photo.bin", "12345")
	got := Text(fakeParser{err: errors.New("unsupported")}, p, 0)
	for _, want := range []string{"filename: photo.bin", "type: .bin", "size:", "modified:"} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback %q missing %q", got, want)
		}
	}
}

func TestEXIF_NilForNonImagesAndGarbage(t *testing.T) {
	if EXIF(write(t, "a.txt", "x")) != nil {
		t.Error("non-image should have no EXIF")
	}
	if EXIF(write(t, "a.jpg", "not a jpeg")) != nil {
		t.Error("garbage jpg should have no EXIF")
	}
}

func TestFirstChunk_DoesNotSplitMultiByteCharacters(t *testing.T) {
	fp := fakeParser{chunks: []parser.Chunk{{Content: "aé"}}} // "é" is 2 bytes
	got := FirstChunk(fp, "x", 2)                              // would cut "é" in half
	if got != "a" {
		t.Fatalf("got %q, want %q", got, "a")
	}
}

func TestFirstChunk_NilParser(t *testing.T) {
	if got := FirstChunk(nil, "x", 0); got != "" {
		t.Fatalf("got %q", got)
	}
}
