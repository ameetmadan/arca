package pipeline

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/ameetmadan/arca/internal/filefacts"
	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/parser"
)

// DefaultSnippetLen is the default maximum size, in bytes, of Facts.Snippet.
const DefaultSnippetLen = 1000

// FactsBuilder gathers the decision.Facts for one file. It reads the file
// (through the parser and the metadata fallback in internal/filefacts) but
// never modifies it.
type FactsBuilder struct {
	Parser parser.DocumentParser
	Embed  embed.EmbeddingProvider
	// Roots are the watched folders; Facts.RelPath is relative to the one that
	// contains the file. A file outside every root gets its base name.
	Roots []string
	// SnippetLen caps Facts.Snippet in bytes; zero means DefaultSnippetLen.
	SnippetLen int
}

// Build returns the Facts for the file at path.
//
// It fails only if the file cannot be stat'ed (for example it was deleted) or
// ctx is cancelled. Anything else degrades gracefully: when the parser yields
// nothing the Snippet is empty, and when embedding fails the Vector is nil so
// that engines which do not need it (rules) can still decide.
//
// Facts.Snippet holds file content and must never be logged.
func (b *FactsBuilder) Build(ctx context.Context, path string) (decision.Facts, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return decision.Facts{}, fmt.Errorf("facts: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return decision.Facts{}, fmt.Errorf("facts: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(abs))
	f := decision.Facts{
		Path:     abs,
		RelPath:  b.relPath(abs),
		Ext:      ext,
		MIME:     mimeType(ext),
		Size:     info.Size(),
		Modified: info.ModTime(),
		EXIF:     filefacts.EXIF(abs),
	}

	if filefacts.Skip(abs, info) {
		return f, nil // empty or hidden: nothing to read or embed
	}

	snippetLen := b.SnippetLen
	if snippetLen <= 0 {
		snippetLen = DefaultSnippetLen
	}
	f.Snippet = filefacts.FirstChunk(b.Parser, abs, snippetLen)

	text := f.Snippet
	if text == "" {
		text = filefacts.Fallback(abs, info)
	}
	if b.Embed != nil {
		vec, err := b.Embed.Embed(ctx, text)
		if err != nil {
			if ctx.Err() != nil {
				return decision.Facts{}, ctx.Err()
			}
			// Deliberately not fatal and not logged with the text: the
			// file must still reach the decision engines.
			return f, nil
		}
		f.Vector = vec
	}
	return f, nil
}

func (b *FactsBuilder) relPath(abs string) string {
	for _, root := range b.Roots {
		rel, err := filepath.Rel(root, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return filepath.Base(abs)
}

// mimeType maps a lower-case extension to a media type without parameters,
// or "" when unknown.
func mimeType(ext string) string {
	t := mime.TypeByExtension(ext)
	if t == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(t); err == nil {
		return mt
	}
	return t
}
