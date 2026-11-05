#!/usr/bin/env sh
# check-commit-msg.sh <message-file>
#
# Rejects a commit whose subject is not a Conventional Commit. The goreleaser
# changelog (.goreleaser.yaml) groups and filters release notes by these
# prefixes, so a subject like "ingest: fix paging" silently lands in the
# catch-all "Other" group. Wired as .beads/hooks/commit-msg; covered by
# scripts/check-commit-msg_test.sh (make test-hooks).
#
# Accepts:  <type>[(<scope>)][!]: <summary>
#   type  = feat fix perf refactor docs test chore ci build style revert
#   scope = anything but ")" — "export/cmd" and "db,ingest" are both in use
# Also passes git's own subjects: Merge ..., Revert "...", and the
# fixup!/squash!/amend! subjects that `git rebase --autosquash` consumes.
set -eu

msg_file=${1:?usage: check-commit-msg.sh <message-file>}

# The subject is the first line that is neither blank nor a git comment.
subject=$(grep -v -e '^#' -e '^[[:space:]]*$' "$msg_file" | head -n 1 || true)

if [ -z "$subject" ]; then
  exit 0 # an empty message: git aborts the commit on its own
fi

types='feat|fix|perf|refactor|docs|test|chore|ci|build|style|revert'
if printf '%s\n' "$subject" | grep -Eq "^(${types})(\([^)]+\))?!?: [^[:space:]]"; then
  exit 0
fi
if printf '%s\n' "$subject" | grep -Eq '^(Merge |Revert "|(fixup|squash|amend)! )'; then
  exit 0
fi

cat >&2 <<MSG
commit-msg: subject is not a Conventional Commit:

    $subject

Use <type>[(<scope>)][!]: <summary>, where type is one of
    ${types}
e.g.  fix(ingest): page ArcGIS by exceededTransferLimit
See CONTRIBUTING.md ("Commit messages"). Bypass once with --no-verify.
MSG
exit 1
