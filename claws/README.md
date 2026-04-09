# Claws

Skills, plugins, and extensions that `ox` publishes to third-party "Claw"
runtimes. Each subdirectory targets one runtime and contains the artifacts
that runtime consumes.

| Claw | Directory | What it contains |
|---|---|---|
| [OpenClaw](https://openclaw.ai) | [`openclaw/`](openclaw/) | Skills published to [ClawHub](https://clawhub.ai) |

## Adding a new claw

1. Create a `claws/<claw-name>/` subdirectory.
2. Add a `README.md` explaining the runtime and linking to its docs.
3. Add a `PUBLISHING.md` documenting how artifacts in this directory get
   released to the claw's registry (commands, auth, state maintained in
   this repo).
4. Add one subdirectory per artifact (skill, plugin, extension).
5. Update the table above.

Each claw directory is self-contained — its layout, naming, and publish
workflow follow the conventions of its target runtime, not `ox`'s own
release pipeline.
