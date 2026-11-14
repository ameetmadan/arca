# ADR-0011: Hosted decision provider: implementation and benchmark

- **Status:** Proposed (benchmark results pending)
- **Date:** 2026-11-14
- **Deciders:** Ameet Madan
- **Supersedes / Superseded by:** Implements [ADR-0008](0008-hosted-decision-models-optional-only.md)

## Context

ADR-0008 allows a hosted decision model (for example TypeSafe's Jev) as an optional last-resort
engine, provided it is opt-in per folder, keeps its key in the Keychain and sends the minimum.
Issue #20 asks for that engine. Its blocking follow-ups (read the vendor's docs on deployment and
data retention, check for an on-device option, confirm the product is stable) could not be done
unattended, so the engine is built vendor-neutral and stays off until they are.

## Decision

- `pkg/decision/hosted` implements `decision.Engine` as engine 4. `internal/config` has one new
  `case` (`type: hosted`) and is the only place it is constructed (ADR-0004).
- **Off by default:** not in the default chain. The config must list it, give an `endpoint` and
  at least one `opt_in_folders` entry (each also in `watcher.folders`). It must be the last step.
  `hosted.New` requires an `Allow` function, so there is no allow-all mode; files outside opted-in
  folders return `ErrNoDecision` before the key is read or any request is made.
- **Minimum data:** a snippet capped at 200 runes, the extension and the candidate folder names.
  No file name, path or whole file. An empty snippet means no call.
- **Key:** read from the macOS Keychain (`security find-generic-password`, service
  `com.arca.hosted-decision`) on first use and cached in memory. Sent only in the
  `Authorization` header; the HTTP client does not follow redirects.
- **No leaks:** log lines and errors contain only a failure class ("timeout", "HTTP 503"), never
  transport error text, response bodies, snippets, paths or the key. Tests assert this.
- **Resilience:** 10 s per request, up to 3 attempts with exponential backoff for network errors,
  timeouts, 429 and 5xx; no retry for other 4xx. After 5 failed calls in a row (401/403 count, other
  4xx do not) a circuit breaker opens for 60 s, then lets one probe through. Every failure is
  `ErrUnavailable`, which wraps `ErrNoDecision`, so the chain ends in `AskUser`.
- **Answers are validated:** the service can only choose one of the candidate folders (exact or
  unique case-insensitive match). Confidence is capped at 0.9.
- **Wire format (assumption):** POST JSON `{question:"choice", input:{ext, snippet}, options:[…]}`;
  reply `{choice, confidence, probabilities}`. It follows the "Choice" shape in ADR-0008 but is
  **not verified against the vendor's documentation**; adapt `request`/`response` in
  `hosted.go` once it is.
- **Benchmark:** `cmd/decisionbench` scores the local chain and local + hosted on a labelled CSV
  and prints a Markdown table (accuracy, precision, coverage, asked, errors, latency).

## Alternatives considered

- **Per-folder switch inside the chain:** would change the generic `decision` package. A gate
  function passed to the engine keeps the change to one package plus one `case`.
- **Storing the key in `config.yaml`:** rejected by ADR-0008.
- **Keychain through CGo (Security.framework):** more code and a build-tag stub; the system
  `security` tool needs neither. The key is read, never passed on a command line.

## Consequences

- **Positive:** nothing changes for users who do not opt in. An outage degrades to the existing
  AskUser path.
- **Negative / trade-offs:** a second network path to maintain. The Keychain item must be added
  by hand until the app has a settings UI for it. The wire format may need changes.
- **Follow-ups (need a human):** verify the vendor API, data retention and on-device options;
  run the benchmark on real labelled folders and record the table below; then decide whether to
  keep the engine, and whether the app should get an opt-in toggle and key entry.

## Benchmark results

_Not yet run. Run `decisionbench` on at least 100 labelled files from real folders, then paste the
table here with the date, model/endpoint version and the label-set size._

| Engine | Files | Accuracy | Precision | Coverage | Asked | Errors | Mean latency |
|---|---|---|---|---|---|---|---|
| local chain | | | | | | | |
| local chain + hosted | | | | | | | |
