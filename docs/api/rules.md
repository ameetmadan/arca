# Rules API (`/api/v1/rules`)

HTTP API for listing, editing, validating, reordering and dry-running the
user's rules. Rules are the first engine in the decision chain
([ADR-0006](../adr/0006-decision-engine-chain-with-policy-and-executor.md)).
What a rule expression can say is documented in the Go package doc of
`backend/pkg/decision/rules` (`doc.go`). This page is the HTTP contract, for
the Swift client.

- Base URL: `http://localhost:44742/api/v1`
- There is **no** unversioned `/api/rules` alias.
- All bodies are JSON (`Content-Type: application/json`). Unknown JSON keys are
  rejected with 400.
- The server stores the rules in `rules.yaml` next to `config.yaml`
  (`~/Library/Application Support/Arca/rules.yaml`). Every write goes to a
  temporary file in the same directory, is fsynced and then renamed over the
  old file, so a crash or concurrent edit never leaves a half-written file.
  Edits are serialised on the server: two clients editing at once both succeed,
  in some order, and neither is lost.
- **Rules are tried in list order and the first match wins.** New rules are
  appended; use `PUT /rules/order` to move them.

## Response envelope

Every response, including errors, has the shape used by the whole API:

```json
{ "data": { }, "error": null, "meta": { "timestamp": "2026-10-12T10:00:00Z" } }
```

- Success: `error` is `null` and `data` holds the result.
- Failure: `error` is a short, human-readable string. `data` is `null`, except
  for validation failures (422 and 409), where `data` is `{"issues": [...]}`
  (see below).

### Issues (field-level validation)

```json
{
  "data": {
    "issues": [
      { "field": "when", "message": "invalid expression: unexpected token EOF ...", "index": 2, "rule_id": "invoices" }
    ]
  },
  "error": "rules are invalid: ...",
  "meta": { "timestamp": "..." }
}
```

| Key | Type | Meaning |
| --- | --- | --- |
| `field` | string | The offending field. For a rule: `id`, `when`, `action`, `target`, `reason`, or `""` for a problem with the rule as a whole (for example an unknown key in a hand-edited file). Other values: `rules` (rule-count limit), `order`, `rule` (missing in `/test`), and `facts.<name>` (for example `facts.ext`). |
| `message` | string | English text suitable for showing next to the field. It never contains file contents. |
| `index` | integer, optional | 0-based position of the rule in the *resulting* rule list. Present only when the problem is about a rule in the list. For `POST /rules` the new rule is last. |
| `rule_id` | string, optional | Id of that rule, if it has one. |

The server will not store invalid rules. If a hand-edited `rules.yaml` already
contains a broken rule, an edit of a *different* rule is refused with an issue
that points at the broken one (`index` and `rule_id` tell you which). Fix it
with `PUT`, or remove it with `DELETE`; both work while other rules are valid.

### Status codes

| Status | When |
| --- | --- |
| 200 | OK (also `POST /validate` and `POST /test`, whatever the result) |
| 201 | Rule created |
| 400 | Body is not one valid JSON object, has unknown keys, a wrong type, a malformed time, or data after the object |
| 404 | `PUT`/`DELETE` of an unknown rule id |
| 409 | `POST /rules` with an id that already exists (`field: "id"`) |
| 413 | Body larger than the limit below |
| 422 | Well-formed JSON that fails validation. `data.issues` says what |
| 429 | Rate limit (shared by all of `/api/v1`) |
| 500 | The rules file cannot be read (for example hand-corrupted YAML) or written. `error` is generic; details are in the server log |
| 503 | Rules are not available in this build, or a `/test` run was cancelled |

## Data types

### Rule (request)

```json
{
  "id": "invoices",
  "when": "Ext == \".pdf\" && Snippet contains \"Invoice\"",
  "action": "move",
  "target": "Finance/Invoices",
  "reason": "PDF mentioning an invoice"
}
```

