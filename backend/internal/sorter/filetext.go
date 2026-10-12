package sorter

import (
	"path/filepath"
	"strings"

	"github.com/ameetmadan/arca/internal/filefacts"
)

// fileText returns a text representation of path for embedding.
// Returns "" for zero-byte files, hidden files, and unreadable files.
// The logic is shared with the event pipeline; see internal/filefacts.
func (s *Sorter) fileText(path string) string {
	return filefacts.Text(s.Parser, path, sortSnippetLen*5)
}

func deriveFolder(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
