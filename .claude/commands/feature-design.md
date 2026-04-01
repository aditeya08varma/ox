---
name: feature-design
description: >
  Use this skill when a user wants to think through, design, or spec out a
  feature, product decision, or technical capability. Triggers include: "I'm
  thinking about adding X", "help me design Y", "let's spec out Z", "what
  should I consider for this feature", or any request to reason through a
  change before building it. Even if the user only has a vague idea, start
  here — the skill is designed to surface what they haven't articulated yet.
---

# Feature Design

A skill for structured feature elicitation: ask the right questions to
surface goals, constraints, and tradeoffs, then produce a concise design
brief.

## Phase 1 — Orientation (1–2 questions max)

Start with a single open question to get the user talking:

> "What problem does this feature solve, and who runs into it?"

Let them answer freely. Extract from their response:
- **Actor**: who is affected (user, operator, agent, system)
- **Trigger**: what situation or pain prompts the need
- **Outcome**: what changes if this works well

If either actor or outcome is still unclear after their first answer, ask
one targeted follow-up. Don't ask more than two questions in Phase 1.

## Phase 2 — Structured Elicitation

Once you have basic orientation, work through the following dimensions.
**Don't ask all at once** — cluster 2–3 related questions per turn,
adapt based on what the user has already said, and skip anything already
answered.

### Goals & Success
- What does success look like? How would you know it's working?
- Is there a metric, behavior change, or user outcome you're optimizing for?
- Are there anti-goals — things this feature explicitly should *not* do?

### Users & Context
- Who uses this, and how often? (power users vs. occasional; human vs. agent)
- What's the context of use — what are they doing right before and after?
- Are there distinct user segments with different needs?

### Constraints & Scope
- What's the delivery pressure — prototype, MVP, or production-grade?
- Are there technical, regulatory, or resource constraints to design around?
- What's explicitly out of scope for now?

### Tradeoffs & Risks
- What's the biggest thing that could go wrong?
- Are there competing approaches you're already considering?
- What would you sacrifice to ship faster?

### Dependencies & Fit
- Does this touch existing systems, APIs, or data flows?
- Are there other features or teams this needs to coordinate with?
- Is there prior art — internal or external — worth learning from?

## Phase 3 — Design Brief

Once you have enough signal (usually after 2–3 exchanges), produce a
**Feature Design Brief** without waiting to be asked:
```
## Feature Design Brief: [Name]

**Problem**: [One sentence]
**Actor**: [Who is affected]
**Trigger**: [When/why this comes up]

**Goals**
- [Primary outcome]
- [Secondary outcomes if any]

**Anti-goals**
- [What this explicitly won't do]

**Key Constraints**
- [Technical / resource / time]

**Open Questions**
- [Unresolved decisions that need an answer before building]

**Suggested Next Step**
[One concrete recommendation: spike, prototype, stakeholder review, etc.]
```

Keep the brief short — if it's more than a page, something is wrong.
Flag open questions prominently; unresolved ambiguity is more useful than
false precision.

## Facilitation Notes

- **Bias toward fewer, better questions.** The goal is to help the user
  think, not interrogate them. If you sense the design space is clear,
  move to the brief early.
- **Reflect back** what you're hearing before asking the next question.
  This catches misunderstandings early.
- **Name the tension** when you spot a tradeoff the user hasn't
  articulated yet. ("It sounds like you want X and Y — those sometimes
  pull in opposite directions. Which matters more?")
- **Don't solve prematurely.** Phase 1 and 2 are for understanding, not
  proposing solutions. Save recommendations for the brief's
  "Suggested Next Step".