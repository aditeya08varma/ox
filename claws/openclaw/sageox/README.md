# sageox

Complete interactive toolkit for [SageOx](https://sageox.ai) team knowledge.

## Capabilities

| Capability | What it does |
|---|---|
| **Query** | Search team discussions, docs, sessions, and code |
| **Coworkers** | Discover, load, create, and remove expert AI agents |
| **Distill** | Interactive single-repo observation distillation |
| **Distill Pipeline** | Multi-repo automated sync + index + distill |
| **Summary** | Cross-team 24h summary generation |
| **Glance** | Real-time AI coworker activity and collision detection |
| **Catchup** | Structured briefing after being away |
| **Import/Export** | Import docs/recordings, export knowledge as local files |

## Prerequisites

- `ox` — installed via bundled curl script (not Homebrew)
- `git`, `gh`, `jq`, `claude` — declared in skill metadata

## Install

```bash
clawhub install sageox
```

## Testing locally

```bash
# Lint before deploying
python3 .claude/skills/clawhub-skill-lint/scripts/lint.py claws/openclaw/sageox

# Deploy to VPS for testing
bash claws/openclaw/sageox/deploy-to-vps.sh
```
