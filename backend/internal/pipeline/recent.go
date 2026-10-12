package pipeline

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/pkg/decision"
)

// DefaultRecentMoveWindow is how long a path stays "recently moved by Arca".
// It must outlast the pipeline debounce and the settle check combined.
const DefaultRecentMoveWindow = time.Minute

// maxRememberedEntries bounds the journal entries kept so an undo can be
// recognised later than the window.
const maxRememberedEntries = 1024

// RecentMoves remembers the paths Arca itself has just moved, so that the file
// system events those moves cause are not mistaken for new files and decided
// on again (a feedback loop: move, event, decide, move, ...).
//
// It is safe for concurrent use. A nil *RecentMoves remembers nothing.
//
// Limits: the memory is per process. An undo of a move made before the last
// restart is not recognised, so the restored file may be suggested again.
type RecentMoves struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	paths   map[string]time.Time // normalised path -> expiry
	entries map[int64]executor.JournalEntry
	order   []int64 // entry ids in insertion order, oldest first
}

// NewRecentMoves returns a RecentMoves with the given window; a non-positive
// ttl selects DefaultRecentMoveWindow.
func NewRecentMoves(ttl time.Duration) *RecentMoves {
	if ttl <= 0 {
		ttl = DefaultRecentMoveWindow
	}
	return &RecentMoves{
		ttl:     ttl,
		now:     time.Now,
		paths:   make(map[string]time.Time),
		entries: make(map[int64]executor.JournalEntry),
	}
}

// Record remembers paths for the window.
func (r *RecentMoves) Record(paths ...string) {
	if r == nil {
		return
	}
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			keys = append(keys, normalize(p))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.prune(now)
	for _, k := range keys {
		r.paths[k] = now.Add(r.ttl)
	}
}

// Contains reports whether path was recorded within the window. The folder
// part is compared with symlinks resolved, because the Executor reports
// resolved paths while the watcher reports paths as configured.
func (r *RecentMoves) Contains(path string) bool {
	if r == nil || path == "" {
		return false
	}
	key := normalize(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(r.now())
	_, ok := r.paths[key]
	return ok
}

func (r *RecentMoves) prune(now time.Time) {
	for p, exp := range r.paths {
		if !now.Before(exp) {
			delete(r.paths, p)
		}
	}
}

// normalize cleans p and resolves symlinks in its directory (the file itself
// may not exist any more). If the directory cannot be resolved, the cleaned
// path is used.
func normalize(p string) string {
	p = filepath.Clean(p)
	dir, base := filepath.Split(p)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, base)
	}
	return p
}

// Wrap returns a decisions.FileExecutor that behaves exactly like exec and
// additionally records every successful move (both ends) and undo (the
// restored location).
func (r *RecentMoves) Wrap(exec decisions.FileExecutor) decisions.FileExecutor {
	return &recordingExecutor{FileExecutor: exec, recent: r}
}

type recordingExecutor struct {
	decisions.FileExecutor
	recent *RecentMoves
}

func (e *recordingExecutor) Execute(ctx context.Context, root string, d decision.Decision, file string) (executor.JournalEntry, error) {
	entry, err := e.FileExecutor.Execute(ctx, root, d, file)
	if err == nil {
		e.recent.remember(entry)
	}
	return entry, err
}

func (e *recordingExecutor) Undo(ctx context.Context, id int64) error {
	err := e.FileExecutor.Undo(ctx, id)
	if err == nil {
		e.recent.recordUndo(id)
	}
	return err
}

// remember records a move and keeps its entry so a later undo can find the paths.
func (r *RecentMoves) remember(entry executor.JournalEntry) {
	if r == nil {
		return
	}
	r.Record(entry.Src, entry.Dst)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[entry.ID]; !ok {
		r.order = append(r.order, entry.ID)
	}
	r.entries[entry.ID] = entry
	for len(r.order) > maxRememberedEntries {
		delete(r.entries, r.order[0])
		r.order = r.order[1:]
	}
}

func (r *RecentMoves) recordUndo(id int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	entry, ok := r.entries[id]
	r.mu.Unlock()
	if ok {
		r.Record(entry.Src, entry.Dst)
	}
}