| Field | Type | Rules |
| --- | --- | --- |
| `id` | string | Required for `POST`. Letters, digits, `.`, `_`, `-`; starts with a letter or digit; unique; at most 64 characters. Not one of the reserved words `order`, `validate`, `test`, `suggestions`. Cannot be changed later. |
| `when` | string | Required. An expression with a boolean result over the Facts fields (see below). At most 1000 bytes and 200 syntax nodes. |
| `action` | string | `move`, `tag`, `index`, `ignore` or `ask_user`. |
| `target` | string | `move`: folder relative to the watched root, `/`-separated, no `..`, not absolute. `tag`: the tag. Must be empty for the other actions. At most 512 bytes. |
| `reason` | string | Optional. Shown to the user; defaults to `matched rule <id>`. At most 200 characters. Do not put file content in it. |

### Rule (response)

The same five fields (always present, `target` and `reason` are `""` when
empty), plus:

| Field | Type | Meaning |
| --- | --- | --- |
| `valid` | boolean | `false` if the stored rule does not compile (only possible after hand-editing the file). |
| `issue` | object or `null` | `{field, message}` when `valid` is `false`. |

### Facts (for `POST /rules/test`)

`Facts` describes one file. All keys are optional. The names a rule's `when`
expression uses are the Go field names (`Path`, `RelPath`, `Ext`, `MIME`,
`Size`, `Modified`, `EXIF`, `Snippet`, `Vector`); the JSON keys are snake case:

| JSON key | Type | `when` name | Notes |
| --- | --- | --- | --- |
| `path` | string | `Path` | Absolute path. At most 4096 bytes. |
| `rel_path` | string | `RelPath` | Relative to the watched root. At most 4096 bytes. |
| `ext` | string | `Ext` | **Lower case with the leading dot**: `".pdf"`, not `"pdf"` or `".PDF"` (422 otherwise). |
| `mime` | string | `MIME` | For example `"application/pdf"`. At most 4096 bytes. |
| `size` | integer | `Size` | Bytes, 0 or more. |
| `modified` | string | `Modified` | RFC 3339 time, for example `"2026-01-02T03:04:05Z"`. |
| `exif` | object of strings | `EXIF` | At most 64 entries; keys and values at most 4096 bytes. |
| `snippet` | string | `Snippet` | Text excerpt, at most 32768 bytes. It is file content: the server never logs it and never returns it. |
| `vector` | array of numbers | `Vector` | Embedding, at most 4096 elements. |

### Limits

| Limit | Value | Result when exceeded |
| --- | --- | --- |
| Rules per file | 1000 | 422 on `POST /rules`, issue field `rules` |
| `when` length | 1000 bytes | 422, issue field `when` |
| Body: single rule (`POST /rules`, `PUT /rules/{id}`, `POST /rules/validate`) | 16 KiB | 413 |
| Body: `PUT /rules/order` | 128 KiB | 413 |
| Body: `POST /rules/test` | 512 KiB | 413 |

`GET /rules` returns the rule-level limits in `data.limits`, so clients need
not hard-code them.

---

## Endpoints

### `GET /api/v1/rules`

List all rules in evaluation order.

```json
{
  "data": {
    "rules": [
      { "id": "invoices", "when": "...", "action": "move", "target": "Finance/Invoices",
        "reason": "", "valid": true, "issue": null }
    ],
    "limits": {
      "max_rules": 1000, "max_expression_length": 1000, "max_expression_nodes": 200,
      "max_id_length": 64, "max_target_length": 512, "max_reason_length": 200
    }
  },
  "error": null, "meta": { "timestamp": "..." }
}
```

`rules` is `[]` when there are none, never `null`. A missing `rules.yaml`
means no rules. Errors: 500.

### `POST /api/v1/rules`

Create a rule; it is appended to the end of the list. Body: a Rule (request).

