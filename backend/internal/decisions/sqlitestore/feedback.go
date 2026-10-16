package sqlitestore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"path/filepath"
	"strings"
)

type Suggestion struct {
	ID      string `json:"id"`
	Root    string `json:"root"`
	When    string `json:"when"`
	Action  string `json:"action"`
	Target  string `json:"target"`
	Reason  string `json:"reason"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
}

// Suggestions are conservative extension corrections scoped to a watched root.
// They are durable feedback-derived proposals, never installed in the rules engine.
func (s *Store) Suggestions(ctx context.Context, threshold int) ([]Suggestion, error) {
	if threshold <= 0 {
		threshold = 3
	}
	rows, err := s.db.QueryContext(ctx, `SELECT root,file,target FROM feedback WHERE target<>'' AND accepted=0 ORDER BY root,file,target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ root, ext, target string }
	counts := map[key]int{}
	order := []key{}
	for rows.Next() {
		var root, file, target string
		if err = rows.Scan(&root, &file, &target); err != nil {
			return nil, err
		}
		ext := strings.ToLower(filepath.Ext(file))
		if ext == "" {
			continue
		}
		k := key{root, ext, target}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []Suggestion{}
	for _, k := range order {
		if counts[k] >= threshold {
			digest := sha256.Sum256([]byte(k.root + "/" + k.ext + "/" + k.target))
			out = append(out, Suggestion{ID: fmt.Sprintf("correction-%x", digest[:16]), Root: k.root, When: fmt.Sprintf("Ext == %q && hasPrefix(Path, %q)", k.ext, k.root+string(filepath.Separator)), Action: "move", Target: k.target, Reason: "repeated extension corrections; review before enabling", Count: counts[k]})
		}
	}
	return out, nil
}

func (s *Store) LearnedExamples(ctx context.Context) ([]semantic.LearnedExample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT root,file,target,vector FROM feedback f WHERE target<>'' AND id=(SELECT id FROM feedback newer WHERE newer.root=f.root AND newer.file=f.file ORDER BY resolved DESC LIMIT 1) ORDER BY resolved,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []semantic.LearnedExample{}
	for rows.Next() {
		var e semantic.LearnedExample
		var data []byte
		if err = rows.Scan(&e.Root, &e.File, &e.Target, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &e.Vector); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
