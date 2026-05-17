# Iteration {{.Iter}} — revert (one-shot)

Auto-revert just fired: ralph hit the dirty streak threshold and
reset the working tree to HEAD ({{.GitHead}}). This prompt runs once.
Use it to record what happened so the next clean iteration doesn't
repeat the same mistake:

1. `bd remember "..."` — capture the failure mode (wrong approach,
   missing context, hidden coupling) so future agents can search for
   it. Be specific; vague memories don't help.
2. If a particular bead drove the streak, `bd update <id>
   --notes="auto-reverted after {{.Iter}}; see remember <key>"` and
   consider `bd defer <id>` until the blocker is understood.
3. Do NOT immediately re-attempt the same bead. Pick a smaller piece
   of work, or file a new bead that breaks the original into parts
   you can finish in one iteration each.

The next iteration will run `clean.md`. The tree is already reset —
don't try to recover the abandoned changes from a stash, that's the
fastest path back into the same hole.
