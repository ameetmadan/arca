package main

import (
	"github.com/ameetmadan/arca/internal/calibration"
	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/pkg/decision"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

func (s *server) handleCalibration(w http.ResponseWriter, r *http.Request) {
	if s.app == nil || s.app.Folders == nil {
		apiResponse(w, 503, nil, "folder index unavailable")
		return
	}
	limit, err := intParam(r.URL.Query().Get("limit"), "limit")
	if err != nil || limit < 0 || limit > 20 {
		apiResponse(w, 400, nil, "limit must be between 1 and 20")
		return
	}
	if limit == 0 {
		limit = 15
	}
	folders, err := s.app.Folders.List(r.Context())
	if err != nil {
		apiResponse(w, 500, nil, "could not load folder index")
		return
	}
	roots := watchedRoots(s.app.Cfg.Watcher.Folders)
	builder := &pipeline.FactsBuilder{Parser: s.app.Parser, Embed: s.app.EmbedProvider, Roots: roots}
	files := []calibration.File{}
	for _, root := range roots {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if r.Context().Err() != nil {
				return r.Context().Err()
			}
			if walkErr != nil {
				return nil
			}
			if path != root && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".txt", ".md", ".pdf", ".docx", ".csv", ".xlsx":
			default:
				return nil
			}
			f, err := builder.Build(r.Context(), path)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			files = append(files, calibration.File{Root: root, Path: rel, Vector: f.Vector})
			return nil
		})
		if err != nil {
			apiResponse(w, 500, nil, "calibration scan interrupted")
			return
		}
	}
	apiResponse(w, 200, map[string]any{"items": calibration.Rank(files, folders, limit)}, "")
}

// Prepare only when the user answers a card. GET and Skip are read-only.
func (s *server) handleCalibrationPrepare(w http.ResponseWriter, r *http.Request) {
	if s.app == nil || s.app.Cfg == nil || s.app.Folders == nil {
		apiResponse(w, 503, nil, "folder index unavailable")
		return
	}
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	var req struct {
		Root string `json:"root"`
		File string `json:"file"`
	}
	if err := decodeDecisionBody(w, r, &req, false); err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	valid := false
	for _, root := range watchedRoots(s.app.Cfg.Watcher.Folders) {
		if root == req.Root {
			valid = true
		}
	}
	if !valid || !filepath.IsLocal(req.File) {
		apiResponse(w, 400, nil, "file must be inside a watched folder")
		return
	}
	path := filepath.Join(req.Root, req.File)
	canonicalRoot, rootErr := filepath.EvalSymlinks(req.Root)
	resolved, err := filepath.EvalSymlinks(path)
	if rootErr != nil || err != nil || resolved != filepath.Join(canonicalRoot, req.File) {
		apiResponse(w, 400, nil, "file must not traverse symlinks")
		return
	}
	page, err := svc.List(r.Context(), decisions.ListFilter{Root: req.Root, File: req.File, Status: decisions.StatusPending, Limit: 1})
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	if len(page.Items) > 0 {
		apiResponse(w, 200, toDecisionJSON(page.Items[0]), "")
		return
	}
	builder := &pipeline.FactsBuilder{Parser: s.app.Parser, Embed: s.app.EmbedProvider, Roots: []string{req.Root}}
	facts, err := builder.Build(r.Context(), path)
	if err != nil {
		apiResponse(w, 400, nil, "file unavailable")
		return
	}
	folders, err := s.app.Folders.List(r.Context())
	if err != nil {
		apiResponse(w, 500, nil, "folder index unavailable")
		return
	}
	ranked := calibration.Rank([]calibration.File{{Root: req.Root, Path: req.File, Vector: facts.Vector}}, folders, 1)
	target := ""
	if len(ranked) > 0 {
		target = ranked[0].Candidates[0].Target
	}
	result, err := svc.SubmitWithFacts(r.Context(), req.Root, req.File, decision.Decision{Action: decision.AskUser, Target: target, Source: "calibration", Reason: "optional calibration"}, facts)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	if result.Record == nil {
		apiResponse(w, 409, nil, "enable suggestions for this folder before calibrating")
		return
	}
	apiResponse(w, 200, toDecisionJSON(*result.Record), "")
}
