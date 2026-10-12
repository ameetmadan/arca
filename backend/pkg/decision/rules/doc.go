// Package rules is the first engine in the decision chain: user-written rules,
// evaluated against [decision.Facts] with github.com/expr-lang/expr. A rule that
// matches answers with Confidence 1 and Source "rule:<id>"; if no rule matches
// the engine returns [decision.ErrNoDecision] so the chain moves on. See
// docs/adr/0006-decision-engine-chain-with-policy-and-executor.md.
//
// # Rule file
//
// Rules live in one YAML file. They are tried in file order and the first match
// wins, so put specific rules before general ones. The block below is
// compiled by this package's tests, so it stays correct.
//
//	rules:
//	  # 1. A text match on the extracted snippet.
//	  - id: invoices
//	    when: 'Ext == ".pdf" && Snippet contains "Invoice"'
//	    action: move
//	    target: Finance/Invoices
//	    reason: "PDF mentioning an invoice"
//
//	  # 2. Case-insensitive match, using the lower() builtin.
//	  - id: receipts
//	    when: 'lower(Snippet) contains "receipt"'
//	    action: move
//	    target: Finance/Receipts
//
//	  # 3. A literal regular expression (RE2 syntax) on the file name.
//	  - id: screenshots
//	    when: 'RelPath matches "(^|/)Screenshot [0-9]{4}-"'
//	    action: move
//	    target: Pictures/Screenshots
//
//	  # 4. Several extensions with "in", and a size threshold in bytes.
//	  - id: big-videos
//	    when: 'Ext in [".mov", ".mp4", ".mkv"] && Size > 500 * 1024 * 1024'
//	    action: move
//	    target: Videos/Large
//
//	  # 5. Image metadata. EXIF is a map of strings; check the key exists first.
//	  - id: iphone-photos
//	    when: '"Make" in EXIF && EXIF["Make"] == "Apple" && MIME startsWith "image/"'
//	    action: tag
//	    target: iphone
//
//	  # 6. Age of the file, using now() and duration().
//	  - id: stale-installers
//	    when: 'Ext in [".dmg", ".pkg"] && now().Sub(Modified) > duration("720h")'
//	    action: move
//	    target: Archive/Installers
//
//	  # 7. Never touch some files. "ignore" takes no target.
//	  - id: skip-temp
//	    when: 'hasPrefix(RelPath, "tmp/") || Ext == ".part"'
//	    action: ignore
//
//	  # 8. Look at the whole path with a predicate over its parts.
//	  - id: work-notes
//	    when: 'Ext in [".md", ".txt"] && any(split(RelPath, "/"), # == "Work")'
//	    action: index
//
// # Fields
//
// A rule can read these fields of [decision.Facts]: Path, RelPath, Ext (lower
// case, with the leading dot, so ".pdf" and not "pdf"), MIME, Size (bytes),
// Modified (a time.Time), EXIF (map of strings), Snippet and Vector. They are
// read-only. Unknown names are rejected when the rule is loaded.
//
// Operators and builtins are those of expr (==, !=, <, &&, ||, !, in, contains,
// startsWith, endsWith, matches, ternary, arithmetic) plus a fixed allow-list of
// functions: all, any, none, one, count, len, lower, upper, trim, trimPrefix,
// trimSuffix, hasPrefix, hasSuffix, indexOf, lastIndexOf, split, abs, ceil,
// floor, round, int, float, string, min, max, now, duration and date. Methods
// of time.Time are available on Modified (for example Modified.Year()).
//
// # Fields of a rule
//
//   - id: letters, digits, '.', '_' and '-'; unique; at most 64 characters.
//   - when: the expression; it must have a boolean result.
//   - action: move, tag, index, ignore or ask_user.
//   - target: the folder, relative to the watched root and never containing
//     "..", for move; the tag for tag; must be empty for the other actions.
//   - reason: shown to the user; defaults to "matched rule <id>". Keep file
//     content out of it.
//
// # Safety model
//
// Rule files are edited by the user, and later possibly by generated
// suggestions, so they are treated as untrusted input:
//
//   - Read-only environment. The expression sees a copy of the Facts struct
//     and nothing else. expr has no assignment, and no function in the
//     allow-list touches the filesystem, the network or the clock other than
//     now().
//   - Compile-time bounds. An expression is at most 1000 bytes and 200 syntax
//     nodes. Ranges (a..b) are rejected, a predicate (all, any, ...) cannot
//     contain another predicate, and `matches` needs a literal pattern of at most
//     200 bytes (RE2 runs in linear time). Together these leave at most one
//     level of looping over the Facts, so the cost of an evaluation is
//     polynomial in their size (for example a predicate over Vector that scans
//     Snippet for each element costs len(Vector) times len(Snippet)), with no
//     way to write an exponential or unbounded loop. A rule file is at most
//     1 MiB and 1000 rules.
//   - Evaluation-time bounds. expr has no step counter or context support, so
//     each evaluation runs with a memory budget and under a deadline
//     (DefaultEvalTimeout, 250 ms, per rule per file; see [WithEvalTimeout]).
//     When the deadline passes, Decide stops waiting, treats the rule as not
//     matching and quarantines it until the next reload.
//
// The deadline has a limit that should be understood: a Go goroutine cannot be
// killed. A timed-out evaluation keeps running in the background until it
// finishes by itself. The compile-time bounds are what guarantee that it does
// finish, and quarantining stops it from being started again, so the cost of a
// bad rule is a bounded amount of wasted CPU, not a hang. The deadline is a
// second line of defence, not the first.
//
// A rule that fails to evaluate (division by zero, a type mismatch that only
// shows at run time, a panic, a timeout) counts as not matching and never fails
// the whole decision. Use [WithEvalErrorHook] to be told about it; the errors it
// passes never contain file contents, because expr's own messages can quote
// operands.
//
// # Loading, reloading and storing
//
// [Load] and [New] compile every rule once. Invalid rules (bad expression,
// wrong types, unknown action, unsafe target, duplicate id, unknown YAML key)
// are reported with their id by [Engine.Issues] and left out; the other rules
// still load. [Engine.Reload] re-reads the file and swaps the whole rule set
// atomically, so concurrent Decide calls see either the old or the new rules.
// If the file cannot be read or parsed, the old rules stay active.
//
// [Store] is the interface for loading, saving and validating rules, for the
// HTTP API; [FileStore] implements it on one YAML file with atomic writes.
// [Store.Save] refuses invalid rules so the stored file always loads in full.
//
// # Wiring
//
// This package does not read configuration and does not choose where the rules
// file lives. internal/config builds the engine, calls [Load] with the file path
// and puts it first in the [decision.Chain] with MinConfidence 1.
package rules
