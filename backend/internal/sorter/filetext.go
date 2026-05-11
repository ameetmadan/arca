package sorter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// fileText returns a text representation of path for embedding.
// Returns "" for zero-byte files, hidden files, and unreadable files.
func (s *Sorter) fileText(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return ""
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return ""
	}

	// Try content parser first (PDF, DOCX, XLSX, MD, TXT).
	if chunks, err := s.Parser.Parse(path); err == nil && len(chunks) > 0 {
		content := chunks[0].Content
		if len(content) > sortSnippetLen*5 {
			content = content[:sortSnippetLen*5]
		}
		return content
	}

	// For images, attempt EXIF before falling back to stat metadata.
	ext := strings.ToLower(filepath.Ext(path))
	if isImageExt(ext) {
		if text := exifText(path); text != "" {
			return text
		}
	}

	return fmt.Sprintf("filename: %s | type: %s | size: %.1fKB | modified: %s",
		filepath.Base(path), ext,
		float64(info.Size())/1024,
		info.ModTime().Format("2006-01-02"),
	)
}

func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".tiff", ".tif", ".heic", ".heif", ".webp":
		return true
	}
	return false
}

func exifText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return ""
	}

	var parts []string
	if tm, err := x.DateTime(); err == nil {
		parts = append(parts, "date: "+tm.Format("2006-01-02"))
	}
	if lat, lon, err := x.LatLong(); err == nil {
		parts = append(parts, fmt.Sprintf("location: %.4f,%.4f", lat, lon))
	}
	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("filename: %s | type: %s | %s",
		filepath.Base(path),
		strings.ToLower(filepath.Ext(path)),
		strings.Join(parts, " | "),
	)
}

func deriveFolder(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
