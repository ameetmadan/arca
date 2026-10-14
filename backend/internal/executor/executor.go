// Package executor is the only code allowed to move or modify user files.
//
// Every action is checked to stay inside a caller-supplied root folder
// (symlinks resolved, then verified with filepath.Rel), never overwrites an
// existing file, and is recorded in a Journal so it can be undone. See
// docs/adr/0006-decision-engine-chain-with-policy-and-executor.md.
//
// The checks give early, descriptive errors, but they are not what keeps files
// inside the root: another process could swap a directory for a symlink
// between a check and the move. So the moves themselves, and the directory
// creation, go through an [os.Root] opened on the root folder, which refuses
// to follow any path out of it at the moment of the operation. An os.Root
// cannot rename across roots; that is fine here because a move always stays
// inside one root, and no copy-and-delete fallback is attempted when the
// rename itself fails (for example when a bind mount inside the root makes it
// cross devices): the error is returned and nothing changes.
//
// The package never reads or logs file contents; only paths are handled.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

var (
	// ErrOutsideRoot means a source or destination resolves outside the root.
	ErrOutsideRoot = errors.New("executor: path escapes the root folder")
	// ErrInvalidRoot means the root is not an absolute path to an existing directory.
	ErrInvalidRoot = errors.New("executor: invalid root folder")
	// ErrInvalidFile means the file argument is empty.
	ErrInvalidFile = errors.New("executor: invalid file")
	// ErrInvalidTarget means a Move decision has an empty target.
	ErrInvalidTarget = errors.New("executor: invalid target")
	// ErrUnsupportedAction means the decision's action is not one the Executor performs.
	ErrUnsupportedAction = errors.New("executor: unsupported action")
	// ErrNotRegularFile means the source is not a regular file (e.g. a directory).
	ErrNotRegularFile = errors.New("executor: source is not a regular file")
	// ErrSymlink means the source is a symlink, which is never moved.
	ErrSymlink = errors.New("executor: source is a symlink")
	// ErrAlreadyInPlace means the file already lives in the target folder.
	ErrAlreadyInPlace = errors.New("executor: file is already in the target folder")
	// ErrAlreadyUndone means the journal entry was undone before.
	ErrAlreadyUndone = errors.New("executor: action was already undone")
	// ErrUndoDestinationMissing means the moved file is gone, so there is nothing to move back.
	ErrUndoDestinationMissing = errors.New("executor: cannot undo, the moved file no longer exists")
	// ErrUndoSourceOccupied means the original location is taken by another file.
	ErrUndoSourceOccupied = errors.New("executor: cannot undo, the original location is occupied")
)

// maxCollisionAttempts bounds the "name (N).ext" search.
const maxCollisionAttempts = 10000

// Executor performs file actions safely and journals them. It is safe for
// concurrent use; file operations are serialised.
type Executor struct {
	journal Journal
	now     func() time.Time

	mu sync.Mutex

	// beforeMove, if set, runs after the paths were checked and before the
	// first file system change. Tests use it to simulate another process
	// racing the Executor.
	beforeMove func()
}

// New returns an Executor that records actions in j.
func New(j Journal) *Executor {
	return &Executor{journal: j, now: func() time.Time { return time.Now().UTC() }}
}

