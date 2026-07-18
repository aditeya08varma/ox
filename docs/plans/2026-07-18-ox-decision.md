# `ox decision` — Decision Records: discover, enrich, verify (v1, as built)

**Status**: v1 implemented on `ryan/louisville-v9` (2026-07-18). This doc records the final scope after four simplification rounds in review, plus the phase 2/3 design directions.

## Context

Teams record decisions in Decision Records (DRs — ADRs are one type, DDRs another), but those documents go stale, get cited wrongly, and never absorb the team context that produced them. Motivating evidence:

- sageox-mono **ADR-061**: code shipped citing a phantom "locked decision #9" that existed in no decision record — an entire ADR had to be written to expunge it. Citations to decisions must be resolvable and verified.
- This repo's own `docs/adr/` turned out to have **nine duplicated ADR numbers** (002, 003, 006, 007, 008, 009, 018, 019, 023 — legacy `002-*.md` colliding with `ADR-002-*.md`); the first live run of the new detector found them all. Nobody notices without tooling.
- SageOx discussions: "the conversations are where the decisions are being made… and recorded" (2026-05-09); "teams never lose a decision again" (Ox Scout, 2026-05-28).

**Doctrine (inherited, non-negotiable):** ADR-021 context-not-inference (ox computes deterministic signals locally, zero LLM/network; the agent authors every word; ox never edits a DR) · ADR-024 retrieval (local = lexical/structured; semantic stays server-side) · marker doctrine (surfaced item = candidate, never a verdict) · thin-relay rule (behavior lives in CLI JSON `guidance` + prime, reaching Claude/Codex/Droid; the Claude skill is a relay).

## v1 shape (after simplification review with Ryan)

**One verb.** `ox decision enrich` — JSON by default, `--text` for humans. Three input modes: `--topic "<subject>"` (consult before drafting), `--file <dr.md>` (existing DR: adds drift + ref verification), stdin (verify a draft before presenting).

**No persisted catalog, no `index`/`list`/`show`/`query`/`resolve`/`gaps` verbs.** Rationale, per review:
- The corpus is hundreds of small files; enrich walks and parses it fresh per call (<50ms). A persisted catalog is machinery without a payoff at this scale.
- **codedb already indexes DR markdown** — `ox code search "context not inference ADR"` returns `docs/adr/ADR-021-…md` today. Fast decision RAG needs no new query surface; a `decision` tag/facet on those results is a codedb fast-follow, not a new command.
- Ref verification lives INSIDE enrich (`--file`/stdin re-checks every ref) — no separate `resolve` verb needed to close the phantom-D9 loop.
- `gaps` (uncodified voice decisions) is deferred with the whole discussion-extraction dependency: the conversation `Decisions[]` structure will change (a first-class "decisions layer" on conversations is coming), so v1 does not lean on it.

**Sources: this repo's committed paths only.** Flat committed config (`.sageox/config.yaml`, team-shared, same rail as the `plan:`/`hooks:` blocks — NOT the ledger; nobody has to create it):

```yaml
decision:
  paths: ["docs/adr", "docs/decisions/**/*.md"]   # dirs (recursive *.md) or doublestar globs
```

Zero-config default: `docs/adr`, `docs/decisions`, `adr`, `docs/architecture/decisions` (existing dirs only). README.md excluded; only DR-shaped files admitted (number, or title + status/date).
- **kb is NOT a DR source** (Ryan ruling): bubbles are dynamically structured memory — hints and references, never authoritative decisions.
- Cross-repo corpora (company-wide ADR repo) and https endpoints: phase 2 (see below).
- `dr_type` (adr | ddr | other) is **inferred** — filename prefix (`ADR-`/`DDR-`), dir names, frontmatter `type:` — no config categorization until real corpora defeat inference.

**What enrich computes** (all deterministic, fail-open, registry-pattern mirroring `internal/plan`):

| Signal | Source |
|---|---|
| `related-decision` candidates (+ `supersede-candidate` variant on Accepted DRs with D-anchors) | fresh corpus walk + field-weighted lexical scorer (title ×3, anchors/status/deciders ×2, excerpt ×1; exact-ID short-circuit) |
| `numbering` (next free, taken-number, duplicate-number diagnostics) | corpus |
| `conventions` block (dir, filename pattern, statuses, template sections, amendment marker, D-anchor style) | corpus |
| `unresolved-ref` (prose `ADR-NNN [Dn]` tokens + `<!-- SOURCE: sageox … -->` machine refs) | corpus |
| `sageox-credit-overflow` (visible credits > 2) | input body |
| `drift` (files the DR cites changed after its date) | `git log --since` per cited path |
| prior sessions/plans (with cites) + murmur awareness (no cites — non-durable) | ledgersearch + ledger murmurs |

