package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
)

func mv(target string) decision.Decision {
	return decision.Decision{Action: decision.Move, Target: target, Confidence: 1, Reason: "test", Source: "rules"}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// setup returns an Executor, its journal, and an empty root directory that
// lives inside a parent so tests can check nothing escapes into the parent.
func setup(t *testing.T) (*Executor, *memJournal, string, string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	j := newMemJournal()
	return New(j), j, root, parent
}

func TestExecute_MovesFileAndCreatesTargetDir(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "hello")

	entry, err := ex.Execute(context.Background(), root, mv("Docs/Notes"), "a.txt")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	dst := filepath.Join(root, "Docs", "Notes", "a.txt")
	if got := read(t, dst); got != "hello" {
		t.Fatalf("dst content = %q", got)
	}
	if exists(filepath.Join(root, "a.txt")) {
		t.Fatal("source still exists")
	}
	if entry.ID == 0 || entry.Action != decision.Move || entry.DecisionSource != "rules" || entry.Reason != "test" {
		t.Fatalf("bad journal entry: %+v", entry)
	}
	if got, _ := j.Get(context.Background(), entry.ID); got.Dst == "" || got.Src == "" || got.Root == "" || got.TS.IsZero() {
		t.Fatalf("journal not populated: %+v", got)
	}
}

func TestExecute_RejectsEscapes(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		target string
	}{
		{"file escapes with dotdot", "../../etc/x", "Docs"},
		{"file escapes one level", "../outside.txt", "Docs"},
		{"target escapes with dotdot", "a.txt", "../escape"},
		{"target escapes via nested dotdot", "a.txt", "Docs/../../escape"},
		{"absolute target", "a.txt", "ABS"},
		{"absolute file", "ABS", "Docs"},
		{"empty target", "a.txt", ""},
		{"empty file", "", "Docs"},
		{"file is root", ".", "Docs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex, j, root, parent := setup(t)
			write(t, filepath.Join(root, "a.txt"), "inside")
			write(t, filepath.Join(parent, "outside.txt"), "outside")

			file, target := tc.file, tc.target
			if file == "ABS" {
				file = filepath.Join(parent, "outside.txt")
			}
			if target == "ABS" {
				target = filepath.Join(parent, "abs-target")
			}
			_, err := ex.Execute(context.Background(), root, mv(target), file)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := read(t, filepath.Join(root, "a.txt")); got != "inside" {
				t.Fatalf("inside file changed: %q", got)
			}
			if got := read(t, filepath.Join(parent, "outside.txt")); got != "outside" {
				t.Fatalf("outside file changed: %q", got)
			}
			for _, p := range []string{"escape", "abs-target"} {
				if exists(filepath.Join(parent, p)) {
					t.Fatalf("%s was created outside root", p)
				}
			}
			if len(j.entries) != 0 {
				t.Fatalf("journal has entries after a rejected move: %v", j.entries)
			}
		})
	}
}

func TestExecute_RejectsDotDotWithSentinel(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "x")
	if _, err := ex.Execute(context.Background(), root, mv("../escape"), "a.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("target err = %v, want ErrOutsideRoot", err)
	}
	if _, err := ex.Execute(context.Background(), root, mv("Docs"), "../../etc/x"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("file err = %v, want ErrOutsideRoot", err)
	}
}

