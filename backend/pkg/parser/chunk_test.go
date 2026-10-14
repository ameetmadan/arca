package parser

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func meta(page int) map[string]interface{} {
	return map[string]interface{}{"source": "/a.pdf", "page": page}
}

func TestRechunkJoinsSmallParagraphsWithSameMetadata(t *testing.T) {
	in := []Chunk{
		{Content: "first", Metadata: meta(1)},
		{Content: "second", Metadata: meta(1)},
		{Content: "third", Metadata: meta(2)},
	}
	out := Rechunk(in, 100)
	if len(out) != 2 {
		t.Fatalf("got %d chunks: %+v", len(out), out)
	}
	if out[0].Content != "first\n\nsecond" || out[0].Metadata["page"] != 1 {
		t.Fatalf("chunk 0 = %+v", out[0])
	}
	if out[1].Content != "third" || out[1].Metadata["page"] != 2 {
		t.Fatalf("chunk 1 = %+v", out[1])
	}
}

func TestRechunkSplitsLongTextAtSentenceBoundaries(t *testing.T) {
	sentence := "This is a sentence about invoices. "
	in := []Chunk{{Content: strings.Repeat(sentence, 100), Metadata: meta(1)}}
	out := Rechunk(in, 200)
	if len(out) < 10 {
		t.Fatalf("expected many chunks, got %d", len(out))
	}
	total := 0
	for i, c := range out {
		if n := utf8.RuneCountInString(c.Content); n > 300 {
			t.Fatalf("chunk %d has %d runes", i, n)
		}
		if !strings.HasSuffix(c.Content, ".") {
			t.Fatalf("chunk %d not cut at a sentence end: %q", i, c.Content)
		}
		if c.Metadata["page"] != 1 {
			t.Fatalf("chunk %d lost its metadata", i)
		}
		total += strings.Count(c.Content, "invoices")
	}
	if total != 100 {
		t.Fatalf("lost text: %d of 100 sentences", total)
	}
}

func TestRechunkHardCutsTextWithoutSpacesOnRuneBoundaries(t *testing.T) {
	in := []Chunk{{Content: strings.Repeat("日本語", 200)}}
	out := Rechunk(in, 100)
	joined := ""
	for _, c := range out {
		if !utf8.ValidString(c.Content) {
			t.Fatal("split inside a rune")
		}
		joined += c.Content
	}
	if joined != strings.Repeat("日本語", 200) {
		t.Fatal("text changed")
	}
}

func TestRechunkDropsBlankChunks(t *testing.T) {
	out := Rechunk([]Chunk{{Content: "  \n "}, {Content: "x"}}, 100)
	if len(out) != 1 || out[0].Content != "x" {
		t.Fatalf("got %+v", out)
	}
}

func TestSupported(t *testing.T) {
	for path, want := range map[string]bool{
		"/a/b.PDF": true, "x.md": true, "x.csv": true, "x.xlsx": true, "x.docx": true,
		"x.jpg": false, "x": false, "x.doc": false,
	} {
		if got := Supported(path); got != want {
			t.Errorf("Supported(%q) = %v", path, got)
		}
	}
}