**Citations: ox composes, the agent places.** Context items carry ready-to-paste `cite` blocks: prose hint + `<!-- SOURCE: sageox adr:<relpath> -->` / `session:<name>` / `plan:<slug>` machine comments. Agents never compose refs by hand; enrich on the edited file re-verifies everything. Visible SageOx credit: earned only when context was non-obvious and changed the decision, plan-subtle (one restrained References/footer line), **cap 2 per DR (3 only when SageOx meaningfully steered)** — enforced advisorily by the credit-overflow diagnostic.

**Plans tie back to DRs** (in v1): a new `internal/plan` retriever surfaces this repo's DRs relevant to a plan as `adr` context items; `ox plan render`'s existing inline-marker machinery (regex extended to `ADR-`-prefixed basenames) marks their prose mentions. Subtle by construction — a marker, context, never a verdict.

**Prime + skill + doctor:**
- `<decision-record-guidance>` block in `ox agent prime` — **gated on a corpus actually existing** (zero tokens for DR-less repos): consult-before-drafting, crediting contract, amendment rule (dated markers on Accepted DRs, never silent rewrites), verify-before-commit, credit cap.
- Intent row (`ox decision enrich`) + consult-first route (drift-tested against the ox-consult skill description, which now names `ox decision`).
- Thin-relay skill `extensions/claude/skills/ox-decision/SKILL.md` (activation ergonomics only).
- Doctor `decision-paths` check: schema errors fail; configured paths matching zero DRs warn; no config → skip.

## Files (v1)

`internal/config/decision.go` (+ `Decision` field on ProjectConfig, validation hook) · `internal/decision/{types,input,sources,parse,search,detectors,retrievers,enrich,guidance}.go` · `cmd/ox/decision.go` · `internal/plan/decision_bundle.go` + `annotate.go` regex extension · `cmd/ox/agent_prime_xml.go` (`writeDecisionRecordGuidance`) · `internal/prime/{guidance,capability_table}.go` · `extensions/claude/skills/{ox-decision,ox-consult}/SKILL.md` · `cmd/ox/doctor_decision.go` · dep `bmatcuk/doublestar/v4` (MIT) · CHANGELOG · generated `docs/reference/decision/`.

> **[Ryan review — required]** data-location config: `decision.paths` shape + default dir list. (Settled in this session's review; recorded here for the PR.)

## Phase 2 (separate PRs, in rough priority order)

1. **codedb decision tagging** — results for files under decision paths carry a `decision` doc-type/facet so `ox code search` hits are recognizable as DRs (and filterable). The answer to "why doesn't codedb tag them" — it should; v1 didn't need to block on it.
2. **Cross-repo corpora** — a source referencing another repo by **full URL** (e.g. `https://github.com/sageox/sageox-design`), resolved to a daemon-cached local checkout; unlocks cross-repo ref resolution (ADR-021 here cites mono's ADR-047 — v1 correctly but noisily flags it "does not resolve in this repo's corpus").
3. **Decision capture / anti-entropy** — `gaps` (pull) + the backend's periodic "update ADRs from clearly-decided meeting content" skill (schedule). **Single-classifier doctrine (Ryan, 2026-07-18): "is this clearly a decision" is judged EXACTLY ONCE — in the conversation-layer distillation that produces the decisions layer (coming for transcribed audio). CLI gaps detector and backend periodic skill are both pure CONSUMERS of that signal; two judges that can disagree are forbidden.** Evidence-gated, boundary-timed (plan approval / session wrap), durable dismissals, never auto-commit.
4. https sources (fetch + cache rail — net-new machinery, honestly deferred).
5. `ox decision new` scaffold; `lint --strict` CI verb if diagnostics prove their worth in review flows.

## Phase 3 (server/mono)

Cloud `doc_type=decision` facet on `/api/v1/query` + ingest tagging; the conversations decisions-layer itself.

## Verification (ran)

- `ox decision enrich --topic …` on this repo: related ADR-021 surfaced, next-free 027, all nine duplicate numbers diagnosed.
- `--file docs/adr/ADR-021…`: real dangling ref found (ADR-047 — lives in mono, not here).
- Adversarial stdin draft: taken-number (names both ADR-018 holders), dangling `ADR-046 D9`, bad SOURCE ref, 3-credit overflow — all flagged.
- `ox plan enrich` on this plan doc: ADR-021 now appears as an `adr` context item (cross-wire live).
- Test suites: internal/decision (parse/search/input/sources + enrich/detectors/retrievers/guidance + no-network guarantee), cmd/ox (decision cmd, prime gating, doctor), internal/config, internal/plan cross-wire — via parallel test agents; `make lint && make test` before PR.
