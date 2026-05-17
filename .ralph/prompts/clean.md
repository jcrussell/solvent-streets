# Iteration {{.Iter}} — clean

Working tree is clean (HEAD={{.GitHead}}). Drain one ready bead this
iteration:

1. `bd ready` — pick the highest-priority unblocked issue you can
   complete in one iteration. Prefer issues whose acceptance criteria
   are testable.
2. `bd show <id>` — read the description, acceptance, design notes,
   and the closure log of its blockers.
3. `bd update <id> --claim` — claim it before writing code so other
   agents don't double-pick.
4. Implement the smallest change that satisfies the acceptance
   criteria. Land tests in the same change.
5. Run the project quality gates (`go build ./...`, `go test ./...`,
   `go vet ./...` — adapt to the project).
6. Commit with a message that names the bead (`<scope>: <summary>
   (<id>)`).
7. `bd close <id>` once the commit is in.

If nothing in `bd ready` looks small enough to finish, prefer filing
a smaller bead and closing the larger one with `--reason="too large;
split into <new-id>"` over leaving the loop in a half-finished state.

When you decide there is no work you can do this iteration, exit
without dirtying the tree — the FSM will see `bd ready` empty + tree
clean and finish with done{queue_empty}.
