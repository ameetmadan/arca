# Learning and calibration

Accept/reject resolution and its feedback event are committed in one SQLite transaction.
Pending decisions retain the embedding used to make the suggestion, without storing snippets.
An accepted move contributes its vector to the chosen folder. A different target or rejection
records a miss for the original suggestion. Failed moves and automatic moves are not feedback.
The last 100 resolved decisions per watched root determine auto eligibility. A claimed but
unfinished accept does not count. Existing completed decisions remain usable for stats.

The semantic index overlays the latest confirmation for each root/file on the base folder
examples, replacing an already indexed copy rather than double counting it. Reindexing cannot
erase explicit confirmations. No vectors, snippets, tokens, or file contents are logged.

`GET /api/v1/rules/suggestions` returns `{suggestions: [...]}` with `id`, `root`, `when`,
`action`, `target`, `reason`, `enabled: false`, and `count`. The heuristic groups corrections by
watched root, normalized extension, and corrected target. At three corrections (or
`decision.suggestion_threshold`) it proposes an extension rule scoped to that root. This is
intentionally simple: an extension is not a semantic category, and heterogeneous formats such
as PDF often need a narrower expression. Suggestions are derived from durable feedback, never
inserted into the active rules file. Creating/enabling a rule remains an explicit user action.

`GET /api/v1/calibration?limit=15` scans supported files under watched roots, ranks by the
smallest cosine margin between the top two indexed folders, and removes vectors with cosine
similarity >= 0.98 to an earlier selected file in the same root. Each item contains `root`,
`file`, `margin`, and up to three `{target, score}` candidates. Limits are 1–20. Files without
usable embeddings or two comparable folders are omitted. Hidden files and symlinks are skipped.
This is a read-only operation and may take time on large folders; cancellation stops the scan.

When a user answers, `POST /api/v1/calibration/decisions` with `{root, file}` reuses an existing
pending decision or creates an ask-user decision with its embedding. The client then calls the
normal `/decisions/{id}/accept` endpoint with the chosen target. Confirming the current folder
records feedback without moving the file or creating an undo entry. Other placements use the
normal executor and journal. Skip and Done make no write requests. Calibration is offered once
after a successful first index and remains available from Settings.

Hosted providers remain deferred under issue #20 and ADR-0008. No hosted service is wired.
