# Arca Architecture

_Last updated: 2026-10-12_

Arca is a local-first macOS file assistant. A SwiftUI shell launches an embedded
Go backend that does all parsing, embedding, retrieval, and inference on-device.

This document has two diagrams:

1. **Current state:** what is in `main` today.
2. **Target state:** the direction agreed in ADR-0006 to ADR-0008 (decision engine,
   learning from existing folders, optional hosted decision models).

The reasoning behind each choice is in [`docs/adr/`](../adr/README.md).

---

## 1. Current state (2026-10-12)

```mermaid
flowchart TB
    subgraph mac["macOS app bundle — Arca.app"]
        subgraph swift["SwiftUI shell (app/)"]
            views["Views<br/>File Sorter · Document Chat · Table Chat<br/>Settings · Onboarding"]
            state["AppState"]
            api["APIClient.swift<br/>(single HTTP gateway)"]
            proc["BackendProcess.swift<br/>spawn / terminate"]
            views --> state
            views --> api
        end

        subgraph go["Go backend — arca-server (backend/)  ·  localhost:44742"]
            router["chi router<br/>/api/index · /api/sort · /api/sort/apply<br/>/api/chat (SSE) · /api/query<br/>/api/models · /api/models/download (SSE) · /api/models/activate"]

            subgraph internal["internal/"]
                config["config<br/>YAML → DI wiring<br/>(only place providers are chosen)"]
                sorter["sorter<br/>parse → embed → k-means → LLM names"]
                watcher["watcher<br/>fsnotify"]
                mm["modelmanager<br/>GGUF download + SHA256"]
            end

            subgraph pkg["pkg/ — interfaces + implementations"]
                llm["llm.LLMProvider<br/>→ llamacpp"]
                embed["embed.EmbeddingProvider<br/>→ nomic"]
                vector["vector.VectorStore<br/>→ sqlitevec"]
                parser["parser.DocumentParser<br/>pdf · docx · xlsx · md · txt"]
                sql["sqlengine.SQLEngine<br/>→ duckdb (NL→SQL via LLM)"]
            end

            router --> sorter
            router --> mm
            router --> llm & embed & vector & parser & sql
            sorter --> llm & embed & parser
            config -. wires .-> llm & embed & vector & sql & parser
        end
    end

    fs[("User folders")]
    disk[("~/Library/Application Support/Arca<br/>config.yaml · models/*.gguf · *.db")]
    hf[("Hugging Face<br/>(model download only)")]

    proc -- launches --> go
    api -- "HTTP / SSE" --> router
    watcher -- "HTTP POST /api/index<br/>(loops back to itself)" --> router
    fs -- events --> watcher
    sorter -- reads --> fs
    router -- "os.Rename (sort/apply)" --> fs
    config --> disk
    mm --> disk
    mm -- HTTPS --> hf

    classDef risk fill:#fde2e2,stroke:#c0392b,color:#000
    class watcher risk
```

### Known gaps in the current state

| Area | Gap | Addressed by |
|---|---|---|
| `/api/sort/apply` | Joins user-supplied paths without checking they stay inside `folder`, so `../` escapes are possible | ADR-0006 (Executor owns all file moves) |
| Watcher | Sends files to indexing by calling its own HTTP API with hand-built JSON (breaks on quotes in paths). Only indexes, never decides | ADR-0006 (in-process event pipeline) |
| Sorting | One-shot batch suggestions. Ignores the user's existing folder structure and doesn't learn from corrections | ADR-0006, ADR-0007 |
| Server lifecycle | No graceful `http.Server.Shutdown`. No `/health` or `/ready` | Not yet decided |
| API conventions | Routes aren't versioned (`/api/*`). The `error` field is `""` instead of `null` | Not yet decided |
| Repo hygiene | ~74 MB binaries and `dist/Arca.app` are committed | Not yet decided |

---

## 2. Target state: decision engine

