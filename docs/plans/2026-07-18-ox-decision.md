# `ox decision` — Decision Records: discover, index, query, enrich (v1)

## Context

Teams record decisions in Decision Records (DRs — ADRs are one type), but those documents go stale, get cited wrongly, and never absorb the team context that actually produced them. The motivating evidence is on the record:

- sageox-mono **ADR-061**: code shipped citing a phantom "locked decision #9" that existed in no decision record — an entire ADR had to be written to expunge it. Citations to decisions must be first-class, resolvable, verifiable.
- This repo's own `docs/adr/` has **three duplicated ADR numbers** (018 ×2, 019 ×2, 023 ×2) plus unnumbered `adr-*.md` strays — nobody notices because nothing indexes the corpus.
- SageOx discussions: "the conversations are where the decisions are being made… and recorded" (2026-05-09); "teams never lose a decision again" (Ox Scout, 2026-05-28). Voice discussions already yield pre-attributed decisions (`discussions/*/summary.json` → `Decisions[]{Description, Owner, Context}`) that never reach a committed DR.

`ox decision` reserves a namespace for processing DRs: **discover** them from configured multi-source corpora, **index** them tagged as decisions, make them **fast to query** for agents, and **enrich** the creation/update of DRs with credited team context (voice discussions, prior coding sessions, knowledge-bubble distillates). `ox agent prime` primes every coding agent (Claude, Codex, Droid) to consult and credit team context whenever it touches a DR.

**Doctrine (inherited, non-negotiable):**
- **ADR-021 context-not-inference**: ox computes deterministic signals + context bundles locally, zero LLM/network-judge calls; the agent authors every word and every judgment. ox never edits a DR file.
- **ADR-024 retrieval**: local = lexical/structured (no local embeddings); semantic knn stays server-side via `ox query`. "Fast RAG" v1 = lexical field-weighted search over a structured catalog.
- **Marker doctrine** (CHANGELOG 0.11.0): a surfaced item means "SageOx has context on this," never a verdict. Related/conflicting/superseding is the agent's call; ox emits `relation: "candidate"` only.
- **Thin-relay rule** (docs/specs/skill-activation-design.md): all behavioral guidance lives in CLI JSON `guidance` + prime output (Layer 1, reaches all agents); the Claude skill is a thin relay.

## Command surface (v1)

`decisionCmd` — pure cobra group, self-registered in `cmd/ox/decision.go` `init()`, `GroupID = "dev"`, mirroring `planCmd` (`cmd/ox/plan.go:26,888-920`).

| Command | Output default | Flags | Purpose |
|---|---|---|---|
| `ox decision index` | human summary; `--json` stats | `--force` | Discover + (re)build catalog; prints diagnostics (duplicate numbers, dangling refs) — this is v1's lint surface |
| `ox decision list` | table; `--json` | `--status`, `--corpus` | Browse the catalog |
| `ox decision show <id\|path>` | metadata + body | `--json`, `--meta` | Resolve "ADR-021" / "021" / path → record; the RAG read path |
| `ox decision query "<terms>"` | table; `--json` | `-k`, `--status`, `--corpus` | Fast lexical decision search (zero-network; rhymes with `ox query --local`) |
| `ox decision enrich` | **JSON by default**; `--text` | `--file <dr.md>` \| stdin \| `--topic "<subject>"` | Context bundle + signals + guidance for creating/updating a DR |
| `ox decision resolve "<ref>"` | JSON; exit 0/1 | `--all <path>` | Verify any citation ref resolves; did-you-mean on failure; `--all` verifies every ref in a file (CI-friendly) |
| `ox decision gaps` | table; `--json` | `--dismiss <ref>` | Uncodified decisions: voice-meeting `Decisions[]` with no matching DR; durable dismissals in ledger cache |

Cut from v1: `sync` (index is incremental), `lint` (diagnostics ride `index`; `--strict` CI verb is phase 2), `new` scaffold, `render`. Seven verbs total (six + `gaps`).

## Config: where DRs live (`.sageox/config.yaml`, committed)

New `internal/config/decision.go` (pattern: `internal/config/plan.go`); pointer field `Decision *DecisionConfig` on `ProjectConfig` beside `Plan` (`project_config.go:152`).

