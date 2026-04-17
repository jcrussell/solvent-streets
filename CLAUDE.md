# Project Instructions for AI Agents

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

## Build & Test

_Add your build and test commands here_

## Architecture Overview

_Add a brief overview of your project architecture_

## Conventions & Patterns

_Add your project-specific conventions here_