// Execute carries out decision d for file, which is a path relative to root.
// Only decision.Move is supported: the file is moved into the folder d.Target
// (relative to root, created if needed). An existing file is never overwritten;
// on a name clash the moved file becomes "name (1).ext", "name (2).ext", ...
//
// It returns the journal entry describing what was done. Nothing is changed
// when an error is returned.
func (e *Executor) Execute(ctx context.Context, root string, d decision.Decision, file string) (JournalEntry, error) {
	if err := ctx.Err(); err != nil {
		return JournalEntry{}, err
	}
	if d.Action != decision.Move {
		return JournalEntry{}, fmt.Errorf("%w: %q", ErrUnsupportedAction, d.Action)
	}
	if file == "" {
		return JournalEntry{}, ErrInvalidFile
	}
	if d.Target == "" {
		return JournalEntry{}, ErrInvalidTarget
	}
	if filepath.IsAbs(file) {
		return JournalEntry{}, fmt.Errorf("%w: file %q is absolute", ErrOutsideRoot, file)
	}
	if filepath.IsAbs(d.Target) {
		return JournalEntry{}, fmt.Errorf("%w: target %q is absolute", ErrOutsideRoot, d.Target)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	rootReal, err := resolveRoot(root)
	if err != nil {
		return JournalEntry{}, err
	}

	src, err := resolveSource(rootReal, file)
	if err != nil {
		return JournalEntry{}, err
	}

	rfs, err := os.OpenRoot(rootReal)
	if err != nil {
		return JournalEntry{}, fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	defer rfs.Close()

	dstDir, err := prepareTargetDir(rfs, rootReal, d.Target)
	if err != nil {
		return JournalEntry{}, err
	}
	if dstDir == filepath.Dir(src) {
		return JournalEntry{}, ErrAlreadyInPlace
	}
	if e.beforeMove != nil {
		e.beforeMove()
	}

	dst, err := moveWithoutClobber(rfs, rootReal, src, dstDir, filepath.Base(src))
	if err != nil {
		return JournalEntry{}, mapRootError(err)
	}

	entry, err := e.journal.Append(ctx, JournalEntry{
		TS:             e.now(),
		Root:           rootReal,
		Action:         d.Action,
		Src:            src,
		Dst:            dst,
		DecisionSource: d.Source,
		Reason:         d.Reason,
	})
	if err != nil {
		// An unjournalled move cannot be undone, so put the file back.
		if rbErr := moveExact(rfs, rootReal, dst, src); rbErr != nil {
			return JournalEntry{}, fmt.Errorf("journal append failed and rollback failed (file is at %s): %w", dst, errors.Join(err, rbErr))
		}
		return JournalEntry{}, fmt.Errorf("journal append: %w", err)
	}
	return entry, nil
}

// Undo moves the file of journal entry id back to where it came from. It
// refuses (with a descriptive error, changing nothing) if the entry was already
// undone, the moved file no longer exists, or the original location is
// occupied.
func (e *Executor) Undo(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, err := e.journal.Get(ctx, id)
	if err != nil {
		return err
	}
	if entry.UndoneAt != nil {
		return ErrAlreadyUndone
	}
	if entry.Action != decision.Move {
		return fmt.Errorf("%w: %q", ErrUnsupportedAction, entry.Action)
	}

	// The journal is data on disk, so re-verify containment instead of trusting it.
	rootReal, err := resolveRoot(entry.Root)
	if err != nil {
		return err
	}
	src, err := resolveInside(rootReal, entry.Src)
	if err != nil {
		return err
	}
	dst, err := resolveInside(rootReal, entry.Dst)
	if err != nil {
		return err
	}

	rfs, err := os.OpenRoot(rootReal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	defer rfs.Close()
	srcRel, err := relInside(rootReal, src)
	if err != nil {
		return err
	}
	dstRel, err := relInside(rootReal, dst)
	if err != nil {
		return err
	}

	if e.beforeMove != nil {
		e.beforeMove()
	}

	if _, err := rfs.Lstat(dstRel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrUndoDestinationMissing
		}
		return mapRootError(err)
	}
	if _, err := rfs.Lstat(srcRel); err == nil {
		return ErrUndoSourceOccupied
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapRootError(err)
	}

	if err := rfs.MkdirAll(filepath.Dir(srcRel), 0o755); err != nil {
		return mapRootError(err)
	}
	if err := moveExact(rfs, rootReal, dst, src); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrUndoSourceOccupied
		}
		return mapRootError(err)
	}
	if err := e.journal.MarkUndone(ctx, id, e.now()); err != nil {
		return fmt.Errorf("file restored but journal update failed: %w", err)
	}
	return nil
}

// resolveRoot returns the symlink-resolved absolute path of root.
func resolveRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: must be an absolute path", ErrInvalidRoot)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: not a directory", ErrInvalidRoot)
	}
	return real, nil
}

// within reports whether p is root or lies below it, using filepath.Rel.
// Both must be absolute and clean.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveInside resolves symlinks in the existing part of the absolute path p
// and verifies the result is inside rootReal. The tail of p may not exist yet.
func resolveInside(rootReal, p string) (string, error) {
	cur := filepath.Clean(p)
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%w: no existing ancestor", ErrOutsideRoot)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	for i := len(tail) - 1; i >= 0; i-- {
		real = filepath.Join(real, tail[i])
	}
	if !within(rootReal, real) {
		return "", ErrOutsideRoot
	}
	return real, nil
}