```yaml
decision:
  sources:
    - name: repo              # this repo's own DRs
      type: repo              # default type
      paths: ["docs/adr"]     # dirs (recursive *.md) or doublestar globs
    - name: platform          # company-wide ADR repo shared as a knowledge bubble
      type: kb
      kb: platform-adrs       # bubble slug → resolved via paths.KBDir (daemon-cloned, sparse, doctored)
    - name: team              # legacy team-context docs/ (classifyDocKind adr/decision;
      type: team              # includes e.g. the team's principles/constitution doc — surfaced by ox plan enrich on this very plan)
```

- **Zero-config default**: one implicit `repo` source scanning `["docs/adr", "docs/decisions", "adr", "docs/architecture/decisions"]` — only dirs that exist; parser admits only files yielding a title.
- **Cross-repo** rides knowledge bubbles (structurally identical rail: server-provisioned RepoURL, daemon sparse-clone, per-project symlink, doctor checks). `team`-type bubbles auto-link into every project — the natural home for a company-wide ADR corpus. Provisioning an ADR repo as a bubble is server-side (mono) work; the CLI only reads the slug.
- **`type: https`**: field reserved, explicitly rejected in v1 with "https sources land in a later release" (no fetch-and-cache-docs rail exists today — honestly deferred to phase 2).
- Validation: unknown type / kb-without-slug / absolute or `..` paths / duplicate names → error. Order = cross-corpus precedence for bare-ID resolution.
- New dep: `github.com/bmatcuk/doublestar/v4` (MIT — allowed) to honor globs literally.

> **[Ryan review — required]** data-location config: `decision.sources` shape, default glob list, catalog path below.

## Catalog: the "special index"

**Single JSON catalog** at ledger **`.sageox/cache/decisions/catalog.json`** (sanctioned derived-data home per `.claude/rules/ledger-cache.md`; codedb precedent; fallback to project `.sageox/cache/` when no ledger). No SQLite/Bleve at ~100s of docs — load+scan <50ms; revisit at thousands (documented in package doc, ledgersearch-style).

Per-DR `Record`, extracted deterministically (zero LLM): `ID, Number, Title, Status, Date, Deciders[], Corpus, Path, RelPath, Mtime, ContentHash, DSections[] (D1..Dn anchors), Amendments[] (dated **Amendment (YYYY-MM-DD):** markers), Refs[] (ADR tokens, word-bounded regex from internal/plan/annotate.go), Supersedes[], Excerpt`.

Catalog carries `diagnostics[]`: `duplicate-number`, `unnumbered`, `missing-status`, `dangling-ref`, `superseded-no-successor`.

**Freshness**: every read path calls `decision.LoadFresh(sources)` — stat-walk roots, diff mtime/size against catalog, re-parse only changed files, atomic temp+rename write. Corrupt/old-schema → silent full rebuild (fail-open). No daemon involvement in v1.

**Query scoring**: ledgersearch-style lexical (tokenize, AND semantics, tf bonus), field-weighted — exact-ID 1.0 short-circuit; title ×3; D-headings/status/deciders ×2; body ×1; deterministic tie order. One scorer in `internal/decision/search.go` shared by `query` + enrich detectors (+ phase-2 plan bundle).

## Citations: ox owns the strings, the agent owns placement

For rendered plans the render owns markers; a committed DR has no render step. Resolution: **enrich emits ready-to-paste citation strings; the agent never composes a ref by hand.** Fabrication becomes impossible by construction — ox only emits refs it just resolved in the index/corpora.

House convention — visible prose credit + invisible machine ref:

```markdown
Per alice's "Ox Scout" discussion (2026-05-28), losing decisions in
conversation is the failure this system exists to prevent.
<!-- SOURCE: sageox discussion:2026-05-28-1423-alice#ch-2 -->
```

Ref vocabulary (stable, greppable via `SOURCE: sageox`, resolvable via `ox decision resolve`):

| Scheme | Example |
|---|---|
| `discussion:<dir>[#ch-N]` | `discussion:2026-05-28-1423-alice#ch-2` |
| `session:<name>` | `session:2026-07-02T09-14-ryan-a1b2` |
| `commit:<sha>` | full SHA, real hashes only (codedb cited-only rule) |
| `adr:<repo-relative-path>[#D<n>]` | `adr:docs/adr/ADR-046-x.md#D5` (path canonical — numbers collide; prose uses "ADR-046 D5") |
| `kb:<bubble>/<relpath>` | `kb:platform/agent-context/distilled-discussions.md` |