```mermaid
flowchart TB
    subgraph swift["SwiftUI shell"]
        views2["Views<br/>File Sorter · Document Chat · Table Chat · Settings"]
        inbox["<b>Inbox</b> (new)<br/>pending suggestions · reason · accept / reject / undo"]
        rulesui["<b>Rules editor</b> (new, in Settings)"]
        calib["<b>Calibration</b> (new, optional)<br/>10–20 most-uncertain files"]
        api2["APIClient.swift"]
        views2 & inbox & rulesui & calib --> api2
    end

    subgraph go["Go backend  ·  /api/v1/*"]
        router2["chi router<br/>existing routes +<br/>/decisions · /decisions/{id}/accept|reject<br/>/rules · /calibration · /journal/{id}/undo"]

        subgraph app["Application layer (internal/)"]
            pipeline["<b>pipeline</b> (new)<br/>in-process event bus"]
            facts["<b>Facts builder</b> (new)<br/>parser · EXIF · metadata · embedding"]
            policy["<b>Policy</b> (new)<br/>per-folder mode: off · suggest · auto<br/>auto-apply threshold"]
            executor["<b>Executor</b> (new)<br/>path containment check · journal · undo<br/><i>only code allowed to move files</i>"]
            feedback["<b>Feedback</b> (new)<br/>accept/reject → update centroids<br/>repeated corrections → suggested rules"]
            sorter2["sorter<br/>batch mode: cluster + name<br/>(cold start for unstructured folders)"]
            watcher2["watcher"]
            config2["config<br/>wires engine chain + providers"]
        end

        subgraph domain["Domain: pkg/decision (new)"]
            engine["decision.Engine<br/>Decide(ctx, Facts) → Decision<br/>{Action, Target, Confidence, Reason, Source}"]
            chain["Chain: first answer that clears its threshold wins"]
            rules["① rules<br/>expr-lang/expr · user YAML<br/>conf = 1.0"]
            semantic["② semantic<br/>nearest folder centroid / kNN<br/>built from the user's existing folders"]
            llmdec["③ local LLM<br/>pick from a fixed list of folders, JSON output checked"]
            jev["④ hosted model, e.g. Jev<br/><i>optional, opt-in, off by default</i><br/>sends data off the device"]
            engine --> chain --> rules --> semantic --> llmdec -.-> jev
        end

        subgraph pkg2["pkg/ providers (unchanged)"]
            llm2["llm"] 
            embed2["embed"]
            vector2["vector<br/>+ folder centroids"]
            parser2["parser"]
            sql2["sqlengine"]
        end

        router2 --> pipeline
        router2 --> executor
        watcher2 -- "file event (in-process)" --> pipeline
        sorter2 -- "batch decisions" --> pipeline
        pipeline --> facts --> engine
        engine --> policy
        policy -- "confidence ≥ threshold & mode=auto" --> executor
        policy -- "otherwise" --> queue[("decision queue")]
        queue --> router2
        router2 -- "accept" --> executor
        router2 -- "accept / reject" --> feedback
        feedback --> vector2
        feedback -. "proposes" .-> rules
        semantic --> vector2
        llmdec --> llm2
        facts --> parser2 & embed2
        config2 -. wires .-> engine & llm2 & embed2 & vector2 & sql2
    end

    fs2[("User folders")]
    db[("SQLite<br/>vectors · centroids · decisions · journal")]
    cloud[("Hosted decision API")]

    api2 -- "HTTP / SSE" --> router2
    fs2 -- events --> watcher2
    executor -- "move / tag (inside folder only)" --> fs2
    executor --> db
    queue --- db
    vector2 --- db
    jev -.-> cloud

    classDef new fill:#e3f2e1,stroke:#2e7d32,color:#000
    classDef optional fill:#fff4d6,stroke:#b8860b,stroke-dasharray:4 3,color:#000
    class inbox,rulesui,calib,pipeline,facts,policy,executor,feedback,engine,chain,rules,semantic,llmdec new
    class jev,cloud optional
```

### How a file flows through the target design

1. **Event:** the watcher, a manual sort, or an API call puts a file on the in-process pipeline.
2. **Facts:** the parser and EXIF/metadata fallback produce a snippet, which is then embedded.
3. **Decide:** the chain tries user rules first, then the nearest existing folder, then the
   local LLM. A hosted model is used only if the user turned it on. Every `Decision` has a
   `Reason` and a `Source`.
4. **Policy:** decides whether to apply automatically or queue a suggestion. The default is
   `suggest`. A folder becomes eligible for `auto` once its acceptance rate is high enough.
5. **Execute:** the Executor checks the target stays inside the folder, records the action in
   the journal (for undo), and moves the file.
6. **Learn:** accepting or rejecting a suggestion updates the folder centroids. Repeated
   identical corrections become suggested rules.

### Phasing

| Phase | Deliverable | ADR |
|---|---|---|
| 1 | `pkg/decision` types, Executor + journal + undo; route `/api/sort/apply` through the Executor | 0006 |
| 2 | Rules engine (`expr-lang/expr`) + `/api/v1/rules` + Rules editor | 0006 |
| 3 | Semantic engine built from existing folders, local LLM fallback, in-process watcher pipeline, Inbox | 0006, 0007 |
| 4 | Feedback loop, optional calibration, per-folder auto mode | 0007 |
| later | Optional hosted decision provider (e.g. Jev), off by default | 0008 |
