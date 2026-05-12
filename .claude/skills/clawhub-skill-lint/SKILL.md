---
name: clawhub-skill-lint
description: >
  Use this skill before publishing any ClawHub skill from this repo, or after
  editing a SKILL.md, to verify the skill won't be flagged or rejected by
  ClawHub's server-side moderation pipeline. The skill re-implements every
  static-scanner rule from openclaw/clawhub's `convex/lib/moderationEngine.ts`
  plus the frontmatter spec from `docs/skill-format.md` and runs them locally.
  Triggers: "lint the claws skills", "check the claws/openclaw skills",
  "scan before publish", "is the skill clean", "any scanner findings",
  /clawhub-skill-lint, before any `clawhub sync` or `clawhub skill publish`.
---

# clawhub-skill-lint

A pre-publish validator for ClawHub skills. Catches publish-time failures
locally — no network round-trip, no `clawhub` CLI required.

## When to use

- **Before** running `clawhub sync` or `clawhub skill publish`.
- **After** editing any `SKILL.md`, `README.md`, or other text file in a
  ClawHub skill folder, to confirm no new patterns trigger the scanner.
- **In CI** as a pre-publish gate.
- Whenever the user asks "is this skill clean", "will this pass review",
  or "scan before I publish".

## How to invoke

The skill bundles a Python linter at
`.claude/skills/clawhub-skill-lint/scripts/lint.py`. Call it with one or
more paths:

```bash
# Lint a single skill folder
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py claws/openclaw/sageox

# Lint every skill under a parent directory
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py claws/openclaw

# Multiple paths in one invocation
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py claws/openclaw/sageox claws/openclaw/<other-skill>

# Machine-readable JSON output (for CI)
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py --json claws/openclaw

# Treat warnings as errors
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py --strict claws/openclaw
```

The linter discovers skill folders by looking for `SKILL.md` (or `skill.md`).
A path that contains `SKILL.md` directly is treated as a single skill;
any other directory is scanned for child folders.

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | Clean (or only `info` findings; or `--strict` not set and only `warn` findings) |
| `1` | At least one `critical` finding, or `--strict` and at least one `warn` |
| `2` | Usage / I/O error (bad path, missing `SKILL.md`, etc.) |

## How to interpret results

Each finding has:

- **rule_id** — short stable identifier (e.g. `static.malicious_install_prompt`,
  `frontmatter.missing_required_field`)
- **severity** — `critical`, `warn`, or `info`
- **file** — path inside the skill folder (or `SKILL.md` / `metadata` for
  frontmatter findings)
- **line** — 1-indexed line number where the rule first matched
- **message** — human-readable explanation
- **evidence** — truncated snippet of the matching text

**Critical findings will hard-block publish.** Examples: malicious install
prompt, frontmatter missing required field, slug regex fail, install kind
not in {`brew`, `node`, `go`, `uv`}, bundle size > 50 MB.

**Warnings are advisory** but worth fixing — they signal patterns the
scanner may flag as `suspicious` (which doesn't block but adds a warning
banner on the skill page). Examples: `always: true` in frontmatter,
URL shortener references, prompt-injection bait phrases.

**Info findings** are observations that don't affect publishability.

## Workflow when findings are reported

1. **Read each finding** out loud to the user, grouped by severity.
2. **For each critical finding:**
   - Show the file:line and the offending evidence.
   - Propose a concrete fix (e.g., "rephrase 'for macOS:' to '**macOS:**'
     to avoid triggering the terminal-instruction precondition" or
     "add `version: 0.1.0` to the frontmatter").
   - Apply the fix only after the user confirms.
3. **Re-run the linter** after fixes to confirm clean.
4. **Only proceed to publish** when the linter exits 0.

## Source-of-truth references

The linter is kept in sync with these upstream files. If ClawHub updates
its rules, update `scripts/lint.py` to match:

- [openclaw/clawhub `docs/skill-format.md`](https://github.com/openclaw/clawhub/blob/main/docs/skill-format.md) — frontmatter schema, slug regex, bundle limits, install kinds
- [openclaw/clawhub `convex/lib/moderationEngine.ts`](https://github.com/openclaw/clawhub/blob/main/convex/lib/moderationEngine.ts) — static scanner rules (the canonical source of truth for `hasMaliciousInstallPrompt` and friends)
- [openclaw/clawhub `docs/security.md`](https://github.com/openclaw/clawhub/blob/main/docs/security.md) — moderation pipeline overview

## What this linter does NOT cover

- **VirusTotal hash lookup** — server-side, runs against a live SHA-256 DB.
  Not reproducible locally.
- **VT Code Insight (Gemini LLM scan)** — opaque, server-side. The only
  way to get the LLM verdict is to publish under a throwaway slug and
  query `/api/v1/skills/<slug>/scan`. See `claws/openclaw/PUBLISHING.md`
  § "Throwaway-slug pre-flight" for the workflow.
- **Behavioral correctness of the skill** — the linter only looks at
  patterns. Whether the skill *actually does what it says* is a separate
  test (load it into a real OpenClaw via `skills.load.extraDirs`).

A clean lint result is necessary but not sufficient for a successful
publish. Always also do at least one throwaway-slug publish before
claiming the canonical slug.
