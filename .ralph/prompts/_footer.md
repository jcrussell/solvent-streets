<!-- prompts/_footer.md — appended to every rendered prompt. -->

## Iteration workflow

Run through these in order every iteration. Skipping a step is how
work goes wrong.

 1. **Recover context.** Read the prompt above, `bd ready`, and any
    relevant `bd memories` keyword search.
 2. **Pick scope.** Choose the smallest unit of work that fits one
    iteration. If the natural unit is bigger, file a smaller bead
    and work that instead.
 3. **Claim it.** `bd update <id> --claim` so other agents see it
    in_progress.
 4. **Read what's there.** Locate the existing functions, tests, and
    conventions you'll touch — never reimplement what already
    exists.
 5. **Plan inline.** Decide the smallest set of files to change and
    why. Reuse over invent.
 6. **Implement.** Edit code; keep changes tightly scoped to the
    bead.
 7. **Test as you go.** Land tests in the same change. No deferred
    tests. No `t.Skip` to get past failures.
 8. **Run the project gates.** `make build`, `make test`,
    `make lint`. Every gate green before commit. (`make build`
    re-runs the embedded WASM forecaster; `make test` runs
    `-race`; `make lint` uses the pinned `.golangci-version`.)
 9. **Commit.** One commit per bead. Message names the scope and
    the bead id.
10. **Close.** `bd close <id>` after the commit is in.
11. **Note non-obvious discoveries.** If you learned something a
    future iteration would want to know, `bd remember "..."` with a
    specific key. Don't log generic observations.
12. **Stop.** When the iteration's scope is done, end the session —
    don't pick up a second bead.

## Safety rails

These never bend:

- **No bypassing the gate.** Never use `--no-verify`, never
  `--no-gpg-sign`, never skip pre-commit / pre-push hooks. If a hook
  fails, fix the cause, not the symptom.
- **No force-push.** No `--force`, no `--force-with-lease`, no
  `reset --hard origin/main`. If history needs rewriting, ask first.
- **No deleting work.** No `git clean -fdx` outside the revert flow,
  no `git stash drop` without inspecting, no removing files you
  didn't author.
- **No silent edits to shared state.** Don't edit `.git/`,
  `.beads/db/`, or `.ralph/state/` by hand. Use the surfacing CLI
  (`bd`, `git`, ralph hooks) and let the tools record the change.
- **No fabricating work.** If `bd ready` is empty, exit with the
  tree clean — don't invent tasks to keep the loop busy. The FSM is
  designed to finish.
- **No remote sync.** Never `git push`, never `bd dolt push`,
  never `bd dolt pull`. Remote sync is human-controlled per
  `CLAUDE.md`. Leave work committed locally and stop.

## Communicate

- Write a one-line narrative of this iteration to
  `.ralph/state/session.md` (append) before exiting — what did the
  iteration accomplish, in plain language? Future iterations and
  human reviewers read this.
- If you blocked on a missing dependency, decision, or credential,
  `bd human <id> "describe blocker"` so the user notices on next
  open.
