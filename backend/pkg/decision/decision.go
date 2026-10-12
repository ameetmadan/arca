// Package decision holds the domain types and the engine chain used to decide
// what to do with a file (move, tag, index, ignore, or ask the user).
//
// The package is pure domain logic: it never touches the filesystem, the
// network, or any provider. Concrete engines (rules, semantic, LLM), policy and
// execution live elsewhere and depend on this package, not the other way round.
// See docs/adr/0006-decision-engine-chain-with-policy-and-executor.md.
package decision

import (
	"context"
	"errors"
	"time"
)

// Facts is everything an Engine may look at when deciding about one file.
// It is built by the application layer; engines treat it as read-only.
type Facts struct {
	// Path is the absolute path of the file.
	Path string
	// RelPath is the path relative to the watched root folder.
	RelPath string
	// Ext is the lower-case file extension including the leading dot, e.g. ".pdf".
	Ext string
	// MIME is the detected media type, e.g. "application/pdf".
	MIME string
	// Size is the file size in bytes.
	Size int64
	// Modified is the last modification time of the file.
	Modified time.Time
	// EXIF holds image or document metadata as key/value pairs; it may be nil.
	EXIF map[string]string
	// Snippet is a short excerpt of the file's text content; it may be empty.
	// It is file content, so it must never be logged.
	Snippet string
	// Vector is the embedding of the file's content; it may be empty.
	Vector []float32
}

// Action is what should happen to a file.
type Action string

const (
	// Move relocates the file to Decision.Target.
	Move Action = "move"
	// Tag labels the file; Decision.Target holds the tag.
	Tag Action = "tag"
	// Index adds the file to the search index without moving it.
	Index Action = "index"
	// Ignore leaves the file alone and does not index it.
	Ignore Action = "ignore"
	// AskUser means no engine was confident enough, so the user should choose.
	AskUser Action = "ask_user"
)

// String returns the wire name of the action.
func (a Action) String() string { return string(a) }

// Valid reports whether a is one of the defined actions.
func (a Action) Valid() bool {
	switch a {
	case Move, Tag, Index, Ignore, AskUser:
		return true
	}
	return false
}

// Decision is an Engine's answer for one file. It carries enough information
// to explain itself (Reason, Source) so the user can understand and undo it.
type Decision struct {
	// Action is what should happen to the file.
	Action Action
	// Target is the destination folder for Move, or the tag for Tag.
	// It is empty for the other actions.
	Target string
	// Confidence is the engine's certainty, from 0 (none) to 1 (certain).
	Confidence float64
	// Reason is a short human-readable explanation. It must not contain file contents.
	Reason string
	// Source says where the decision came from, e.g. "semantic" or
	// "rule:invoices". An engine may set it to be more specific than its name;
	// Chain fills in the step name when the engine leaves it empty.
	Source string
}

// ErrNoDecision is returned by an Engine that cannot place the file. The Chain
// treats it as "try the next engine" rather than as a failure.
var ErrNoDecision = errors.New("decision: no decision")

// Engine decides what to do with a file.
//
// Implementations must be safe for concurrent use, must honour ctx
// cancellation, and return ErrNoDecision (possibly wrapped) when they cannot
// place the file. Any other error means the engine itself failed.
type Engine interface {
	// Decide returns the engine's decision for the given facts.
	Decide(ctx context.Context, f Facts) (Decision, error)
}
