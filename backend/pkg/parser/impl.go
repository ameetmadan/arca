package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/nguyenthenguyen/docx"
	"github.com/xuri/excelize/v2"
)

// MultiParser dispatches to a format-specific parser based on file extension.
type MultiParser struct{}

func NewMultiParser() *MultiParser { return &MultiParser{} }

func (m *MultiParser) Parse(path string) ([]Chunk, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt":
		return parsePlainText(path, ext)
	case ".pdf":
		return parsePDF(path)
	case ".docx":
		return parseDOCX(path)
	case ".xlsx":
		return parseXLSX(path)
	default:
		return nil, fmt.Errorf("parser: unsupported file extension %q", ext)
	}
}

// ── plain text ────────────────────────────────────────────────────────────────

func parsePlainText(path, ext string) ([]Chunk, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var chunks []Chunk
	for _, para := range strings.Split(string(data), "\n\n") {
		if p := strings.TrimSpace(para); p != "" {
			chunks = append(chunks, Chunk{
				Content:  p,
				Metadata: map[string]interface{}{"source": path, "format": ext},
			})
		}
	}
	return chunks, nil
}

// ── PDF ───────────────────────────────────────────────────────────────────────

func parsePDF(path string) ([]Chunk, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("parser: pdf open %s: %w", path, err)
	}
	defer f.Close()

	numPages := r.NumPage()
	var chunks []Chunk
	for i := 1; i <= numPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Content: strings.TrimSpace(text),
			Metadata: map[string]interface{}{
				"source": path,
				"format": ".pdf",
				"page":   i,
			},
		})
	}
	return chunks, nil
}

// ── DOCX ──────────────────────────────────────────────────────────────────────

func parseDOCX(path string) ([]Chunk, error) {
	r, err := docx.ReadDocxFile(path)
	if err != nil {
		return nil, fmt.Errorf("parser: docx open %s: %w", path, err)
	}
	defer r.Close()

	raw := r.Editable().GetContent()
	var chunks []Chunk
	for _, para := range strings.Split(raw, "\n") {
		if t := strings.TrimSpace(para); t != "" {
			chunks = append(chunks, Chunk{
				Content:  t,
				Metadata: map[string]interface{}{"source": path, "format": ".docx"},
			})
		}
	}
	return chunks, nil
}

// ── XLSX ──────────────────────────────────────────────────────────────────────

const xlsxRowsPerChunk = 50

func parseXLSX(path string) ([]Chunk, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("parser: xlsx open %s: %w", path, err)
	}
	defer f.Close()

	var chunks []Chunk
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil || len(rows) == 0 {
			continue
		}
		header := rows[0]
		data := rows[1:]

		for start := 0; start < len(data); start += xlsxRowsPerChunk {
			end := start + xlsxRowsPerChunk
			if end > len(data) {
				end = len(data)
			}
			var sb strings.Builder
			sb.WriteString(strings.Join(header, "\t") + "\n")
			for _, row := range data[start:end] {
				sb.WriteString(strings.Join(padRow(row, len(header)), "\t") + "\n")
			}
			chunks = append(chunks, Chunk{
				Content: sb.String(),
				Metadata: map[string]interface{}{
					"source":    path,
					"format":    ".xlsx",
					"sheet":     sheet,
					"row_start": start + 2,
					"row_end":   start + 1 + (end - start),
				},
			})
		}
	}
	return chunks, nil
}

func padRow(row []string, width int) []string {
	out := make([]string, width)
	copy(out, row)
	return out
}
