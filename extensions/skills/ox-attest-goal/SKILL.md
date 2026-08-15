---
name: ox-attest-goal
description: >-
  Create or revise an Attest-backed customer capability as executable BDD.
  Use when a user asks to add, improve, or review BDDs/customer scenarios for
  an Attest corpus; especially when they mention customer flow, acceptance
  criteria, Gherkin, Rules, or a capability that needs proof. Start from the
  customer's desired journey, not the current implementation, then use
  `ox attest status --json` to place the capability in the proof ladder.
---

<!-- This opt-in skill is deliberately about product judgment, not an
     implementation-shaped test recipe. The Attest CLI remains the portable
     source of truth for the capability corpus and proof state. -->

## Customer journey before mechanics

1. State the customer's goal, starting point, natural action, feedback, and
   continuation or recovery.
2. Preserve an established journey. Do not silently redesign it to make a
   scenario easier to automate. If the journey is unclear, name the smallest
   proposed flow and the product decision it needs.
3. Write the `Rule:` as a customer-recognizable promise. Write scenarios in
   observable language: what customers can do, see, retain, and recover from.
   Do not put routes, status codes, database rows, selectors, or validation
   regexes in Gherkin.
4. Cover the happy path plus consequential failure, permission, and continuity
   cases. Derive component/API/concurrency tests separately for the mechanics
   that keep the promise true.

## Ground the work in Attest

1. Run `ox attest status --json` to understand the existing capability ladder.
2. Find the relevant feature and keep one coherent customer promise per
   `Rule:` block.
3. Compile and run the project's existing acceptance workflow. Do not claim a
   capability is proven until its run evidence supports it.
4. Run `ox attest check --json` before handoff to identify proof invalidated by
   the working diff.
