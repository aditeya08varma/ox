# Business Action: Inspect Knowledge Bubbles

**Actor:** A developer (e.g., Devon)
**Goal:** See and locate the curated team knowledge available to them
**Preconditions:**
- Signed in and a member of a team

## Stub

The actor runs `ox kb list` to see every Knowledge Bubble they can access —
personal, profile, and team-scoped — in one list, filterable by type, and can
inspect a bubble to see what it holds. Bubbles are Curator-maintained
syntheses of the team's distilled conversations (ox ADR-028). Team Contexts
and Ledgers are permanent conversation stores, not bubbles: they never appear
in this list and are read with `ox teams` and `ox status` instead.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: knowledge-bubbles/list-bubbles.feature