- 201: `data` is `{"rule": <Rule response>, "reloaded": true|false}`.
- 400, 413, 422 (any field; also the 1000-rule limit), 409 (duplicate id), 500.

### `PUT /api/v1/rules/{id}`

Replace a rule completely (all fields; omitted `target`/`reason` become empty).
`id` in the body is optional; if present it must equal the one in the URL
(rules cannot be renamed; delete and create instead). The rule keeps its
position.

- 200: `data` is `{"rule": <Rule response>, "reloaded": true|false}`.
- 400, 404, 413, 422, 500.

### `DELETE /api/v1/rules/{id}`

- 200: `data` is `{"deleted": "<id>", "reloaded": true|false}`.
- 404, 500.

### `PUT /api/v1/rules/order`

Set the evaluation order. The list must contain every existing rule id exactly
once.

```json
{ "order": ["skip-temp", "invoices", "receipts"] }
```

- 200: `data` is `{"rules": [<Rule response>...], "reloaded": true|false}`, in
  the new order.
- 400, 413, 500.
- 422 with one issue (`field: "order"`) per problem: `unknown id "x"`,
  `duplicate id "x"`, `missing id "x"...`. Nothing is changed.

If a rule is added or removed between the client's `GET` and `PUT order`, the
call fails with 422 (`missing` or `unknown`). Refetch and retry.

### `POST /api/v1/rules/validate`

Check a rule without saving it. Meant for live feedback while the user types,
so `id` and `action` may be left out: only the supplied fields are checked,
`when` is always required. `target` without an `action` is an error. Id
uniqueness is *not* checked (create returns 409).

Body: a Rule (request), all keys optional.

```json
{ "when": "Ext == \".pdf\" &&" }
```

Always 200 for a well-formed request (400/413 otherwise):

```json
{ "data": { "valid": false,
            "issues": [ { "field": "when", "message": "invalid expression: ..." } ] },
  "error": null, "meta": { "timestamp": "..." } }
```

`error` is `null` here even when `valid` is `false`: an invalid rule is a
successful answer to "is this valid". `issues` is `[]` when valid.

### `POST /api/v1/rules/test`

Dry run. Compiles the supplied rule and evaluates it against the supplied
Facts. Nothing is saved and the live engine is untouched. The response never
contains the Facts.

```json
{
  "rule":  { "when": "Ext == \".pdf\" && Snippet contains \"Invoice\"",
             "action": "move", "target": "Finance/Invoices",
             "reason": "PDF mentioning an invoice" },
  "facts": { "rel_path": "scan.pdf", "ext": ".pdf", "snippet": "Invoice 42" }
}
```

`id`, `reason` and the rest of `rule` follow the same leniency as
`/validate` (`id` defaults to `preview`). Both `rule` and the Facts are
validated first: problems give 422 with issues (`rule`/`when`/... or
`facts.<name>`).

200, match:

```json
{ "data": { "matched": true,
            "reason": "PDF mentioning an invoice",
            "decision": { "action": "move", "target": "Finance/Invoices",
                          "confidence": 1, "reason": "PDF mentioning an invoice",
                          "source": "rule:preview" },
            "evaluation_error": null },
  "error": null, "meta": { "timestamp": "..." } }
```

200, no match: `matched: false`, `decision: null`, `evaluation_error: null`,
`reason: "the rule did not match these facts"`.

200, the rule compiled but failed at run time on these Facts (for example a
modulo by zero): `matched: false`, `decision: null`, `reason` explains, and
`evaluation_error` is a generic message (it never quotes file content). The
engine treats such a rule as "not matching".

### `GET /api/v1/rules/suggestions`

Placeholder for rules suggested from the user's corrections (a later
feature). Currently always:

```json
{ "data": { "suggestions": [] }, "error": null, "meta": { "timestamp": "..." } }
```

The shape of the list items is not defined yet. Treat the list as opaque and
show nothing while it is empty.

---

## `reloaded`

