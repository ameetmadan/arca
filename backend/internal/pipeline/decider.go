package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

// Defaults for Decider.
const (
	// DefaultMaxPending is the number of pending decisions a watched folder
	// may have before the pipeline stops generating new ones for it. It is
	// configurable as decision.max_pending_per_folder.
	DefaultMaxPending = 50

	// DefaultSettleDelay is the gap between the two size checks that decide
	// whether a file is still being written.
	DefaultSettleDelay = 250 * time.Millisecond
)

// FactsSource gathers the Facts for one file; *FactsBuilder is the production
// implementation.
type FactsSource interface {
	Build(ctx context.Context, path string) (decision.Facts, error)
}

// DecisionQueue is the part of *decisions.Service the Decider uses.
type DecisionQueue interface {
	Submit(ctx context.Context, root, file string, d decision.Decision) (decisions.SubmitResult, error)
	CountPending(ctx context.Context, root, file string) (int, error)
	GetPolicy(ctx context.Context, root string) (decisions.PolicyView, error)
}

var _ DecisionQueue = (*decisions.Service)(nil)

// Decider is the pipeline stage that follows indexing: for a settled file it
// builds Facts, asks the decision engine what to do, and submits the answer to
// the decision queue, where the folder's policy applies it or queues it
// (ADR-0006). Handle is a Handler, so it plugs straight into Pipeline.
//
// Per event it:
//
//  1. runs Index (parse, embed, upsert) first, so search stays current;
//  2. skips files that are not eligible: outside every watched folder, hidden,
//     a partial download, a directory, or gone;
//  3. skips files Arca itself just moved or restored (see RecentMoves);
//  4. skips folders whose policy is off, files that already have a pending
//     decision, and folders with more than MaxPending pending decisions;
//  5. skips a file whose size or modification time changes during
//     SettleDelay; the write that changed it produces another event, which is
//     handled once the file has settled;
//  6. decides and submits. Only a Move decision whose target differs from the
//     file's current folder is worth submitting, the rest is up to the policy.
//
// A failure to index or to decide about one file is returned for the Pipeline
// to report; it never affects other files.
type Decider struct {
	// Index is the indexing stage; it may be nil.
	Index Handler
	// Facts builds the Facts of a file.
	Facts FactsSource
	// Engine is the decision chain (App.Decision).
	Engine decision.Engine
	// Queue receives the decisions.
	Queue DecisionQueue
	// Roots are the watched folders (clean, absolute).
	Roots []string
	// Recent is the memory of paths Arca just moved; it may be nil.
	Recent *RecentMoves

	// MaxPending is the backpressure threshold; zero means DefaultMaxPending.
	MaxPending int
	// SettleDelay is the gap between the two stability stats; zero means
	// DefaultSettleDelay, negative disables the check.
	SettleDelay time.Duration
	// Sleep waits for d or until ctx is done. It defaults to a timer and is
	// replaceable so tests need not wait.
	Sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	throttled map[string]bool // roots we already logged backpressure for
}

// Handle implements Handler.
func (d *Decider) Handle(ctx context.Context, ev Event) error {
	var indexErr error
	if d.Index != nil {
		indexErr = d.Index(ctx, ev)
	}
	if ctx.Err() != nil {
		return indexErr
	}
	return errors.Join(indexErr, d.decide(ctx, ev))
}

func (d *Decider) decide(ctx context.Context, ev Event) error {
	if ev.Op == Removed {
		return nil
	}
	path := filepath.Clean(ev.Path)
	if !eligibleName(path) {
		return nil
	}
	root, ok := rootOf(d.Roots, path)
	if !ok {
		return nil // not in a watched folder: indexed only
	}
	if d.Recent.Contains(path) {
		return nil // an echo of our own move
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return nil // gone again, or a directory
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil
	}

	pol, err := d.Queue.GetPolicy(ctx, root)
	if err != nil {
		return fmt.Errorf("read folder policy: %w", err)
	}
	if pol.Settings.Mode == policy.ModeOff {
		return nil
	}
	if n, err := d.Queue.CountPending(ctx, root, rel); err != nil {
		return fmt.Errorf("count pending decisions: %w", err)
	} else if n > 0 {
		return nil // the user already has a suggestion for this file
	}
	if full, err := d.folderFull(ctx, root); err != nil {
		return err
	} else if full {
		return nil
	}

	settled, err := d.settled(ctx, path)
	if err != nil || !settled {
		return err
	}

	facts, err := d.Facts.Build(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("build facts: %w", err)
	}
	dec, err := d.Engine.Decide(ctx, facts)
	if err != nil {
		return fmt.Errorf("decide: %w", err)
	}
	if dec.Action == decision.Move && sameFolder(root, dec.Target, path) {
		return nil // already where the engine would put it
	}
	// Re-check: a move of this very file may have finished while we decided.
	if d.Recent.Contains(path) {
		return nil
	}
	if _, err := d.Queue.Submit(ctx, root, rel, dec); err != nil {
		return fmt.Errorf("submit decision: %w", err)
	}
	return nil
}

// folderFull reports whether root has more pending decisions than MaxPending.
// It logs once when a folder becomes full and once when it frees up again.
func (d *Decider) folderFull(ctx context.Context, root string) (bool, error) {
	limit := d.MaxPending
	if limit <= 0 {
		limit = DefaultMaxPending
	}
	n, err := d.Queue.CountPending(ctx, root, "")
	if err != nil {
		return false, fmt.Errorf("count pending decisions: %w", err)
	}
	full := n > limit

	d.mu.Lock()
	defer d.mu.Unlock()
	if full && !d.throttled[root] {
		log.Printf("pipeline: %s has %d pending decisions (limit %d); not suggesting more until some are accepted or rejected", root, n, limit)
	} else if !full && d.throttled[root] {
		log.Printf("pipeline: %s is below its pending-decision limit again; suggestions resume", root)
	}
	if d.throttled == nil {
		d.throttled = make(map[string]bool)
	}
	d.throttled[root] = full
	return full, nil
}

// settled stats path twice, SettleDelay apart, and reports whether size and
// modification time stayed the same. A file that vanished counts as not
// settled without an error.
func (d *Decider) settled(ctx context.Context, path string) (bool, error) {
	delay := d.SettleDelay
	if delay < 0 {
		return true, nil
	}
	if delay == 0 {
		delay = DefaultSettleDelay
	}
	first, err := os.Stat(path)
	if err != nil {
		return false, ignoreNotExist(err)
	}
	sleep := d.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	if err := sleep(ctx, delay); err != nil {
		return false, nil // shutting down
	}
	second, err := os.Stat(path)
	if err != nil {
		return false, ignoreNotExist(err)
	}
	return first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ignoreNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// partialSuffixes are extensions browsers and download tools use for files
// that are still arriving.
var partialSuffixes = []string{".crdownload", ".part", ".partial", ".download", ".tmp"}

// eligibleName reports whether the file name is one worth deciding about:
// not hidden and not a partial download.
func eligibleName(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false
	}
	lower := strings.ToLower(base)
	for _, s := range partialSuffixes {
		if strings.HasSuffix(lower, s) {
			return false
		}
	}
	return true
}

// sameFolder reports whether target (relative to root) is the folder path is
// already in.
func sameFolder(root, target, path string) bool {
	if target == "" || filepath.IsAbs(target) {
		return false
	}
	return filepath.Join(root, target) == filepath.Dir(path)
}

// rootOf returns the root that contains path.
func rootOf(roots []string, path string) (string, bool) {
	for _, r := range roots {
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return r, true
		}
	}
	return "", false
}
