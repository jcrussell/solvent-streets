<!--
prompts/_header.md — prepended to every rendered prompt. Optional.

The default ships as comments only so the rendered prompt is just the
state body. Edit this file to inject ambient context every iteration:
session goals, branch conventions, repo-wide constraints.

Available template variables (Go text/template syntax):
  .Iter         int     iteration counter (1-based)
  .State        string  current FSM state (clean/dirty/revert/review)
  .PrevState    string  state from the previous iteration
  .GitDirty     bool    working tree has uncommitted changes
  .GitHead      string  current HEAD sha
  .RepoRoot     string  absolute path to repo root
  .LastIter     any     iteration record from the previous iter
  .GateResult   string  pass / fail / "" — last gate hook outcome
  .Review.Branch       string  review-mode branch
  .Review.Base         string  review-mode base
  .Review.OpenFindings int     count of open bd findings on review:<branch>

Includes are supported via the include template function: a directive
like {{`{{include "snippet.md"}}`}} (without the surrounding backticks)
pastes another file from the same prompts/ directory. Climbs above
the prompts/ directory are rejected.
-->

You are working on `pvmt` — a pure-Go (no CGO) CLI for pavement data ingestion, hex-grid coverage, and PCI decay forecasting. Repo root: `{{.RepoRoot}}`.

The authoritative project guide is `CLAUDE.md` at the repo root. It documents the factory DI pattern, multi-city model, simplefeatures gotchas, and `make` targets — read it if you need architecture or convention context. Persistent cross-session knowledge lives in `bd memories`.

Current iteration: {{.Iter}}. State: `{{.State}}` (previous: `{{.PrevState}}`). Git HEAD: `{{.GitHead}}` (dirty: {{.GitDirty}}). Last gate: `{{.GateResult}}`.
