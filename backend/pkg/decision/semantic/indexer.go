package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ameetmadan/arca/pkg/embed"
)

// SourceFile describes one file directly inside a folder.
type SourceFile struct {
	// Name is the file name without any directory.
	Name string
	// Size is the file size in bytes.
	Size int64
	// ModTime is the last modification time.
	ModTime time.Time
}

// Source is how the Indexer sees the user's folders. It exists so the indexing
// logic can be tested without a filesystem; OSSource is the real one.
//
// All paths are relative to the root and slash-separated. Implementations
// must not follow symlinks out of the root and must not report hidden
// folders or files.
type Source interface {
	// Folders lists every sub-folder below root, at any depth, excluding root
	// itself and hidden folders.
	Folders(ctx context.Context, root string) ([]string, error)
	// Files lists the files directly inside root/rel (not recursively).
	// A folder that no longer exists yields an error wrapping fs.ErrNotExist.
	Files(ctx context.Context, root, rel string) ([]SourceFile, error)
	// Text returns the text that represents a file for embedding. It is file
	// content: callers must not log it. An empty string means "nothing to embed".
	Text(ctx context.Context, root, rel, name string) (string, error)
}

// Progress is a snapshot of an indexing run.
type Progress struct {
	// Root is the root being indexed.
	Root string
	// FoldersTotal is the number of folders found under Root.
	FoldersTotal int
	// FoldersDone counts folders handled so far, including skipped ones.
	FoldersDone int
	// FoldersSkipped counts folders that were unchanged since the last run.
	FoldersSkipped int
	// FilesEmbedded counts files that contributed a vector.
	FilesEmbedded int
	// FilesSkipped counts sampled files with no text or no usable embedding.
	FilesSkipped int
	// Finished is true once the run completed (even if some folders failed).
	// It stays false if the run was cancelled.
	Finished bool
}

// Indexer builds the per-folder centroids and examples the Engine reads.
// It is safe for concurrent use.
type Indexer struct {
	store    FolderStore
	embedder embed.EmbeddingProvider
	source   Source
	t        Tunables
}

// NewIndexer returns an Indexer. It returns an error if a dependency is nil
// or the tunables are invalid. The provider choice is the caller's (ADR-0004).
func NewIndexer(store FolderStore, embedder embed.EmbeddingProvider, source Source, t Tunables) (*Indexer, error) {
	switch {
	case store == nil:
		return nil, errors.New("semantic: nil FolderStore")
	case embedder == nil:
		return nil, errors.New("semantic: nil EmbeddingProvider")
	case source == nil:
		return nil, errors.New("semantic: nil Source")
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &Indexer{store: store, embedder: embedder, source: source, t: t}, nil
}

// IndexRoot indexes every sub-folder of root and removes stored folders that
// no longer exist. Folders whose contents have not changed since the last run
// are skipped, so calling it again is cheap. report, if not nil, is called
// after each folder with the current progress.
//
// A folder that fails is skipped and the run continues; the errors are
// returned joined once the run ends. If ctx is cancelled it stops promptly and
// returns ctx.Err(); folders finished before that stay indexed.
func (ix *Indexer) IndexRoot(ctx context.Context, root string, report func(Progress)) error {
	if err := validateRoot(root); err != nil {
		return err
	}
	rels, err := ix.source.Folders(ctx, root)
	if err != nil {
		return fmt.Errorf("semantic: list folders: %w", err)
	}

	p := Progress{Root: root, FoldersTotal: len(rels)}
	emit := func() {
		if report != nil {
			report(p)
		}
	}
	emit()

	var errs []error
	for _, rel := range rels {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := ix.indexFolder(ctx, root, rel)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("folder %q: %w", rel, err))
		}
		p.FoldersDone++
		if res.unchanged {
			p.FoldersSkipped++
		}
		p.FilesEmbedded += res.embedded
		p.FilesSkipped += res.skipped
		emit()
	}

	if err := ix.prune(ctx, root, rels); err != nil {
		return err
	}
	p.Finished = true
	emit()
	return errors.Join(errs...)
}

// IndexFolder re-indexes one folder of root, for use when a file watcher
// reports that it changed. It reports whether the stored index was rewritten;
// it is false when the folder was unchanged or is empty. An emptied or
// vanished folder is removed from the store.
func (ix *Indexer) IndexFolder(ctx context.Context, root, rel string) (bool, error) {
	if err := validateRoot(root); err != nil {
		return false, err
	}
	if err := validateRel(rel); err != nil {
		return false, err
	}
	res, err := ix.indexFolder(ctx, root, rel)
	return res.rewritten, err
}

type folderResult struct {
	unchanged, rewritten bool
	embedded, skipped    int
}

