package semantic

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ameetmadan/arca/pkg/parser"
)

const (
	// DefaultMaxDepth is how many folder levels below the root OSSource
	// descends when MaxDepth is zero.
	DefaultMaxDepth = 6
	// DefaultMaxTextBytes is the longest text OSSource returns for one file
	// when MaxTextBytes is zero.
	DefaultMaxTextBytes = 2000
)

// OSSource is the Source backed by the real filesystem. It only reads: it
// never writes, moves or deletes anything (ADR-0006: that is the Executor's
// job). Symlinks are not followed, so a link inside the root cannot lead the
// indexer outside it.
type OSSource struct {
	// Parser extracts text from documents. If nil, or if it cannot parse a
	// file, the text falls back to file metadata.
	Parser parser.DocumentParser
	// MaxDepth limits how many folder levels below the root are listed;
	// zero means DefaultMaxDepth.
	MaxDepth int
	// MaxTextBytes truncates the text returned per file; zero means
	// DefaultMaxTextBytes.
	MaxTextBytes int
	// Describe, if set, derives the text for a regular, non-empty file at the
	// given absolute path, capped at maxBytes, in place of the built-in
	// Parser-then-metadata logic. internal/config sets it to the function the
	// pipeline's Facts builder uses, so that file vectors and folder vectors
	// come from the same text. The path has already been checked to be a
	// visible regular file directly inside the requested folder.
	Describe func(path string, maxBytes int) string
}

var _ Source = (*OSSource)(nil)

// Folders implements Source.
func (s *OSSource) Folders(ctx context.Context, root string) ([]string, error) {
	maxDepth := s.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir // an unreadable folder is not evidence
			}
			return nil
		}
		if p == root || !d.IsDir() { // symlinks are not directories to WalkDir
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		out = append(out, rel)
		if strings.Count(rel, "/")+1 >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// Files implements Source.
func (s *OSSource) Files(ctx context.Context, root, rel string) ([]SourceFile, error) {
	dir, err := folderPath(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SourceFile
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished between ReadDir and Info
		}
		out = append(out, SourceFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

// Text implements Source. It prefers the parser's first chunk and falls back
// to a line of metadata, the same shape the sorter uses, so file and folder
// vectors come from comparable text.
func (s *OSSource) Text(ctx context.Context, root, rel, name string) (string, error) {
	dir, err := folderPath(root, rel)
	if err != nil {
		return "", err
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("semantic: invalid file name %q", name)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name)
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", nil
	}

	limit := s.MaxTextBytes
	if limit <= 0 {
		limit = DefaultMaxTextBytes
	}
	if s.Describe != nil {
		return s.Describe(full, limit), nil
	}
	if s.Parser != nil {
		if chunks, err := s.Parser.Parse(full); err == nil && len(chunks) > 0 && chunks[0].Content != "" {
			return truncateUTF8(chunks[0].Content, limit), nil
		}
	}
	return fmt.Sprintf("filename: %s | type: %s | size: %.1fKB | modified: %s",
		name, strings.ToLower(filepath.Ext(name)),
		float64(info.Size())/1024, info.ModTime().Format("2006-01-02")), nil
}

// folderPath joins root and rel after checking that rel is a visible folder
// strictly below root.
func folderPath(root, rel string) (string, error) {
	if err := validateRoot(root); err != nil {
		return "", err
	}
	if err := validateRel(rel); err != nil {
		return "", err
	}
	if path.Clean(rel) != rel {
		return "", fmt.Errorf("semantic: folder %q is not clean", rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
