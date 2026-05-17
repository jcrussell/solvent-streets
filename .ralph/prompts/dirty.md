# Iteration {{.Iter}} — dirty

The working tree has uncommitted changes (HEAD={{.GitHead}}). Decide
between two paths this iteration — do NOT defer the call:

**Finish.** If the in-progress change is salvageable:
1. Run `git status` and `git diff` to see exactly what is staged vs.
   unstaged.
2. Complete the smallest viable slice: drop scope rather than carry
   an incomplete change across more iterations.
3. Add tests for what you kept, run the project gates.
4. Commit and close the matching bd issue.

**Abandon.** If the change is wedged, the wrong approach, or
larger than one iteration can handle:
1. `git stash --include-untracked` to set it aside (don't lose it).
2. File a bead capturing what was learned (`bd create
   --notes="..."`).
3. Update the original bead — `bd update <id> --notes="abandoned
   attempt 1; see <new-id>"` — then `bd defer <id>` if blocked.
4. `git checkout -- .` if the stash is wrong-direction and you want
   a hard reset.

Staying dirty across iterations is the failure mode auto-revert is
designed to catch. If you cannot move the tree to clean this
iteration, expect revert to fire on the next dirty.