Mutating calls return `"reloaded": true` when the new rule list has been
applied to the live engine (hot reload; the next file decided already uses it)
and `false` when the server has no engine attached, in which case the change is
stored but takes effect after the next restart. A client can ignore it.

## OpenAPI snippet

```yaml
openapi: 3.0.3
info: { title: Arca rules API, version: "1" }
servers: [{ url: "http://localhost:44742/api/v1" }]
paths:
  /rules:
    get:  { summary: List rules, responses: { "200": { $ref: "#/components/responses/RuleList" } } }
    post:
      summary: Create a rule (appended)
      requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/RuleInput" } } } }
      responses:
        "201": { $ref: "#/components/responses/RuleSaved" }
        "400": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Issues" }
        "413": { $ref: "#/components/responses/Error" }
        "422": { $ref: "#/components/responses/Issues" }
  /rules/{id}:
    parameters: [{ name: id, in: path, required: true, schema: { type: string } }]
    put:
      summary: Replace a rule
      requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/RuleInput" } } } }
      responses:
        "200": { $ref: "#/components/responses/RuleSaved" }
        "404": { $ref: "#/components/responses/Error" }
        "422": { $ref: "#/components/responses/Issues" }
    delete:
      summary: Delete a rule
      responses: { "200": { description: Deleted }, "404": { $ref: "#/components/responses/Error" } }
  /rules/order:
    put:
      summary: Reorder rules (must list every id once)
      requestBody:
        required: true
        content: { application/json: { schema: { type: object, required: [order], properties: { order: { type: array, items: { type: string } } } } } }
      responses: { "200": { description: New order }, "422": { $ref: "#/components/responses/Issues" } }
  /rules/validate:
    post:
      summary: Validate a rule without saving
      requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/RuleInput" } } } }
      responses: { "200": { description: "data: {valid, issues}" } }
  /rules/test:
    post:
      summary: Dry-run a rule against Facts
      requestBody:
        required: true
        content: { application/json: { schema: { type: object, required: [rule], properties: { rule: { $ref: "#/components/schemas/RuleInput" }, facts: { $ref: "#/components/schemas/Facts" } } } } }
      responses: { "200": { description: "data: {matched, reason, decision, evaluation_error}" }, "422": { $ref: "#/components/responses/Issues" } }
  /rules/suggestions:
    get: { summary: Suggested rules (always empty for now), responses: { "200": { description: "data: {suggestions: []}" } } }
components:
  schemas:
    RuleInput:
      type: object
      additionalProperties: false
      properties:
        id: { type: string, maxLength: 64, pattern: "^[A-Za-z0-9][A-Za-z0-9._-]*$" }
        when: { type: string, maxLength: 1000 }
        action: { type: string, enum: [move, tag, index, ignore, ask_user] }
        target: { type: string, maxLength: 512 }
        reason: { type: string, maxLength: 200 }
    Facts:
      type: object
      additionalProperties: false
      properties:
        path: { type: string }
        rel_path: { type: string }
        ext: { type: string, pattern: "^\\.[^A-Z]+$", description: lower case, leading dot }
        mime: { type: string }
        size: { type: integer, minimum: 0 }
        modified: { type: string, format: date-time }
        exif: { type: object, additionalProperties: { type: string }, maxProperties: 64 }
        snippet: { type: string, maxLength: 32768 }
        vector: { type: array, items: { type: number }, maxItems: 4096 }
    Issue:
      type: object
      required: [field, message]
      properties:
        field: { type: string }
        message: { type: string }
        index: { type: integer, minimum: 0 }
        rule_id: { type: string }
  responses:
    RuleList: { description: "data: {rules: [Rule], limits}" }
    RuleSaved: { description: "data: {rule, reloaded}" }
    Error: { description: "Envelope with error string and data null" }
    Issues: { description: "Envelope with error string and data: {issues: [Issue]}" }
```
