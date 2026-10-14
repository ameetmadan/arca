// Package scan indexes the files already present in the watched folders.
//
// The watcher only reports changes, so without a scan a file that was in a
// folder before Arca started watching it would never be searchable. A scan
// walks every root, indexes supported files whose size or modification time
// changed since the last scan, and forgets files that have disappeared. What
// was indexed is remembered in a small JSON state file, so a rescan after a
// restart only touches what changed while Arca was not running.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults for the zero values of the corresponding Scanner fields.
const (
	DefaultMaxFileBytes = 50 << 20 // larger files are skipped
	DefaultMaxFiles     = 20000    // per scan, across all roots
	saveEvery           = 25       // files indexed between state saves
)

// skippedDirs are directory names never descended into. Package-like
// directories (an .app, a Photos library) are skipped by extension below.
var skippedDirs = map[string]bool{
	"node_modules": true,
	"__pycache__":  true,
	"vendor":       true,
}

var packageExts = map[string]bool{
	".app": true, ".bundle": true, ".framework": true, ".photoslibrary": true,
	".musiclibrary": true, ".xcodeproj": true, ".xcworkspace": true, ".pkg": true,
}

// Status describes the current or last scan. It is safe to serialise.
type Status struct {
	Running bool `json:"running"`
	// Found is the number of supported files seen so far in this scan.
	Found int `json:"found"`
	// Done counts files handled so far: indexed, unchanged or failed.
	Done      int `json:"done"`
	Indexed   int `json:"indexed"`
	Unchanged int `json:"unchanged"`
	Failed    int `json:"failed"`
	Removed   int `json:"removed"`
	// Chunks is the number of chunks written by this scan.
	Chunks int `json:"chunks"`
	// Stopped is set when the scan gave up early, e.g. because the embedding
	// model is not available yet. It never contains file paths.
	Stopped    string     `json:"stopped,omitempty"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Scanner indexes existing files. Index, Supported and StatePath are
// required; the rest are optional.
type Scanner struct {
	// Index indexes one file and returns the number of chunks written.
	Index func(ctx context.Context, path string) (int, error)
	// Forget removes a file that no longer exists from the index.
	Forget func(ctx context.Context, path string) error
	// Supported reports whether a file of this name can be indexed.
	Supported func(path string) bool
	// Fatal reports whether an Index error means no other file will index
	// either (the embedding model is missing), so the scan should stop.
	// It returns the reason to report, or "".
	Fatal func(err error) string
	// StatePath is the JSON file recording what has been indexed.
	StatePath string

	MaxFileBytes int64
	MaxFiles     int

	mu      sync.Mutex
	status  Status
	trigger chan struct{}
	once    sync.Once
}

type fileState struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *Scanner) init() {
	s.once.Do(func() { s.trigger = make(chan struct{}, 1) })
}

// Status returns a snapshot of the current or last scan.
func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Trigger asks Run to scan again. It never blocks; a request made while a
// scan is running starts one more scan after it.
func (s *Scanner) Trigger() {
	s.init()
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run scans roots once at start and again on every Trigger, until ctx is
// cancelled. It is meant to run as a server worker.
func (s *Scanner) Run(ctx context.Context, roots []string) {
	s.init()
	s.Trigger()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
			s.Scan(ctx, roots)
		}
	}
}

// Scan runs one scan over roots and returns its final status.
func (s *Scanner) Scan(ctx context.Context, roots []string) Status {
	now := time.Now()
	s.update(func(st *Status) { *st = Status{Running: true, StartedAt: &now} })

	state := s.loadState()
	seen := map[string]bool{}
	files, complete := s.collect(ctx, roots)
	s.update(func(st *Status) { st.Found = len(files) })

	sinceSave := 0
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		seen[f.path] = true
		cur := fileState{Size: f.size, ModTime: f.mod}
		if old, ok := state[f.path]; ok && old.Size == cur.Size && old.ModTime.Equal(cur.ModTime) {
			s.update(func(st *Status) { st.Done++; st.Unchanged++ })
			continue
		}
		n, err := s.Index(ctx, f.path)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if s.Fatal != nil {
				if reason := s.Fatal(err); reason != "" {
					s.update(func(st *Status) { st.Stopped = reason })
					break
				}
			}
			// Remember failures too, so a file that cannot be parsed is not
			// retried on every start; a change to it retries it.
			state[f.path] = cur
			s.update(func(st *Status) { st.Done++; st.Failed++ })
			continue
		}
		state[f.path] = cur
		s.update(func(st *Status) { st.Done++; st.Indexed++; st.Chunks += n })
		if sinceSave++; sinceSave >= saveEvery {
			s.saveState(state)
			sinceSave = 0
		}
	}

	// Forget files that are gone, including those in folders no longer
	// watched, but only after a complete walk: a stopped or cancelled scan
	// has not seen everything.
	if complete && ctx.Err() == nil && s.Status().Stopped == "" {
		for path := range state {
			if seen[path] {
				continue
			}
			if s.Forget != nil {
				if err := s.Forget(ctx, path); err != nil {
					continue // try again next scan
				}
			}
			delete(state, path)
			s.update(func(st *Status) { st.Removed++ })
		}
	}
	s.saveState(state)
	end := time.Now()
	s.update(func(st *Status) { st.Running, st.FinishedAt = false, &end })
	return s.Status()
}

type candidate struct {
	path string
	size int64
	mod  time.Time
}

// collect walks roots and returns the supported files, sorted by path so
// progress is reproducible, and whether the walk saw everything (it stops at
// MaxFiles). Unreadable directories are skipped silently.
func (s *Scanner) collect(ctx context.Context, roots []string) ([]candidate, bool) {
	maxBytes := s.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	maxFiles := s.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	var out []candidate
	complete := true
	seen := map[string]bool{}
	errLimit := errors.New("limit reached")
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && (skippedDirs[name] || packageExts[strings.ToLower(filepath.Ext(name))]) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !s.Supported(path) || seen[path] {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() == 0 || info.Size() > maxBytes {
				return nil
			}
			seen[path] = true
			out = append(out, candidate{path: path, size: info.Size(), mod: info.ModTime()})
			if len(out) >= maxFiles {
				return errLimit
			}
			return nil
		})
		if errors.Is(err, errLimit) {
			log.Printf("scan: stopped collecting at %d files", maxFiles)
			complete = false
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, complete
}

func (s *Scanner) update(f func(*Status)) {
	s.mu.Lock()
	f(&s.status)
	s.mu.Unlock()
}

func (s *Scanner) loadState() map[string]fileState {
	state := map[string]fileState{}
	data, err := os.ReadFile(s.StatePath)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("scan: ignoring unreadable state file: %v", err)
		return map[string]fileState{}
	}
	return state
}

func (s *Scanner) saveState(state map[string]fileState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	tmp := s.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("scan: save state: %v", err)
		return
	}
	if err := os.Rename(tmp, s.StatePath); err != nil {
		log.Printf("scan: save state: %v", err)
	}
}
