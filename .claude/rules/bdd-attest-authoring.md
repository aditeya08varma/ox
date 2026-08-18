# Rule: Authoring customer-intent BDDs for the ox CLI (Attest-backed)

**Scope:** the `.feature` acceptance corpus under `tests/acceptance/` and the
`ox attest` capability ladder that reads it (`internal/attest/`). Read this
before adding, editing, or "proving" any `.feature` capability, or before wiring
one to a test run.

---

## 1. The altitude — what these BDDs test, and what they do NOT

**A BDD here is an end-to-end, customer-facing flow test. It asserts what a
coworker *experiences* at the terminal and in the product — not how the code is
built.** It is a *different lens*, at a *different level*, than the unit and
integration tests. It is **not a replacement** for either.

| Lens | Question it answers | Lives in | Example |
|---|---|---|---|
| **BDD / capability** (this rule) | "Can the customer do the thing, see it work, and recover when it fails?" | `tests/acceptance/**/*.feature` (+ its executable proof) | *Aborting a recorded session removes it and all its summarized data — from the Ledger, not just my machine.* |
| **Integration** | "Do these real subsystems, wired together against a real git remote / real files, keep the contract?" | `cmd/ox/*_test.go` E2E tests (real bare remote, real `pushLedger`) | `TestAbort_KillsFinalizedSessionAndAllSummarizedData` drives `runAgentSessionAbort` and asserts on a bare remote. |
| **Unit** | "Does this function return the right value for each input, including the failure paths?" | `internal/**/*_test.go`, table-driven | `TestResolveSessionRecording_PrecedenceMatrix`. |

The unit test proves a helper; the integration test proves the subsystems
interoperate; the **BDD proves the customer promise still holds end to end.** A
capability is not covered because a unit test exists — it is covered when the
*promise* has a red-first proof. Derive the mechanics tests separately (see
`ox-attest-goal` skill step 4).

**Write the customer journey first, the mechanics never.** State the customer's
goal, starting point, natural action, the feedback they see, and how they
continue or recover. Preserve an established journey — do not silently redesign
it to make a scenario easier to automate.

---

## 2. The unit is the CAPABILITY = one Gherkin `Rule:` block

`ox attest` counts **capabilities, not tests**. A capability is one `Rule:`
block — "a claim a customer would recognize." One coherent customer promise per
`Rule:`. The capability `ID` (`<domain>/<feature>#<rule-slug>`) is **derived by
the corpus scanner — never author it**.

Authoring conventions (enforced by `internal/attest/corpus.go` + the corpus
README, `tests/acceptance/README.md`):

- **Layout:** `tests/acceptance/features/<domain>/<name>.feature`. `ScanCorpus`
  reads the `features/` subdir only. Author new capabilities there.
  *(The repo's pre-existing flat `tests/acceptance/<domain>/` files predate this;
  migrating them into `features/` is tracked separately — do not mass-move them
  in an unrelated change.)*
- **`Feature:`** opens with 2–4 sentences of user-facing prose, then `See also:`
  cross-references. No `Background:` blocks.
- **Personas** are named (`Devon`, `Avery`, `Sam`, `Riley`, `Quinn`) — never
  "the user." Each scenario name starts with actor + action.
- **Observable language only.** Say "the plan is rendered as a SageOx
  team-context-optimized HTML page," not the Go function that renders it.
  **Never** put Go function names, file paths, internal structs, routes, status
  codes, database rows, selectors, or validation regexes in Gherkin.
