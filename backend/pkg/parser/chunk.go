package parser

import (
	"strings"
	"unicode/utf8"
)

// DefaultChunkChars is the target chunk size. About 1,200 characters is
// roughly 300 tokens: well inside the embedding model's 512-token window,
// and large enough that a chunk carries a complete thought.
const DefaultChunkChars = 1200

// Rechunk evens out chunk sizes for retrieval. Consecutive small chunks with
// the same metadata (paragraphs of one document or page) are joined up to
// target characters, and chunks longer than 1.5 x target are split at
// paragraph, sentence or word boundaries. Split pieces keep their
// chunk's metadata.
func Rechunk(chunks []Chunk, target int) []Chunk {
	if target <= 0 {
		return chunks
	}
	var out []Chunk
	for _, c := range chunks {
		for _, piece := range split(strings.TrimSpace(c.Content), target) {
			if piece == "" {
				continue
			}
			if n := len(out); n > 0 && sameMeta(out[n-1].Metadata, c.Metadata) &&
				utf8.RuneCountInString(out[n-1].Content)+utf8.RuneCountInString(piece)+2 <= target {
				out[n-1].Content += "\n\n" + piece
				continue
			}
			out = append(out, Chunk{Content: piece, Metadata: c.Metadata})
		}
	}
	return out
}

// split breaks text into pieces of at most 1.5 x target runes, preferring
// to cut at a paragraph break, then a sentence end, then a space.
func split(text string, target int) []string {
	limit := target + target/2
	var out []string
	for utf8.RuneCountInString(text) > limit {
		runes := []rune(text)
		window := string(runes[:target])
		cut := -1
		for _, sep := range []string{"\n\n", ". ", "? ", "! ", "\n", " "} {
			if i := strings.LastIndex(window, sep); i > len(window)/3 {
				cut = i + len(sep)
				break
			}
		}
		if cut < 0 {
			cut = len(window) // no boundary: hard cut on a rune boundary
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	return append(out, text)
}

func sameMeta(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
