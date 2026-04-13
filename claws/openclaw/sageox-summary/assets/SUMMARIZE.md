# Team Summary

You are generating a team summary from a curated set of distilled session logs. The calling skill has already selected exactly which files are new since the previous summary run — you only need to synthesize them. The summary will be read by people across all functions — engineering, product, marketing, and leadership. Your job is to help the team stay aligned and quickly identify what conversations need to happen and what work may need adjusting.

## Workflow

1. Read ONLY the files listed below (grouped by team). Do not open any other file in these directories — the calling skill has already filtered out anything that was covered by a prior summary, and reading un-listed files will double-count content.
2. Each team's section names its absolute context directory once in the heading; the file paths underneath are relative to that directory. Join them when reading (e.g. `<team_dir>/memory/daily/<file>`).
3. Produce a structured summary following the format described below.

## Files to read

{{FILE_LIST}}

## Summary structure

You MUST use exactly these four section headings. Each section should have 2–5 bullets. Prioritize signal over detail — a product manager and a marketer will read this alongside engineers. Synthesize across PRs and sessions; do not enumerate individual PRs unless they represent a meaningful milestone.

*Where we are*
Progress relative to what we set out to do. Cover both macro (goals, initiatives — are we on track?) and micro (what shipped recently). Name people and repos, but summarize themes rather than listing every PR.

*What's getting in the way*
Blockers, friction, unresolved issues, failed attempts, or things that took longer than expected. Flag patterns. This section drives the conversations the team needs to have.

*What we've learned*
This is one of the most valuable sections — don't skip or thin it out. Include: techniques or approaches someone used that others should adopt, non-obvious discoveries about the codebase or infrastructure, important team discussions and the conclusions reached, and shifts in thinking about how we build or ship. If someone solved a hard problem, explain the insight — not just that they fixed it.

*What's next*
Work queued up, drafts in progress, or follow-ups called out. Anything that signals upcoming changes others should prepare for.

## Formatting rules

- Format for Slack mrkdwn: use *bold*, _italic_, `code`, bullet points with •
- Keep it scannable — each bullet should be 1–2 sentences max
- Link to notable sessions where they add context
- Surface important discussions, debates, and decisions the team had
{{MULTI_TEAM_RULES}}
