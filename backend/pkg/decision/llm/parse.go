package llmengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxFolderNameRunes bounds the length of a model-proposed new folder name. It
// matches the limit the batch sorter applies to cluster names.
const maxFolderNameRunes = 50

// errMalformed marks model output that is not the strict JSON the prompt asked for.
var errMalformed = errors.New("llm output is not the expected JSON object")

// answer is the JSON object the model is asked to produce.
type answer struct {
	Folder string `json:"folder"`
	New    bool   `json:"new"`
}

// parseAnswer decodes the model's raw reply defensively. It strips a Markdown
// code fence and tolerates trailing text after the first JSON object, but
// rejects everything else: leading prose, unknown fields, an empty folder.
func parseAnswer(raw string) (answer, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		// Drop the opening fence line, including any language tag like "json".
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
		s = strings.TrimSpace(s)
	}
	if !strings.HasPrefix(s, "{") {
		return answer{}, errMalformed
	}

	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var a answer
	if err := dec.Decode(&a); err != nil {
		return answer{}, errMalformed
	}
	a.Folder = strings.TrimSpace(a.Folder)
	if a.Folder == "" {
		return answer{}, errMalformed
	}
	return a, nil
}

// sanitizeNewName turns a model-proposed folder name into a single safe path
// component. It returns false if nothing usable is left. The result never
// contains a path separator or "..", never starts with a dot (no hidden or
// relative names), has no control characters, and is truncated on a rune
// boundary.
func sanitizeNewName(name string) (string, bool) {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':':
			b.WriteRune('-')
		case unicode.IsControl(r), r == utf8.RuneError:
			// drop
		default:
			b.WriteRune(r)
		}
	}
	s := b.String()
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	s = strings.Trim(s, " .-\t")
	if runes := []rune(s); len(runes) > maxFolderNameRunes {
		s = string(runes[:maxFolderNameRunes])
		s = strings.Trim(s, " .-\t")
	}
	if s == "" {
		return "", false
	}
	return s, true
}

// truncateRunes returns s cut to at most n runes, never splitting a rune.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// cleanSnippet prepares untrusted file text for the prompt: invalid UTF-8 and
// control characters are replaced by spaces, whitespace is collapsed, angle
// brackets are removed so the text cannot forge the delimiter tags around it,
// and the result is cut to n runes.
func cleanSnippet(s string, n int) string {
	var b bytes.Buffer
	lastSpace := true
	for _, r := range s {
		switch {
		case r == '<' || r == '>':
			continue
		case r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsControl(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return truncateRunes(strings.TrimSpace(b.String()), n)
}
