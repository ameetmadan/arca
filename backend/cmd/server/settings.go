package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/scan"
)

// maxWatchedFolders bounds PUT /settings/folders; each folder is walked by
// the scanner and the semantic indexer.
const maxWatchedFolders = 20

// settingsRoutes registers the app settings and indexing endpoints on r
// (mounted under /api/v1).
func (s *server) settingsRoutes(r chi.Router) {
	r.Get("/settings", s.handleSettingsGet)
	r.Put("/settings/folders", s.handleSettingsFolders)
	r.Get("/index/status", s.handleIndexStatus)
	r.Post("/index/rescan", s.handleIndexRescan)
}

type settingsJSON struct {
	Version        string   `json:"version"`
	WatchedFolders []string `json:"watched_folders"`
	// ActiveModel is the id of the LLM the server uses, or "" when none is
	// configured or its file is missing.
	ActiveModel string `json:"active_model"`
	// EmbeddingReady reports whether the embedding model is downloaded and
	// configured, which indexing and search need.
	EmbeddingReady bool `json:"embedding_ready"`
	// DataDir holds the config, databases and models.
	DataDir string `json:"data_dir"`
	// LogFile is where the app writes the server's output.
	LogFile string `json:"log_file"`
}

// GET /api/v1/settings
func (s *server) handleSettingsGet(w http.ResponseWriter, _ *http.Request) {
	apiResponse(w, http.StatusOK, s.settings(), "")
}

func (s *server) settings() settingsJSON {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg := s.app.Cfg
	out := settingsJSON{
		Version:        version,
		WatchedFolders: append([]string{}, cfg.Watcher.Folders...),
		DataDir:        filepath.Dir(s.app.ConfigPath),
		LogFile:        filepath.Join(logDir(), "arca-server.log"),
	}
	if id := s.mm.IDForPath(cfg.LLM.ModelPath); id != "" && s.mm.IsPresent(id) {
		out.ActiveModel = id
	}
	out.EmbeddingReady = s.embeddingReady()
	return out
}

// embeddingReady reports whether the configured embedding model file exists.
// cfgMu must be held.
func (s *server) embeddingReady() bool {
	if s.app.Cfg == nil {
		return false
	}
	p := s.app.Cfg.Embed.ModelPath
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// PUT /api/v1/settings/folders — body {"folders": ["/abs/path", ...]}
//
// Saves the watched folders to config.yaml. Watched folders are read by
// several components at start-up, so the change applies after a restart; the
// response says so with "restart_required": true and the app restarts the
// server.
func (s *server) handleSettingsFolders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folders []string `json:"folders"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folders == nil {
		apiResponse(w, http.StatusBadRequest, nil, `body must be {"folders": [...]}`)
		return
	}
	folders, err := validateFolders(req.Folders, filepath.Dir(s.app.ConfigPath))
	if err != nil {
		apiResponse(w, http.StatusUnprocessableEntity, nil, err.Error())
		return
	}

	s.cfgMu.Lock()
	err = s.app.PersistWatchedFolders(folders)
	if err == nil {
		s.app.Cfg.Watcher.Folders = folders
	}
	s.cfgMu.Unlock()
	if err != nil {
		log.Printf("settings: persist watched folders: %v", err)
		apiResponse(w, http.StatusInternalServerError, nil, "could not save the configuration")
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{
		"watched_folders":  folders,
		"restart_required": true,
	}, "")
}

// validateFolders cleans and de-duplicates folders and checks that each is
// an existing directory the server may watch.
func validateFolders(in []string, dataDir string) ([]string, error) {
	if len(in) > maxWatchedFolders {
		return nil, fmt.Errorf("at most %d folders can be watched", maxWatchedFolders)
	}
	home, _ := os.UserHomeDir()
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, f := range in {
		if strings.HasPrefix(f, "~/") && home != "" {
			f = filepath.Join(home, f[2:])
		}
		if !filepath.IsAbs(f) {
			return nil, fmt.Errorf("%q is not an absolute path", f)
		}
		f = filepath.Clean(f)
		if f == "/" {
			return nil, errors.New("the whole disk cannot be watched; choose a folder")
		}
		if dataDir != "" && (f == dataDir || strings.HasPrefix(f, dataDir+string(filepath.Separator)) ||
			strings.HasPrefix(dataDir, f+string(filepath.Separator))) {
			return nil, fmt.Errorf("%q contains Arca's own data folder", f)
		}
		info, err := os.Stat(f)
		if err != nil {
			return nil, fmt.Errorf("%q does not exist or cannot be read", f)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%q is not a folder", f)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, nil
}

// GET /api/v1/index/status
func (s *server) handleIndexStatus(w http.ResponseWriter, _ *http.Request) {
	if s.scanner == nil {
		apiResponse(w, http.StatusOK, scan.Status{}, "")
		return
	}
	apiResponse(w, http.StatusOK, s.scanner.Status(), "")
}

// POST /api/v1/index/rescan — scan the watched folders again in the background.
func (s *server) handleIndexRescan(w http.ResponseWriter, _ *http.Request) {
	if s.scanner == nil {
		apiResponse(w, http.StatusServiceUnavailable, nil, "indexing is unavailable")
		return
	}
	s.scanner.Trigger()
	apiResponse(w, http.StatusAccepted, map[string]bool{"started": true}, "")
}

// newScanner returns the scanner that indexes files already in the watched
// folders, using the same indexer as the watcher.
func (s *server) newScanner(statePath string) *scan.Scanner {
	ix := s.indexer()
	return &scan.Scanner{
		Index:     ix.IndexFile,
		Forget:    ix.Forget,
		Supported: s.supported,
		StatePath: statePath,
		Fatal: func(err error) string {
			var ie *pipeline.IndexError
			if errors.As(err, &ie) && ie.Stage == pipeline.StageEmbed {
				s.cfgMu.Lock()
				ready := s.embeddingReady()
				s.cfgMu.Unlock()
				if !ready {
					return "the embedding model is not downloaded yet"
				}
			}
			return ""
		},
	}
}

// supported reports whether the parser can read path. Parsers that do not
// say are assumed to read everything.
func (s *server) supported(path string) bool {
	if p, ok := s.app.Parser.(interface{ Supported(string) bool }); ok {
		return p.Supported(path)
	}
	return true
}

// logDir is where the app writes the server's log (see BackendProcess.swift).
func logDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "Arca")
}

// activeModelID is used by the model list to mark the active LLM.
func (s *server) activeModelID() string {
	return s.settings().ActiveModel
}