- Murmurs: surfaced for awareness, **never** get a `cite` (~12h retention, non-durable).
- Readers without SageOx see plain English (names, dates, titles), working relative links, invisible HTML comments. No `sageox://` URLs in visible prose.
- **SageOx credit in a committed DR** (per Ryan, 2026-07-18): subtle by default — the `sageox` token in SOURCE comments + the existing scored commit trailer (`Co-Authored-By: SageOx`, session score ≥ moderate) + verbal attribution when presenting. A **visible** SageOx credit is reserved for genuinely non-obvious context that meaningfully changed the decision — the agent's judgment, prompted by `guidance`, never automatic. When earned, it is **subtle, plan-style**: one restrained line beside the relevant Reference entry or a single footer line (mirroring the plan render's "Guided by SageOx" footer) — never inline marketing prose woven through the body. **Hard cap: ≤2 visible SageOx credits per DR; 3 only when SageOx context meaningfully steered the decision process.** Enforced two ways: `guidance` states the cap; enrich on file input counts visible SageOx-credit phrases and emits a `sageox-credit-overflow` advisory diagnostic above the cap. Teammate credits (names/dates) are never capped. The document belongs to the team; SageOx earns visible credit only when it materially helped the team decide better.

## `ox decision enrich` — package + JSON

`internal/decision/` mirrors `internal/plan/` exactly: global Detector/Retriever registry, `init()` self-registration, fail-open orchestrator with panic recovery (`internal/plan/enrich.go:35-142` as model). Files: `types.go, sources.go, parse.go, catalog.go, search.go, enrich.go, input.go, related.go, numbering.go, refs.go, drift.go, discussions.go, sessions.go, murmurs.go, guidance.go`.

Result shape (merged; plan-Result conventions — `schema_version`, cap ~12 / floor ~0.55 on context):

```json
{
  "schema_version": "v1",
  "decision": {"id": "", "suggested_id": "ADR-027", "title": "…", "status": "Proposed", "corpus": "repo"},
  "conventions": {"dir": "docs/adr", "filename_pattern": "ADR-NNN-slug.md", "next_number": 27,
                  "number_collisions": ["018","019","023"], "statuses_observed": ["Draft (Proposed)","Accepted"],
                  "sections_observed": ["Context","Decision","Consequences"],
                  "amendment_marker": "**Amendment (YYYY-MM-DD):**", "decision_anchors": "D1..Dn"},
  "annotations": [
    {"kind": "deterministic", "type": "related-decision", "ref": "ADR-017", "ref_path": "docs/adr/ADR-017-….md",
     "anchor": "D4", "relation": "candidate", "why": "ADR-017 (Accepted, 2026-03-02) D4 covers KB provisioning; draft overlaps"},
    {"kind": "deterministic", "type": "numbering", "why": "next free number in corpus 'repo' is 027; 018/019/023 duplicated — do not add a third"},
    {"kind": "deterministic", "type": "drift", "files": ["internal/kb/resolve.go"], "source_url": "commit:8f41c2d…",
     "why": "2 files cited by this DR changed after its date (latest 2026-07-02)"},
    {"kind": "deterministic", "type": "unresolved-ref", "rule": "dangling-ref",
     "why": "'ADR-046 D9' does not resolve — ADR-046 defines D1–D7; nearest: D5"}
  ],
  "context": [
    {"kind": "discussion-decision", "title": "Sparse clone default", "ref": "discussion:2026-06-30-…", "score": 0.84,
     "author": "Person A", "when": "2026-06-30", "snippet": "…",
     "cite": {"prose_hint": "Per Person A's 2026-06-30 architecture discussion, …",
              "comment": "<!-- SOURCE: sageox discussion:2026-06-30-… -->"}},
    {"kind": "decision", "title": "ADR-017 — …", "ref": "docs/adr/ADR-017-….md", "score": 0.91, "when": "2026-03-02", "cite": {…}},
    {"kind": "session", "…": "…", "cite": {…}},
    {"kind": "kb", "…": "…", "cite": {…}},
    {"kind": "murmur", "…": "… (no cite field)"}
  ],
  "signals": {"related": 2, "discussion_decisions": 1, "prior_sessions": 1, "murmurs": 1,
              "diagnostics": 1, "unresolved_refs": 1, "material": true},
  "guidance": "…"
}
```

**v1 detectors/retrievers** (all deterministic, fail-open):

