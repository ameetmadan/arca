package decisions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

const (
	// DefaultLimit is the page size used when the caller gives none.
	DefaultLimit = 50
	// MaxLimit is the largest page a caller may ask for.
	MaxLimit = 200

	maxPathLen = 4096 // root and file
	maxTarget  = 1024 // target folder
)

// Deps are the collaborators of a Service. Queue, Policies and Stats are
// required; Exec may be nil, in which case Accept, Undo and automatic
// application fail with ErrExecutorUnavailable. Now defaults to time.Now.
type Deps struct {
	Queue    Store
	Policies PolicyStore
	Stats    policy.StatsProvider
	Exec     FileExecutor
	Now      func() time.Time
}

// Service is the application service for the decision queue. It is safe for
// concurrent use if its dependencies are.
type Service struct {
	d Deps
}

// NewService returns a Service using d. It panics if a required dependency is
// nil, since that is a programming error.
func NewService(d Deps) *Service {
	if d.Queue == nil || d.Policies == nil || d.Stats == nil {
		panic("decisions: NewService needs Queue, Policies and Stats")
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{d: d}
}

// SubmitResult reports what Submit did with a decision.
type SubmitResult struct {
	Outcome policy.Outcome
	// Record is the stored record: pending when queued, applied (or failed)
	// when the policy applied it. It is nil when the decision was dropped.
	Record *Record
}

// Submit runs decision d about file (relative to root) through the folder's
// policy. Depending on the outcome it is dropped, stored as a pending record,
// or executed immediately and recorded as applied.
//
// If an automatic application fails, the failure is recorded (status failed)
// and returned together with the result, so callers can log it and move on.
func (s *Service) Submit(ctx context.Context, root, file string, d decision.Decision) (SubmitResult, error) {
	return s.SubmitWithFacts(ctx, root, file, d, decision.Facts{})
}

func (s *Service) SubmitWithFacts(ctx context.Context, root, file string, d decision.Decision, facts decision.Facts) (SubmitResult, error) {
	if err := validateRoot(root); err != nil {
		return SubmitResult{}, err
	}
	if err := validateFile(file); err != nil {
		return SubmitResult{}, err
	}
	if err := validateDecision(d); err != nil {
		return SubmitResult{}, err
	}

	settings, err := s.settings(ctx, root)
	if err != nil {
		return SubmitResult{}, err
	}
	// Only auto mode needs the acceptance history.
	var stats policy.Stats
	if settings.Mode == policy.ModeAuto && d.Action == decision.Move {
		if stats, err = s.d.Stats.AcceptanceStats(ctx, root); err != nil {
			return SubmitResult{}, fmt.Errorf("read acceptance stats: %w", err)
		}
	}

	outcome := policy.Evaluate(settings, d, stats)
	switch outcome {
	case policy.Drop:
		return SubmitResult{Outcome: outcome}, nil
	case policy.ApplyNow:
		if s.d.Exec == nil {
			// Never fail open: without an executor the user decides later.
			outcome = policy.Queue
		}
	}

	rec := Record{
		Vector: facts.Vector,
		TS:     s.d.Now().UTC(), Root: root, File: file, Action: d.Action, Target: d.Target,
		Confidence: d.Confidence, Reason: d.Reason, Source: d.Source, Status: StatusPending,
	}
	if outcome == policy.Queue {
		stored, err := s.d.Queue.Insert(ctx, rec)
		if err != nil {
			return SubmitResult{}, fmt.Errorf("queue decision: %w", err)
		}
		return SubmitResult{Outcome: outcome, Record: &stored}, nil
	}

	entry, execErr := s.d.Exec.Execute(ctx, root, d, file)
	if execErr != nil {
		rec.Status = StatusFailed
	} else {
		rec.Status = StatusApplied
		rec.JournalID = &entry.ID
	}
	// The file may already have moved, so record it even if ctx was cancelled.
	stored, err := s.d.Queue.Insert(context.WithoutCancel(ctx), rec)
	if err != nil {
		return SubmitResult{}, errors.Join(fmt.Errorf("record decision: %w", err), execErr)
	}
	return SubmitResult{Outcome: outcome, Record: &stored}, execErr
}

// List returns a page of queued decisions, newest first. A zero Limit means
// DefaultLimit.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	if f.Status != "" && !f.Status.Valid() {
		return Page{}, fmt.Errorf("%w: status must be one of pending, accepted, rejected, applied, failed", ErrInvalid)
	}
	if f.Limit == 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit < 0 || f.Limit > MaxLimit {
		return Page{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalid, MaxLimit)
	}
	if f.Offset < 0 {
		return Page{}, fmt.Errorf("%w: offset must not be negative", ErrInvalid)
	}
	p, err := s.d.Queue.List(ctx, f)
	if err != nil {
		return Page{}, fmt.Errorf("list decisions: %w", err)
	}
	p.Limit, p.Offset = f.Limit, f.Offset
	return p, nil
}

