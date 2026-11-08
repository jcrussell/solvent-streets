# AGENTS.md

Guidance for coding agents working in this repository. Humans and agents
share one workflow, described in [CONTRIBUTING.md](CONTRIBUTING.md): build,
test, lint, commit-message style, and the rules for changes that affect stored
results all live there. Read [docs/architecture.md](docs/architecture.md)
before changing anything non-trivial. This file adds only what is specific to
agents.

## Beads (issue tracking & memory)

This project uses **bd (beads)** for issue tracking and persistent memory.
Run `bd prime` for the full command reference.

- Use `bd` for task tracking — not TodoWrite, TaskCreate, or markdown TODO lists.
- Use `bd remember "..."` for cross-session knowledge — not MEMORY.md files.

## Remote sync — agents do NOT push or pull

Do **not** run `git push`, `git pull`, `bd dolt push`, or `bd dolt pull`
unless the user explicitly asks. The user controls when remote sync happens.
This includes session-close behavior — finish work, leave it committed
locally, and stop.

## Gotchas

The traps that catch agents are all stated in their owning docs: always build
with `make build` and never add CGO
([CONTRIBUTING.md](CONTRIBUTING.md#build-test-lint)), and copy the existing
`simplefeatures` call sites rather than guessing its signatures
([architecture](docs/architecture.md#geometry)).