| Name | Fires on | Source |
|---|---|---|
| `related-decisions` | topic/draft/file | catalog via shared scorer; `supersede-candidate` variant when overlapping an Accepted DR's D-section |
| `numbering` | draft lacks ID / number taken | catalog |
| `catalog-diagnostics` | always | catalog diagnostics relevant to input |
| `unresolved-refs` | file/draft input | SOURCE comments + ADR/D-token patterns checked against catalog — the ADR-061 class, mechanized |
| `credit-cap` | file/draft input | counts visible SageOx-credit phrases; >2 → `sageox-credit-overflow` advisory (cap 3 when agent judged SageOx steered the decision) |
| `drift` | file input | file paths cited in DR body × codedb commits after DR date (fail-open when codedb absent) |
| `discussion-decisions` | always | `discussions/*/summary.json Decisions[]{Description,Owner,Context}` — **no Go reader exists yet; write fail-open against the cloud shape, fall back to `memory/*.md`, verify against a live team-context checkout during implementation** |
| `prior-sessions` | always | `ledgersearch.Search` (sessions, plans) |
| `recent-decision-murmurs` | always | `ledger.ReadMurmursInWindow`, awareness only |

**`guidance` string** (`buildGuidance`, leads with specific evidence — plan's `guidanceLead` pattern), four branches: new-DR-rich-context (weave + credit + reconcile, use `suggested_id`); new-DR-zero-context ("no prior team decision found as of <date>" is itself a citable claim — a gap admitted beats a citation invented); update-drift (decision stands → dated amendment; else amend/supersede — your call); update-unresolvable-refs (fix or delete before committing).

## Agent choreography (the answer to "what/how do agents pull team context")

**Creating a new DR** ("write an ADR for X"):
1. `ox decision enrich --topic "X"` before drafting (consult-first).
2. Read returned conventions + related-DR candidates (Read the actual files via `ref_path`) + voice-meeting decisions (pre-attributed Owner/date) + session prior art + kb distillates.
3. Draft, weaving context per section map: **Context** ← discussion decisions + kb distillates; **Decision** ← agent's synthesis (name the decider when formalizing a voice decision); **Alternatives** ← who proposed what; **Consequences** ← learnings; **References** ← related DRs (relative links) + sessions + real commit SHAs. Paste `cite.comment` verbatim beside each credited claim.
4. `ox decision enrich --file <draft>` (or stdin) before presenting — verifies refs, checks numbering.
5. Present with verbal attribution ("SageOx surfaced [name]'s discussion…"); commit trailer rides session score.

**Updating an existing DR**: `ox decision enrich --file <path>` → drift + newer context + amendment anchors + unresolved refs. On an **Accepted** DR: dated `**Amendment (YYYY-MM-DD):**` marker, never a silent rewrite; on a Draft: revise freely. Aligns/amends/supersedes stays the agent's judgment.

## Prime, skill, docs

- **`cmd/ox/agent_prime_xml.go`**: new `writeDecisionRecordGuidance(&sb, output.AgentType)` called after the plan-enrichment call (:152), inside the pre-`bk.charge` block. **Gated: emitted only when a decision corpus is detected** (config present or default dirs exist) — zero token cost for repos without DRs (~120 tokens otherwise). Block copy (drafted, final wording at implementation):

  > Decision records (ADRs — docs/adr/**, docs/decisions/**) are the team's permanent memory. Creating or editing one is a consult-first event.
  > BEFORE drafting a new DR: `ox decision enrich --topic "<subject>"` — repo conventions, related decisions, voice-meeting decisions with owner and date, ready-to-paste citations. Weave them in WHILE drafting, not after.
  > BEFORE editing an existing DR: `ox decision enrich --file <path>` — drift, newer context, amendment anchors, refs that no longer resolve.
  > Crediting: name the teammate and date in visible prose; paste the matching `<!-- SOURCE: sageox … -->` comment from enrich output. NEVER compose a source ref by hand. Whether a decision aligns with, amends, or supersedes another is YOUR call — ox only surfaces candidates.
  > Amendments: on an Accepted DR add a dated amendment marker; never silently rewrite history.
  > Verify: every ref must resolve (`ox decision resolve "<ref>"`). A citation you cannot resolve is a citation you delete. A gap admitted beats a citation invented.
  > Capture: when uncodified team decisions exist (`ox decision gaps`), raise it once at a planning or wrap-up boundary — with the evidence, never mid-task. Not every choice is a DR; a dependency bump is not an ADR.
  > SageOx credit rides the commit trailer via your session score. Visible credit only when surfaced context was genuinely non-obvious and changed the decision — and then subtle, plan-style: one restrained line in References or a single footer line, never inline marketing. Max 2 visible SageOx credits per DR (3 only if SageOx meaningfully steered the decision process).

- **`internal/prime/guidance.go`**: `IntentCommand` row — "create/update a decision record (ADR), or find why something was decided" → `ox decision`.
- **`internal/prime/capability_table.go`**: consult-first route — "a DR/ADR is being created, edited, or cited by number" → `ox decision enrich <path|--topic>` / `ox decision resolve` (note: `TestConsultRoutes_NoDriftWithSkill` requires syncing the ox-consult skill description).
- **Skill**: `extensions/claude/skills/ox-decision/SKILL.md` — thin relay (activation description + "run `ox decision enrich`, follow its `guidance`"). Auto-embedded/stamped/installed by existing machinery.
- **Docs**: `make docs` auto-generates `docs/reference/decision/*.mdx` — all doc effort goes into cobra strings. CHANGELOG entry under Unreleased.

## Decision capture — when ox nudges codification (ADR/DDR)

Should prime encourage agents to *create* DRs when team context holds uncodified decisions? **Yes — evidence-gated, boundary-timed, pull-based. Never mid-flow, never auto-drafted.** Grounding (July 2026): ADRs are resurging precisely because agents refactor away reasoning they can't see; "continuous ADR generation" is the emerging pattern, but standing prompt instructions decay as context grows — the nudge must be actionable at the moment it appears. Nudge research: annoyance = goal interference; frequency × placement × context decide which side of the line you land on.

**The signal is deterministic and already in the corpora**: a discussion's pre-attributed `Decisions[]{Description, Owner, Context}` with no matching DR in the catalog = an **uncodified decision** — computable, cited, zero-LLM. Same for a material plan with zero related DRs (phase 2, plan cross-wire) and a significant session that touched no DR (phase 2, wrap-up hook).

| Surface | Timing | Shape | v1? |
|---|---|---|---|
| `ox decision gaps` (7th verb) | pull | list uncodified decisions (owner, date, discussion ref) + `--dismiss <ref>` (durable, ledger-cache — never re-nagged) | **yes** |
| `index` diagnostics | pull | `uncodified-decision` rows | **yes** |
| prime | ambient, 1 line | "N team decisions have no DR — `ox decision gaps`" (count only, only when >0 after dismissals) | **yes** |
| plan enrich | planning boundary | `capture-candidate` annotation: "this plan decides something no DR covers — consider an ADR/DDR in this PR" | phase 2 |
| wrap-up / session close | natural boundary | one confirm → agent drafts via `enrich --topic`, human reviews | phase 2 |

**Annoying (banned)**: interrupting mid-implementation; auto-drafting or auto-committing DRs; re-nagging dismissed items; flagging trivial choices (only pre-attributed voice decisions, material plans, scored sessions qualify); process theater — guidance says explicitly "not every choice is a DR; a dependency bump is not an ADR."
**Exceptionally useful (the bet)**: the gap list is always computed and one command away; the agent mentions it exactly at planning/wrap boundaries with the evidence attached ("[owner] decided X in the [date] discussion — no DR covers it; draft one?") and acceptance is one confirmation with the enrich context prefilled.

**ADR vs DDR**: catalog records gain `dr_type` (adr | ddr | other) via dir/filename/frontmatter heuristics; nudges suggest the type from the evidence source (architecture vs design). DDR corpora configure like any other source.

## Doctor

- `decision-sources` (`FixLevelCheckOnly`): each configured source resolves (repo dirs match ≥1 file; kb slug resolves + hydrated — model `doctor_kb_repo_health.go`; hint `ox kb hydrate <slug>`).
- `decision-catalog` (`FixLevelSuggested`): catalog fresh vs sources; surfaces duplicate-number/dangling-ref counts; `--fix` rebuilds.

## Tests (patterns to clone)

- `internal/decision/parse_test.go` — table-driven, fixtures in `testdata/`: mono template (Status/Date/Deciders, D1..D9, dated amendment), ox `ADR-021` style, unnumbered `adr-*.md`, numeric-only `002-*.md`, duplicate ADR-018 pair, `superseded/` file, README status-index excluded, frontmatter>H1>filename chain.
- `catalog_test.go` — incremental refresh, atomic write, corrupt → rebuild.
- `search_test.go` — ID short-circuit, field weights, AND semantics, deterministic ties.
- `enrich_test.go` — panicking detector skipped, empty catalog → non-material, **no-network guarantee test** (clone `internal/ledgersearch/no_network_test.go`; ADR-021/024 enforcement).
- Detector tests — numbering gaps/dupes, supersede-candidate threshold, discussions fail-open + fallback, unresolved-refs did-you-mean.
- `cmd/ox/decision_test.go` — golden enrich JSON, `--text`, list table under `NO_COLOR=1`/`COLUMNS=80`.
- `internal/config/decision_test.go` + `cmd/ox/doctor_decision_test.go` — mirror plan/kb siblings.

## Phasing

**v1 — one coherent PR** (draft first, per house rules; branch `ryan/louisville-v9`):
0. **Commit this plan to the branch as `docs/plans/2026-07-18-ox-decision.md`** (new dir) so worktree reviewers see it. During implementation, distill the doctrine parts into a draft `docs/adr/ADR-027-ox-decision-namespace.md` (dogfood: the decision feature's own DR, enriched by its own tooling).
1. `internal/config/decision.go` + validation + tests.
2. `go.mod` doublestar; `internal/decision/` sources+parse+catalog+search + tests.
3. `cmd/ox/decision.go`: group + index/list/show/query/resolve.
4. enrich orchestrator + detectors/retrievers + guidance + tests.
5. enrich verb wiring (JSON default, `--text`).
6. Prime wiring (3 files) + thin-relay skill.
7. Doctor checks.
8. `make docs`, CHANGELOG, `make lint && make test`.

**Beads**: epic `ox decision v1` with tasks per step + `[HUMAN] Ryan review: decision.sources schema, default globs, catalog path, prime token spend`.

**Phase 2** (separate PRs): `lint --strict` CI verb; https sources (daemon fetch + ETag cache — net-new machinery); plan↔decision cross-wire (plan's `gatherTeamContext` consumes the catalog instead of the `classifyDocKind` substring heuristic); PR/murmur collision on DR paths; `ox decision new` scaffold.

**Phase 3 (server/mono follow-ons — out of ox CLI scope, filed as mono issues)**: cloud ingest tagging `doc_type=decision` + a `doc_type` facet on `QueryRequest` (`internal/api/query.go:22` has none) so `ox query` filters decisions cloud-side; provisioning company ADR repos as team-type bubbles (server-side create); supersedes/refs link-graph render.

## Anti-goals (explicit)

No LLM/network-judge in the CLI path · ox never edits a DR · no fabricated citations (cite emitted only for resolved items; murmurs never) · no verdicts (candidates only) · no local vector RAG · no auto-numbering writes · no SageOx branding in DR bodies · no fat skill.

## Verification (end-to-end)

1. `make build` → run `ox decision index` **in this repo** — must discover `docs/adr/`, report the real 018/019/023 duplicates as diagnostics.
2. `ox decision query "plan context inference"` → ADR-021 top hit; `ox decision show ADR-021`.
3. `ox decision resolve "adr:docs/adr/ADR-021-ox-plan-context-not-inference.md"` → exit 0; `ox decision resolve "ADR-046 D9"` → exit 1 + did-you-mean.
4. `ox decision enrich --topic "session streaming pause semantics"` → discussion/session context items with cite blocks; guidance branch sane; `--text` readable.
5. Pipe a draft with a taken number via stdin → `numbering` annotation fires.
6. `ox agent prime` in this repo → `<decision-record-guidance>` present; in a DR-less scratch repo → absent.
7. `make lint && make test` green; golden JSONs stable.

## Open items resolved with Ryan (2026-07-18)

1. **DR-body credit**: subtle by default (invisible SOURCE comments + commit trailer); visible credit only when the context was non-obvious and meaningfully improved the decision — agent's judgment via `guidance`, styled plan-subtle (one restrained References/footer line, like the plan render's "Guided by SageOx"). **Cap: ≤2 visible SageOx credits per DR (3 when SageOx meaningfully steered); `sageox-credit-overflow` diagnostic enforces advisorily.**
2. **Zero-config**: default-on discovery of standard DR dirs + prime block gated on corpus detection. Confirmed.
3. **Verb set**: full six (`index, list, show, query, enrich, resolve`). Confirmed.

Plan-enrich signals on this plan: 0 collisions with open PRs; expert-routing → Ryan owns all touched files; SageOx surfaced the team's Constitution/principles doc as decision-adjacent team context (folded into the `team` source rationale above).