// CountPending returns how many decisions are waiting for the user in the
// watched folder root. A non-empty file narrows the count to that file
// (relative to root). The pipeline uses it to stop suggesting for a folder
// whose queue is not being worked through, and to avoid queueing the same file
// twice.
func (s *Service) CountPending(ctx context.Context, root, file string) (int, error) {
	if err := validateRoot(root); err != nil {
		return 0, err
	}
	p, err := s.d.Queue.List(ctx, ListFilter{Status: StatusPending, Root: root, File: file, Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("count pending decisions: %w", err)
	}
	return p.Total, nil
}

// Accept carries out a pending decision through the Executor, so path
// containment, no-overwrite and journalling all apply. A non-empty target
// replaces the suggested target; it is required for ask_user decisions.
//
// If the file has since moved or vanished, the Executor's error is returned
// (wrapped) and the decision is marked failed. For other failures the decision
// stays pending so the user can retry, for example with another target.
func (s *Service) Accept(ctx context.Context, id int64, target string) (Record, error) {
	if s.d.Exec == nil {
		return Record{}, ErrExecutorUnavailable
	}
	override := target != ""
	if override {
		if err := validateTarget(target); err != nil {
			return Record{}, err
		}
	}

	rec, err := s.d.Queue.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if rec.Status != StatusPending {
		return Record{}, fmt.Errorf("%w: it is already %s", ErrNotPending, rec.Status)
	}
	if rec.Action != decision.Move && rec.Action != decision.AskUser {
		return Record{}, fmt.Errorf("%w: a %s decision cannot be carried out", ErrInvalid, rec.Action)
	}

	d := decision.Decision{
		Action: decision.Move, Target: rec.Target, Confidence: rec.Confidence,
		Reason: rec.Reason, Source: rec.Source,
	}
	if override {
		d.Target = target
		d.Source = "user"
		d.Reason = "target chosen by the user"
		if rec.Source != "" {
			d.Reason += " (suggested by " + rec.Source + ")"
		}
	}
	if d.Target == "" {
		return Record{}, ErrTargetRequired
	}

	// Claim the record first so two concurrent accepts cannot both move the file.
	if err := s.d.Queue.Transition(ctx, id, StatusPending, StatusAccepted, Update{}); err != nil {
		return Record{}, claimError(err, rec.Status)
	}

	// A calibration answer can confirm the file's current folder without a move.
	if rec.Source == "calibration" && filepath.Clean(filepath.Dir(rec.File)) == filepath.Clean(d.Target) {
		if err := s.d.Queue.Transition(context.WithoutCancel(ctx), id, StatusAccepted, StatusAccepted, Update{Target: &d.Target, Resolved: true}); err != nil {
			return Record{}, err
		}
		return s.d.Queue.Get(context.WithoutCancel(ctx), id)
	}
	entry, execErr := s.d.Exec.Execute(ctx, rec.Root, d, rec.File)

	// The file may already have moved, so bookkeeping must survive a cancelled ctx.
	bg := context.WithoutCancel(ctx)
	if execErr != nil {
		to := StatusPending // let the user retry
		if isStale(execErr) {
			to = StatusFailed
		}
		if err := s.d.Queue.Transition(bg, id, StatusAccepted, to, Update{}); err != nil {
			return Record{}, errors.Join(execErr, fmt.Errorf("release decision %d: %w", id, err))
		}
		return Record{}, execErr
	}

	upd := Update{JournalID: &entry.ID}
	if override {
		upd.Target = &d.Target
	}
	if err := s.d.Queue.Transition(bg, id, StatusAccepted, StatusAccepted, upd); err != nil {
		return Record{}, fmt.Errorf("file moved (journal entry %d) but recording it failed: %w", entry.ID, err)
	}
	return s.d.Queue.Get(bg, id)
}

// Reject marks a pending decision as rejected. It never touches files.
func (s *Service) Reject(ctx context.Context, id int64) (Record, error) {
	if err := s.d.Queue.Transition(ctx, id, StatusPending, StatusRejected, Update{}); err != nil {
		if errors.Is(err, ErrWrongStatus) {
			cur, gerr := s.d.Queue.Get(ctx, id)
			if gerr != nil {
				return Record{}, gerr
			}
			return Record{}, fmt.Errorf("%w: it is already %s", ErrNotPending, cur.Status)
		}
		return Record{}, err
	}
	return s.d.Queue.Get(ctx, id)
}

// Undo reverses the Executor action with the given journal id.
func (s *Service) Undo(ctx context.Context, journalID int64) error {
	if s.d.Exec == nil {
		return ErrExecutorUnavailable
	}
	if journalID < 1 {
		return fmt.Errorf("%w: journal id must be a positive integer", ErrInvalid)
	}
	err := s.d.Exec.Undo(ctx, journalID)
	if errors.Is(err, executor.ErrJournalEntryNotFound) {
		return ErrNotFound
	}
	return err
}

// PolicyView is a folder's policy together with its acceptance history.
type PolicyView struct {
	Root     string
	Settings policy.Settings
	Stats    policy.Stats
	// Eligible reports whether the folder has earned auto mode.
	Eligible bool
}

// GetPolicy returns the policy of root, or the defaults if none was set.
func (s *Service) GetPolicy(ctx context.Context, root string) (PolicyView, error) {
	if err := validateRoot(root); err != nil {
		return PolicyView{}, err
	}
	settings, err := s.settings(ctx, root)
	if err != nil {
		return PolicyView{}, err
	}
	return s.view(ctx, root, settings)
}

// PutPolicy validates and stores the policy of root. Auto mode is refused with
// ErrNotEligible until the folder has earned it.
func (s *Service) PutPolicy(ctx context.Context, root string, settings policy.Settings) (PolicyView, error) {
	if err := validateRoot(root); err != nil {
		return PolicyView{}, err
	}
	if err := settings.Validate(); err != nil {
		return PolicyView{}, err
	}
	v, err := s.view(ctx, root, settings)
	if err != nil {
		return PolicyView{}, err
	}
	if settings.Mode == policy.ModeAuto && !v.Eligible {
		return PolicyView{}, fmt.Errorf("%w: it needs at least %d accepted-or-rejected decisions with %.0f%% accepted (currently %d of %d)",
			ErrNotEligible, policy.MinDecisions, policy.MinAcceptanceRate*100, v.Stats.Accepted, v.Stats.Decided)
	}
	if err := s.d.Policies.PutPolicy(ctx, root, settings); err != nil {
		return PolicyView{}, fmt.Errorf("store policy: %w", err)
	}
	return v, nil
}

func (s *Service) view(ctx context.Context, root string, settings policy.Settings) (PolicyView, error) {
	stats, err := s.d.Stats.AcceptanceStats(ctx, root)
	if err != nil {
		return PolicyView{}, fmt.Errorf("read acceptance stats: %w", err)
	}
	return PolicyView{Root: root, Settings: settings, Stats: stats, Eligible: stats.Eligible()}, nil
}

func (s *Service) settings(ctx context.Context, root string) (policy.Settings, error) {
	st, ok, err := s.d.Policies.GetPolicy(ctx, root)
	if err != nil {
		return policy.Settings{}, fmt.Errorf("read policy: %w", err)
	}
	if !ok {
		return policy.DefaultSettings(), nil
	}
	return st, nil
}

// isStale reports whether err means the decision can never succeed as it is
// because the world changed: the file moved or vanished, is already in the
// target folder, or the watched folder is gone.
func isStale(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, executor.ErrAlreadyInPlace) ||
		errors.Is(err, executor.ErrInvalidRoot)
}

