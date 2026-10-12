// Package filefacts derives short text and metadata descriptions of a file.
// It is shared by the sorter (which embeds the text in batches) and the event
// pipeline's Facts builder, so both describe a file the same way.
//
// The text can contain file content, so callers must never log it.
package filefacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/rwcarlsen/goexif/exif"

	"github.com/ameetmadan/arca/pkg/parser"
)

// Text returns a text representation of path for embedding: the first parsed
// chunk (capped at maxContent bytes when maxContent > 0), else image EXIF, else
// a filename/size/date description. It returns "" for zero-byte files, hidden
// files (name starts with "."), and unreadable files.
func Text(p parser.DocumentParser, path string, maxContent int) string {
	info, err := os.Stat(path)
	if err != nil || Skip(path, info) {
		return ""
	}

	if content := FirstChunk(p, path, maxContent); content != "" {
		return content
	}
	return Fallback(path, info)
}

// Skip reports whether a file has nothing worth describing: it is empty or
// hidden (its name starts with ".").
func Skip(path string, info os.FileInfo) bool {
	return info.Size() == 0 || strings.HasPrefix(filepath.Base(path), ".")
}

// FirstChunk returns the content of the first chunk the parser yields for
// path, truncated to maxContent bytes when maxContent > 0. It returns "" when
// the parser is nil, fails, or yields nothing.
func FirstChunk(p parser.DocumentParser, path string, maxContent int) string {
	if p == nil {
		return ""
	}
	chunks, err := p.Parse(path)
	if err != nil || len(chunks) == 0 {
		return ""
	}
	content := chunks[0].Content
	if maxContent > 0 && len(content) > maxContent {
		n := maxContent
		for n > 0 && !utf8.RuneStart(content[n]) { // do not cut a multi-byte character in half
			n--
		}
		content = content[:n]
	}
	return content
}

// Fallback describes a file that has no parsable text: EXIF-derived text for
// images that carry date or location data, otherwise name, type, size and
// modification date taken from info.
func Fallback(path string, info os.FileInfo) string {
	ext := strings.ToLower(filepath.Ext(path))
	if IsImageExt(ext) {
		if text := exifText(path, ext); text != "" {
			return text
		}
	}
	return fmt.Sprintf("filename: %s | type: %s | size: %.1fKB | modified: %s",
		filepath.Base(path), ext,
		float64(info.Size())/1024,
		info.ModTime().Format("2006-01-02"),
	)
}

// IsImageExt reports whether ext (lower-case, with leading dot) is an image
// format we try to read EXIF from.
func IsImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".tiff", ".tif", ".heic", ".heif", ".webp":
		return true
	}
	return false
}

// Keys used in the map returned by EXIF.
const (
	EXIFDate     = "date"
	EXIFLocation = "location"
)

// EXIF returns the metadata we extract from an image: "date" (YYYY-MM-DD) and
// "location" ("lat,lon"). It returns nil when the file is not a readable image
// or carries neither.
func EXIF(path string) map[string]string {
	if !IsImageExt(strings.ToLower(filepath.Ext(path))) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return nil
	}

	out := map[string]string{}
	if tm, err := x.DateTime(); err == nil {
		out[EXIFDate] = tm.Format("2006-01-02")
	}
	if lat, lon, err := x.LatLong(); err == nil {
		out[EXIFLocation] = fmt.Sprintf("%.4f,%.4f", lat, lon)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func exifText(path, ext string) string {
	m := EXIF(path)
	if m == nil {
		return ""
	}
	var parts []string
	if v, ok := m[EXIFDate]; ok {
		parts = append(parts, "date: "+v)
	}
	if v, ok := m[EXIFLocation]; ok {
		parts = append(parts, "location: "+v)
	}
	return fmt.Sprintf("filename: %s | type: %s | %s",
		filepath.Base(path), ext, strings.Join(parts, " | "))
}