- **Cover the happy path plus the consequential failure, permission, and
  continuity cases** — not every internal error code (those are unit/integration
  scope, per the corpus README's "Scope discipline").
- **Tags** are matched by exact equality (`@pending-migration` is NOT
  `@pending`). Vocabulary: `@validated` (claims green, pairs with a
  `# validated:` stamp comment), and the "do-not-dispatch" set `@wip`,
  `@pending`, `@speculative`. Tag a scenario you have not yet proven `@wip` so it
  does not falsely inflate the ladder.

---

## 3. The proof ladder — no stamp without evidence

`ox attest status` places every capability on a worst-first honest ladder:
`untested → skipped → unproven → stamped → stale → attested`. A capability
reaches **`attested` only with a committed record carrying a clean, red-first
proof** (`ox attest record`). A green `@validated` stamp with no backing record
sits at `stamped` — a claim, not a proof.

**Prove red-first, every time** (mirrors `ox-attest-create`):

1. Break the product so the scenario fails **on the step that names the
   customer claim**. Save the *unedited* failure text and the red run id.
2. Restore; run the same scenario **green**; save the green run id.
3. `ox attest record --capability <id> --break "…" --red-run <id>
   --green-run <id> --red-verbatim-file <f> --landed-on-claim-step --surface
   <file>…`. The command derives the verdict — `clean` only if the failure
   landed on the claim step — and refuses a dirty tree. A break that lands
   somewhere other than the claim step is honestly recorded as `ambiguous`, not
   forced clean.
4. `ox attest check --json` before handoff to see what your working diff
   invalidated; `ox attest publish --to <dir>` to bundle for the hosted team
   view.

**A gate nobody watched fail is a gate nobody has tested.** Report both the red
and the green in the PR.

---

## 4. What "proof" means in ox *today* vs. once the runner is wired

ox ships the attest **reader** (`internal/attest` + `ox attest …`) but the
external `@sageox/attest` **compiler + runner** that emits `compiled/<feature>.plan.json`
and `tests/bdd/runs/<runId>/…` is **not wired into this repo yet** (see the
corpus README: "no runner wired"). Until it is:

- **The executable proof of a capability is a Go E2E test** in `cmd/ox/*_test.go`
  that drives the real command surface (real bare remote, real `pushLedger`,
  real `ox config set`) and asserts the customer-observable outcome. The
  `.feature` `Rule:` is its human-readable mirror; the Go test carries the
  red-first proof (break the branch → test red → restore → green).
- **When the runner is wired**, the same `.feature` scenarios dispatch through
  it, and `ox attest record` mints the durable attestation from the runner's
  red/green run ids. Author the `.feature` now so that transition is a wiring
  step, not a rewrite.

The runner lives in a **separate repository that is private today**. Do **not**
read, quote, or copy its source into this public repo (see
`.claude/rules/private-source-boundary.md`). Build only against the public
file-format seam that `internal/attest` already documents
(`compiled/**/*.plan.json`, `tests/bdd/runs/<runId>/{run.json,report/results.json,run-report.json}`).
The actual runner integration is a separate, blocked task.

---

## 5. No test theater

Each capability must state, in one line, **the real customer failure it
prevents** and **the observable difference** its proof asserts (a Ledger commit
that appears or doesn't, a `/c/<id>` link that resolves or 404s, a commit
trailer present or absent, a secret that reaches the remote or doesn't). If the
proof would pass with the feature removed, it proves nothing. A `.feature` with
no executable proof (Go E2E today, runner later) is a claim at rung `stamped` at
best — say so, do not present it as covered.

For a settings-driven capability, the proof drives the **real `ox …` command**
and observes the **downstream flow**, never a resolver function in isolation —
that observation is the whole point of the lens.

---

## See also

- `tests/acceptance/README.md` — corpus structure, conventions, personas, scope discipline.
- `extensions/skills/ox-attest-goal/SKILL.md` — customer-journey-first authoring.
- `extensions/skills/ox-attest-create/SKILL.md` — turning a red/green run pair into an honest proof.
- `internal/attest/{corpus,ladder,record,freshness,publish}.go` — the reader + ladder + record format.
- `.claude/rules/testing.md` — the unit/integration philosophy this rule sits above.
- `.claude/rules/private-source-boundary.md` — why the runner's source stays out of this repo.