func claimError(err error, seen Status) error {
	if errors.Is(err, ErrWrongStatus) {
		return fmt.Errorf("%w: it is already %s", ErrNotPending, seen)
	}
	return err
}

func validateRoot(root string) error {
	switch {
	case root == "" || len(root) > maxPathLen || strings.ContainsRune(root, 0):
		return fmt.Errorf("%w: root must be a non-empty path of at most %d bytes", ErrInvalid, maxPathLen)
	case !filepath.IsAbs(root):
		return fmt.Errorf("%w: root must be an absolute path", ErrInvalid)
	case filepath.Clean(root) != root:
		return fmt.Errorf("%w: root must be a clean path (no '..', '.' or trailing slash)", ErrInvalid)
	}
	return nil
}

func validateFile(file string) error {
	switch {
	case strings.TrimSpace(file) == "" || len(file) > maxPathLen || strings.ContainsRune(file, 0):
		return fmt.Errorf("%w: file must be a non-empty path of at most %d bytes", ErrInvalid, maxPathLen)
	case filepath.IsAbs(file):
		return fmt.Errorf("%w: file must be relative to the root folder", ErrInvalid)
	}
	return nil
}

func validateTarget(target string) error {
	switch {
	case strings.TrimSpace(target) == "" || len(target) > maxTarget || strings.ContainsRune(target, 0):
		return fmt.Errorf("%w: target must be a non-blank folder path of at most %d bytes", ErrInvalid, maxTarget)
	case filepath.IsAbs(target):
		return fmt.Errorf("%w: target must be relative to the root folder", ErrInvalid)
	}
	return nil
}

func validateDecision(d decision.Decision) error {
	if !d.Action.Valid() {
		return fmt.Errorf("%w: unknown action %q", ErrInvalid, d.Action)
	}
	if math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("%w: confidence must be between 0 and 1", ErrInvalid)
	}
	if d.Action == decision.Move {
		return validateTarget(d.Target)
	}
	return nil
}

// Compile-time check that the real Executor fits the interface.
var _ FileExecutor = (*executor.Executor)(nil)
