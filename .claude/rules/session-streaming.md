# Real-Time Session Streaming — Consult the Spec First

Any work touching real-time streaming of coding sessions — `internal/moq/`,
`internal/daemon/sessionstream/`, the stream envelope, wire/entry seq semantics,
`revision`/amendments, pause fencing, abort handling, or the coordinates/cloud contract —
MUST consult **`docs/specs/session-streaming.md`** before designing or changing anything.
That spec is the design canon; it was adversarially reviewed and is mapped onto the
sageox-mono conversation model (ADR-057/076/081/087).

Non-negotiables it encodes (do not re-derive or contradict them casually):

- **Tail `raw.jsonl`, never tee `RawWriter`.** Streaming must be unable to block or corrupt
  capture. Git/LFS upload stays the durability + fidelity authority.
- **Conversation-native envelope.** `conversation_id` = `cnv_` twin of `ses_` (pure prefix
  swap, bijective — never bind a foreign `cnv_`); legacy `turns.jsonl` shapes are
  server-side projections, never the wire format.
- **Dense wire `seq` fold.** Wire-seq assignment is a deterministic pure function of
  `raw.jsonl` content; liveness pings are seq-less; dedup key is
  `(conversation_id, seq, revision)`; `revision` = file rewrite generation.
- **Pause is fail-closed with a frame-atomic fence** (range membership on
  `suspended_from_entry_seq`, decided at send-commit; frames are never torn).
- **Honest acks.** moq-lite has no object ack; cursor advance = local send success, not
  durability. No receipt plane exists yet (ADR-087 confirms) — never design as if one does.
- **Abort persists a durable intent marker** until the tombstone is confirmed (relay close
  or HTTPS fallback); aborted content is never projected.
- **Flag-gated, default off** (`FEATURE_SESSION_STREAMING`); the cloud coordinates call is
  the authoritative enable. Streaming failures are log-only — never surfaced into session
  flow.

If a change genuinely requires breaking one of these, update the spec in the same PR and
say so explicitly in the PR body — the spec and the code must never disagree silently.
