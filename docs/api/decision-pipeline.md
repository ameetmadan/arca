# Decision pipeline and batch sort

How file events and batch sorting reach the decision queue
([ADR-0006](../adr/0006-decision-engine-chain-with-policy-and-executor.md),
[ADR-0005](../adr/0005-content-based-sorting-embed-cluster-name.md)).

## Watcher events

```
watcher -> pipeline (debounce, de-dup) -> index -> Decider -> decisions.Service.Submit
                                                      |
                              Facts -> decision chain -+-> policy: drop | queue | apply now (Executor)
```

For every created or modified file the pipeline first indexes it, then the
`Decider` (`backend/internal/pipeline/decider.go`) decides. A file is **indexed
only** (no decision) when it is outside every folder in `watcher.folders`, is
hidden (`.name`), looks like a partial download (`.crdownload`, `.part`,
`.partial`, `.download`, `.tmp`), is a directory, or has vanished. A folder whose
policy mode is `off` is also indexed only.

Decisions carry the watched folder as `root` and a root-relative `file` and
`target`, as the Executor requires.

### Backpressure

If a watched folder has **more than N** decisions with status `pending`, no new
decisions are generated for it (the engine is not even consulted), so an ignored
inbox cannot grow without bound. Decisions resume as soon as the user accepts or
rejects enough of them. This is logged once when the limit is hit and once when
it clears.

```yaml
decision:
  max_pending_per_folder: 50   # default; 0 or omitted = 50
```

Batch sorting (below) is user-initiated and is not limited. A file that already
has a pending decision is not decided on again, so repeated writes do not queue
duplicates.

### Files that are still being written

Before deciding, the Decider stats the file, waits 250 ms and stats it again. If
size or modification time changed, it skips the file for now. The write that
changed it produces another event, which is handled after the debounce and the
next check. The wait happens on one pipeline worker for at most 250 ms and does
not block other files' events from queueing.

### No feedback loop from Arca's own moves

Moving a file through the Executor makes the watcher report a removal at the
source and a creation at the destination; undoing a move reports the restored
file. These would otherwise be decided on again. `pipeline.RecentMoves`
remembers both ends of every Executor move (made by accepting a decision, an
automatic apply, or `POST /sort/apply`) and the restored location of every undo
for one minute; events for those paths are ignored. The executor handed to the
decision service is wrapped, so no code path can forget to record. In addition,
a Move decision whose target is the folder the file is already in is not
submitted.

Limits: the memory is per process and per minute. Undoing a move made before a
restart is not recognised, so the restored file may be suggested again.

### When the queue or Executor is missing

If the decision queue database or the Executor could not be opened, or no
decision engine is configured, the server logs once at start-up and files are
indexed only.

### Errors

An error from the engine, Facts or Submit for one file is reported by the
pipeline and affects only that file.

## `POST /api/v1/sort` (and legacy `POST /api/sort`)

Request: `{"folder": "/abs/path"}`. Response `data.moves` is unchanged:

```json
{ "moves": [ { "file": "a.txt", "suggested_folder": "Reports" } ] }
```

Optional `"submit": true` (folder must be absolute; 503 if the queue is
unavailable) also sends each suggestion through the decision queue as a `move`
decision with `source: "cluster"` and a reason such as
`cluster "Reports": 4 files with similar content`. The folder's policy decides
whether it is dropped, queued or applied. Confidence is the cluster's cohesion
capped at 0.8 (0.3 for a file with nothing to group with), so under the default
auto threshold of 0.9 cluster decisions are queued for review. The response then
also contains:

```json
{ "decisions": { "queued": 4, "applied": 0, "dropped": 0, "skipped": 0, "failed": 0 } }
```

`skipped` counts files that already had a pending decision. `POST
/api/v1/sort/apply` is unchanged and still moves files through the Executor.