func TestExecute_RejectsSymlinkedSourceOutsideRoot(t *testing.T) {
	ex, _, root, parent := setup(t)
	write(t, filepath.Join(parent, "secret.txt"), "secret")
	if err := os.Symlink(filepath.Join(parent, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	_, err := ex.Execute(context.Background(), root, mv("Docs"), "link.txt")
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if got := read(t, filepath.Join(parent, "secret.txt")); got != "secret" {
		t.Fatal("outside file was touched")
	}
	if exists(filepath.Join(root, "Docs")) {
		t.Fatal("target dir was created for a rejected move")
	}
}

func TestExecute_RejectsSymlinkedSourceDirectoryOutsideRoot(t *testing.T) {
	ex, _, root, parent := setup(t)
	write(t, filepath.Join(parent, "elsewhere", "f.txt"), "secret")
	if err := os.Symlink(filepath.Join(parent, "elsewhere"), filepath.Join(root, "dir")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	_, err := ex.Execute(context.Background(), root, mv("Docs"), "dir/f.txt")
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if got := read(t, filepath.Join(parent, "elsewhere", "f.txt")); got != "secret" {
		t.Fatal("outside file was touched")
	}
}

func TestExecute_RejectsSymlinkedTargetDirOutsideRoot(t *testing.T) {
	ex, _, root, parent := setup(t)
	write(t, filepath.Join(root, "a.txt"), "inside")
	if err := os.Mkdir(filepath.Join(parent, "outdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outdir"), filepath.Join(root, "Docs")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	for _, target := range []string{"Docs", "Docs/sub"} {
		_, err := ex.Execute(context.Background(), root, mv(target), "a.txt")
		if !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("target %q: err = %v, want ErrOutsideRoot", target, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(parent, "outdir"))
	if len(entries) != 0 {
		t.Fatalf("something was written outside root: %v", entries)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "inside" {
		t.Fatal("source was moved")
	}
}

func TestExecute_RejectsSymlinkInsideRoot(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "real.txt"), "x")
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := ex.Execute(context.Background(), root, mv("Docs"), "link.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

func TestExecute_FollowsRootSymlink(t *testing.T) {
	ex, _, root, parent := setup(t)
	write(t, filepath.Join(root, "a.txt"), "x")
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := ex.Execute(context.Background(), alias, mv("Docs"), "a.txt"); err != nil {
		t.Fatalf("Execute via symlinked root: %v", err)
	}
	if got := read(t, filepath.Join(root, "Docs", "a.txt")); got != "x" {
		t.Fatalf("content = %q", got)
	}
}

func TestExecute_InvalidInput(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "x")
	ctx := context.Background()

	if _, err := ex.Execute(ctx, "relative/root", mv("Docs"), "a.txt"); !errors.Is(err, ErrInvalidRoot) {
		t.Errorf("relative root: %v", err)
	}
	if _, err := ex.Execute(ctx, filepath.Join(root, "nope"), mv("Docs"), "a.txt"); !errors.Is(err, ErrInvalidRoot) {
		t.Errorf("missing root: %v", err)
	}
	if _, err := ex.Execute(ctx, filepath.Join(root, "a.txt"), mv("Docs"), "a.txt"); !errors.Is(err, ErrInvalidRoot) {
		t.Errorf("file as root: %v", err)
	}
	for _, a := range []decision.Action{decision.Tag, decision.Index, decision.Ignore, decision.AskUser, ""} {
		d := decision.Decision{Action: a, Target: "Docs"}
		if _, err := ex.Execute(ctx, root, d, "a.txt"); !errors.Is(err, ErrUnsupportedAction) {
			t.Errorf("action %q: %v", a, err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ex.Execute(cctx, root, mv("Docs"), "a.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: %v", err)
	}
	if !exists(filepath.Join(root, "a.txt")) {
		t.Fatal("source moved despite invalid input")
	}
}

func TestExecute_SourceMissingAndDirectory(t *testing.T) {
	ex, _, root, _ := setup(t)
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Execute(context.Background(), root, mv("Docs"), "gone.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := ex.Execute(context.Background(), root, mv("Docs"), "sub"); !errors.Is(err, ErrNotRegularFile) {
		t.Errorf("directory: %v", err)
	}
}

func TestExecute_AlreadyInPlace(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "Docs", "a.txt"), "x")
	_, err := ex.Execute(context.Background(), root, mv("Docs"), "Docs/a.txt")
	if !errors.Is(err, ErrAlreadyInPlace) {
		t.Fatalf("err = %v, want ErrAlreadyInPlace", err)
	}
	if got := read(t, filepath.Join(root, "Docs", "a.txt")); got != "x" || len(j.entries) != 0 {
		t.Fatal("file was renamed or journalled")
	}
}

func TestExecute_CollisionNeverOverwrites(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "a", "report.pdf"), "first")
	write(t, filepath.Join(root, "b", "report.pdf"), "second")
	write(t, filepath.Join(root, "c", "report.pdf"), "third")
	ctx := context.Background()

	for _, dir := range []string{"a", "b", "c"} {
		if _, err := ex.Execute(ctx, root, mv("Reports"), dir+"/report.pdf"); err != nil {
			t.Fatalf("move %s: %v", dir, err)
		}
	}
	want := map[string]string{
		"report.pdf":     "first",
		"report (1).pdf": "second",
		"report (2).pdf": "third",
	}
	for name, content := range want {
		if got := read(t, filepath.Join(root, "Reports", name)); got != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
}

func TestExecute_CollisionWithPreexistingFilesAndNoExtension(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "Dst", "Makefile"), "old")
	write(t, filepath.Join(root, "Dst", ".env"), "old-env")
	write(t, filepath.Join(root, "Makefile"), "new")
	write(t, filepath.Join(root, ".env"), "new-env")
	ctx := context.Background()
	if _, err := ex.Execute(ctx, root, mv("Dst"), "Makefile"); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Execute(ctx, root, mv("Dst"), ".env"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(root, "Dst", "Makefile")) != "old" || read(t, filepath.Join(root, "Dst", "Makefile (1)")) != "new" {
		t.Error("Makefile collision mishandled")
	}
	if read(t, filepath.Join(root, "Dst", ".env")) != "old-env" || read(t, filepath.Join(root, "Dst", ".env (1)")) != "new-env" {
		t.Error(".env collision mishandled")
	}
}

func TestUndo_RoundTrip(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "hello")
	ctx := context.Background()

	entry, err := ex.Execute(ctx, root, mv("Docs"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Undo(ctx, entry.ID); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "hello" {
		t.Fatalf("restored content = %q", got)
	}
	if exists(filepath.Join(root, "Docs", "a.txt")) {
		t.Fatal("dst still exists after undo")
	}
	if got, _ := j.Get(ctx, entry.ID); got.UndoneAt == nil {
		t.Fatal("journal entry not marked undone")
	}
	if err := ex.Undo(ctx, entry.ID); !errors.Is(err, ErrAlreadyUndone) {
		t.Fatalf("second undo = %v, want ErrAlreadyUndone", err)
	}
}

func TestUndo_RestoresCollisionRenamedFile(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "Docs", "a.txt"), "existing")
	write(t, filepath.Join(root, "a.txt"), "mine")
	ctx := context.Background()
	entry, err := ex.Execute(ctx, root, mv("Docs"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Undo(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(root, "a.txt")) != "mine" || read(t, filepath.Join(root, "Docs", "a.txt")) != "existing" {
		t.Fatal("undo touched the wrong file")
	}
}

func TestUndo_RecreatesRemovedSourceDir(t *testing.T) {
	ex, _, root, _ := setup(t)
	write(t, filepath.Join(root, "old", "a.txt"), "x")
	ctx := context.Background()
	entry, err := ex.Execute(ctx, root, mv("Docs"), "old/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := ex.Undo(ctx, entry.ID); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if read(t, filepath.Join(root, "old", "a.txt")) != "x" {
		t.Fatal("not restored")
	}
}

func TestUndo_DestinationDeleted(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "x")
	ctx := context.Background()
	entry, err := ex.Execute(ctx, root, mv("Docs"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "Docs", "a.txt")); err != nil {
		t.Fatal(err)
	}
	err = ex.Undo(ctx, entry.ID)
	if !errors.Is(err, ErrUndoDestinationMissing) {
		t.Fatalf("err = %v, want ErrUndoDestinationMissing", err)
	}
	if got, _ := j.Get(ctx, entry.ID); got.UndoneAt != nil {
		t.Fatal("failed undo was recorded as done")
	}
}

func TestUndo_SourceOccupied(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "original")
	ctx := context.Background()
	entry, err := ex.Execute(ctx, root, mv("Docs"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "newcomer")
	err = ex.Undo(ctx, entry.ID)
	if !errors.Is(err, ErrUndoSourceOccupied) {
		t.Fatalf("err = %v, want ErrUndoSourceOccupied", err)
	}
	if read(t, filepath.Join(root, "a.txt")) != "newcomer" || read(t, filepath.Join(root, "Docs", "a.txt")) != "original" {
		t.Fatal("files were modified by a refused undo")
	}
	if got, _ := j.Get(ctx, entry.ID); got.UndoneAt != nil {
		t.Fatal("refused undo was recorded as done")
	}
}

func TestUndo_UnknownID(t *testing.T) {
	ex, _, _, _ := setup(t)
	if err := ex.Undo(context.Background(), 42); !errors.Is(err, ErrJournalEntryNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUndo_RefusesTamperedJournalEntry(t *testing.T) {
	ex, j, root, parent := setup(t)
	write(t, filepath.Join(root, "Docs", "a.txt"), "x")
	e, _ := j.Append(context.Background(), JournalEntry{
		Root: root, Action: decision.Move,
		Src: filepath.Join(parent, "pwned.txt"), Dst: filepath.Join(root, "Docs", "a.txt"),
	})
	if err := ex.Undo(context.Background(), e.ID); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if exists(filepath.Join(parent, "pwned.txt")) {
		t.Fatal("undo wrote outside root")
	}
}

func TestExecute_JournalFailureRollsBackMove(t *testing.T) {
	ex, j, root, _ := setup(t)
	write(t, filepath.Join(root, "a.txt"), "x")
	j.appendErr = errors.New("disk full")
	if _, err := ex.Execute(context.Background(), root, mv("Docs"), "a.txt"); err == nil {
		t.Fatal("expected error")
	}
	if read(t, filepath.Join(root, "a.txt")) != "x" {
		t.Fatal("move was not rolled back after journal failure")
	}
	if exists(filepath.Join(root, "Docs", "a.txt")) {
		t.Fatal("dst remains")
	}
}

// swapForSymlink replaces the directory dir with a symlink to target, as
// another process could between the Executor's checks and its file operations.
func swapForSymlink(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
}

func dirEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// The target folder passes the containment check, then is replaced by a
// symlink to a folder outside the root before the move happens.
func TestExecute_TargetDirSwappedForOutsideSymlinkMidOperation(t *testing.T) {
	ex, j, root, parent := setup(t)
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "secret")
	ex.beforeMove = func() { swapForSymlink(t, filepath.Join(root, "Docs"), outside) }

	_, err := ex.Execute(context.Background(), root, mv("Docs"), "a.txt")
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if n := dirEntries(t, outside); n != 0 {
		t.Fatalf("%d entries appeared outside the root", n)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "secret" {
		t.Fatalf("source changed: %q", got)
	}
	if len(j.entries) != 0 {
		t.Fatalf("journal has %d entries for a move that did not happen", len(j.entries))
	}
}

// The folder holding the source is replaced by a symlink to a folder outside
// the root that has a file of the same name: the outside file must not move.
func TestExecute_SourceDirSwappedForOutsideSymlinkMidOperation(t *testing.T) {
	ex, _, root, parent := setup(t)
	outside := filepath.Join(parent, "outside")
	write(t, filepath.Join(outside, "a.txt"), "not yours")
	write(t, filepath.Join(root, "sub", "a.txt"), "mine")
	ex.beforeMove = func() { swapForSymlink(t, filepath.Join(root, "sub"), outside) }

	_, err := ex.Execute(context.Background(), root, mv("Docs"), "sub/a.txt")
	if err == nil {
		t.Fatal("Execute succeeded although its source directory was swapped for an outside symlink")
	}
	if got := read(t, filepath.Join(outside, "a.txt")); got != "not yours" {
		t.Fatalf("file outside the root was touched: %q", got)
	}
}

// Undo re-verifies the journal entry, then the folder the file sits in is
// replaced by a symlink to a folder outside the root.
func TestUndo_DestinationDirSwappedForOutsideSymlinkMidOperation(t *testing.T) {
	ex, _, root, parent := setup(t)
	outside := filepath.Join(parent, "outside")
	write(t, filepath.Join(outside, "a.txt"), "not yours")
	write(t, filepath.Join(root, "a.txt"), "mine")
	entry, err := ex.Execute(context.Background(), root, mv("Docs"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	ex.beforeMove = func() { swapForSymlink(t, filepath.Join(root, "Docs"), outside) }

	if err := ex.Undo(context.Background(), entry.ID); err == nil {
		t.Fatal("Undo succeeded although the moved file's folder was swapped for an outside symlink")
	}
	if got := read(t, filepath.Join(outside, "a.txt")); got != "not yours" {
		t.Fatalf("file outside the root was touched: %q", got)
	}
	if exists(filepath.Join(root, "a.txt")) {
		t.Fatal("a file was moved into the root from outside it")
	}
}