// resolveSource validates file (relative to rootReal) and returns its real
// absolute path. Symlinks are never followed or moved.
func resolveSource(rootReal, file string) (string, error) {
	lexical := filepath.Join(rootReal, file) // Join cleans, collapsing ".."
	if !within(rootReal, lexical) {
		return "", fmt.Errorf("%w: file %q", ErrOutsideRoot, file)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(lexical))
	if err != nil {
		return "", err
	}
	if !within(rootReal, parent) {
		return "", fmt.Errorf("%w: file %q", ErrOutsideRoot, file)
	}
	src := filepath.Join(parent, filepath.Base(lexical))

	fi, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if target, err := filepath.EvalSymlinks(src); err != nil || !within(rootReal, target) {
			return "", fmt.Errorf("%w: file %q is a symlink to outside the root", ErrOutsideRoot, file)
		}
		return "", fmt.Errorf("%w: %q", ErrSymlink, file)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q", ErrNotRegularFile, file)
	}
	return src, nil
}

// relInside returns p, an absolute path inside rootReal, relative to it, which
// is the form an [os.Root] takes. "." means the root itself.
func relInside(rootReal, p string) (string, error) {
	if !within(rootReal, p) {
		return "", ErrOutsideRoot
	}
	return filepath.Rel(rootReal, p)
}

// mapRootError turns the error an [os.Root] gives for a path that leaves the
// root into ErrOutsideRoot, so callers see one error for "escapes the root"
// whether it was caught by the early checks or at the moment of the operation.
func mapRootError(err error) error {
	if err != nil && strings.Contains(err.Error(), "path escapes from parent") {
		return fmt.Errorf("%w: %v", ErrOutsideRoot, err)
	}
	return err
}

// prepareTargetDir validates target (relative to rootReal), creates the
// directory if needed and returns its real absolute path.
func prepareTargetDir(rfs *os.Root, rootReal, target string) (string, error) {
	lexical := filepath.Join(rootReal, target)
	if !within(rootReal, lexical) {
		return "", fmt.Errorf("%w: target %q", ErrOutsideRoot, target)
	}
	// Verify before creating anything, so a symlinked component that leads out
	// of the root cannot cause directories to be made outside it.
	if _, err := resolveInside(rootReal, lexical); err != nil {
		return "", fmt.Errorf("target %q: %w", target, err)
	}
	rel, err := relInside(rootReal, lexical)
	if err != nil {
		return "", fmt.Errorf("target %q: %w", target, err)
	}
	if err := rfs.MkdirAll(rel, 0o755); err != nil {
		return "", fmt.Errorf("create target folder: %w", mapRootError(err))
	}
	real, err := resolveInside(rootReal, lexical)
	if err != nil {
		return "", fmt.Errorf("target %q: %w", target, err)
	}
	return real, nil
}

// moveWithoutClobber moves src into dir under name, or under "stem (N).ext"
// when that name is taken, and returns the final path. The destination name is
// claimed with O_EXCL before renaming, so a file that appears concurrently is
// never replaced.
func moveWithoutClobber(rfs *os.Root, rootReal, src, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // dotfile such as ".env": treat the whole name as the stem
		stem, ext = name, ""
	}
	for i := 0; i < maxCollisionAttempts; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		dst := filepath.Join(dir, candidate)
		err := moveExact(rfs, rootReal, src, dst)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", fmt.Errorf("no free name for %q after %d attempts", name, maxCollisionAttempts)
}

// moveExact renames src to dst (absolute paths inside rootReal), failing with
// fs.ErrExist instead of replacing an existing dst. Rename alone would
// silently overwrite on POSIX, so dst is first claimed with an exclusive
// create. Both steps go through rfs, so neither can leave the root even if a
// directory on the way is swapped for a symlink in the meantime.
func moveExact(rfs *os.Root, rootReal, src, dst string) error {
	srcRel, err := relInside(rootReal, src)
	if err != nil {
		return err
	}
	dstRel, err := relInside(rootReal, dst)
	if err != nil {
		return err
	}
	f, err := rfs.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		rfs.Remove(dstRel) //nolint:errcheck
		return err
	}
	if err := rfs.Rename(srcRel, dstRel); err != nil {
		rfs.Remove(dstRel) //nolint:errcheck
		return err
	}
	return nil
}
