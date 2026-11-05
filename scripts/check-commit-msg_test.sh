#!/usr/bin/env sh
# Table test for check-commit-msg.sh. Run via `make test-hooks`.
set -eu

here=$(dirname "$0")
check="$here/check-commit-msg.sh"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
fail=0

expect() {
  want=$1
  shift
  printf '%b' "$1" >"$tmp"
  if sh "$check" "$tmp" 2>/dev/null; then got=pass; else got=fail; fi
  if [ "$got" != "$want" ]; then
    printf 'FAIL: want %s, got %s for %s\n' "$want" "$got" "$(head -n 1 "$tmp")"
    fail=1
  fi
}

expect pass 'feat: add piecewise PCI decay\n'
expect pass 'fix(ingest): handle 504 from Overpass\n\nbody\n'
expect pass 'fix(export/cmd): scope with a slash\n'
expect pass 'fix(cmdutil,db,ingest): scope with commas\n'
expect pass 'feat(api)!: breaking change\n'
expect pass 'build(deps): bump modernc.org/sqlite\n'
expect pass 'chore(deps): bump the gomod group\n'
expect pass 'ci(deps): bump actions/checkout\n'
expect pass '# Please enter the commit message\n\ndocs: subject after comments\n'
expect pass 'Merge branch '"'"'main'"'"' into feature\n'
expect pass 'Revert "feat: add thing"\n'
expect pass 'Reapply "feat: add thing"\n'
expect pass 'fixup! fix(ingest): handle 504\n'
expect pass ''

expect fail 'ingest: page ArcGIS by exceededTransferLimit\n'
expect fail 'test,docs: comma-joined types\n'
expect fail 'Fix: capitalised type\n'
expect fail 'fix:no space after colon\n'
expect fail 'fix(): empty scope\n'
expect fail 'fix(ingest) missing colon\n'
expect fail 'wip\n'

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "check-commit-msg: all cases pass"