func (ix *Indexer) indexFolder(ctx context.Context, root, rel string) (folderResult, error) {
	var res folderResult
	if err := validateRel(rel); err != nil {
		return res, err
	}

	files, err := ix.source.Files(ctx, root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return res, ix.store.Delete(ctx, root, rel)
	}
	if err != nil {
		return res, fmt.Errorf("list files: %w", err)
	}
	if len(files) == 0 {
		return res, ix.store.Delete(ctx, root, rel)
	}

	fp := ix.fingerprint(files)
	if prev, ok, err := ix.store.Get(ctx, root, rel); err != nil {
		return res, err
	} else if ok && prev.Fingerprint == fp {
		res.unchanged = true
		return res, nil
	}

	var (
		vecs     [][]float32
		examples []Example
		embedErr error
	)
	for _, f := range sample(files, ix.t.SampleSize, ix.t.Seed, rel) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		v, err := ix.embedFile(ctx, root, rel, f.Name)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return res, ctxErr
			}
			embedErr = err
		}
		if v != nil && len(vecs) > 0 && len(v) != len(vecs[0]) {
			v = nil // produced by a different model; cannot be averaged
		}
		if v == nil {
			res.skipped++
			continue
		}
		vecs = append(vecs, v)
		examples = append(examples, Example{Name: f.Name, Vector: v})
		res.embedded++
	}

	if len(vecs) == 0 && embedErr != nil {
		// Do not remember a failed attempt as "this folder has no usable
		// files": that would stop it ever being retried until its files change.
		return res, fmt.Errorf("embed: %w", embedErr)
	}

	idx := FolderIndex{Root: root, Rel: rel, FileCount: len(files), Fingerprint: fp, Examples: examples}
	if c, ok := centroidOf(vecs); ok {
		idx.Centroid = c
	} else {
		idx.Examples = nil
	}
	if err := ix.store.Put(ctx, idx); err != nil {
		return res, err
	}
	res.rewritten = true
	return res, nil
}

// embedFile returns the unit vector for one file, or nil if there is nothing
// usable. The returned error describes an embedder or source failure and, like
// all errors here, never contains file contents.
func (ix *Indexer) embedFile(ctx context.Context, root, rel, name string) ([]float32, error) {
	text, err := ix.source.Text(ctx, root, rel, name)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil, nil // unreadable or empty files are simply not evidence
	}
	v, err := ix.embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	n, ok := normalize(v)
	if !ok {
		return nil, nil
	}
	return n, nil
}

// prune deletes stored folders of root that are no longer on disk.
func (ix *Indexer) prune(ctx context.Context, root string, current []string) error {
	keep := make(map[string]struct{}, len(current))
	for _, rel := range current {
		keep[rel] = struct{}{}
	}
	stored, err := ix.store.List(ctx)
	if err != nil {
		return err
	}
	for _, f := range stored {
		if f.Root != root {
			continue
		}
		if _, ok := keep[f.Rel]; !ok {
			if err := ix.store.Delete(ctx, root, f.Rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// fingerprint identifies a folder's contents together with the tunables that
// shape its index, so changing either triggers a re-index.
func (ix *Indexer) fingerprint(files []SourceFile) string {
	sorted := append([]SourceFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	fmt.Fprintf(h, "sample=%d seed=%d\n", ix.t.SampleSize, ix.t.Seed)
	for _, f := range sorted {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", f.Name, f.Size, f.ModTime.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sample picks at most n files. The result depends only on the set of file
// names, the seed and the folder path, never on the order files were listed in
// or on how many times it is called, and it is returned sorted by name.
func sample(files []SourceFile, n int, seed uint64, rel string) []SourceFile {
	sorted := append([]SourceFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	if len(sorted) <= n {
		return sorted
	}
	h := fnv.New64a()
	h.Write([]byte(rel))
	rng := rand.New(rand.NewPCG(seed, h.Sum64()))
	picked := make([]SourceFile, 0, n)
	for _, i := range rng.Perm(len(sorted))[:n] {
		picked = append(picked, sorted[i])
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Name < picked[j].Name })
	return picked
}

func validateRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("semantic: root %q must be a clean absolute path", root)
	}
	return nil
}

// validateRel rejects anything that is not a visible folder strictly below the
// root: the root itself, parent references, absolute paths and hidden folders.
func validateRel(rel string) error {
	local := filepath.FromSlash(rel)
	if rel == "" || rel == "." || !filepath.IsLocal(local) {
		return fmt.Errorf("semantic: folder %q must be a relative path below the root", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("semantic: folder %q is hidden or not clean", rel)
		}
	}
	return nil
}

// Job is a background indexing run started by Indexer.Start.
type Job struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	progress Progress
	err      error
}

// Start indexes root in a new goroutine and returns immediately. The run
// stops when ctx is cancelled or Job.Cancel is called. Decide never waits for
// it: it uses whatever has been stored so far.
func (ix *Indexer) Start(ctx context.Context, root string) *Job {
	ctx, cancel := context.WithCancel(ctx)
	j := &Job{cancel: cancel, done: make(chan struct{}), progress: Progress{Root: root}}
	go func() {
		defer close(j.done)
		defer cancel()
		err := ix.IndexRoot(ctx, root, func(p Progress) {
			j.mu.Lock()
			j.progress = p
			j.mu.Unlock()
		})
		j.mu.Lock()
		j.err = err
		j.mu.Unlock()
	}()
	return j
}

// Progress returns the latest progress snapshot.
func (j *Job) Progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.progress
}

// Done is closed when the run has ended, whether it finished, failed or was cancelled.
func (j *Job) Done() <-chan struct{} { return j.done }

// Wait blocks until the run has ended and returns its error: nil, the joined
// per-folder errors, or the context error if it was cancelled.
func (j *Job) Wait() error {
	<-j.done
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// Cancel asks the run to stop. It is safe to call more than once.
func (j *Job) Cancel() { j.cancel() }
