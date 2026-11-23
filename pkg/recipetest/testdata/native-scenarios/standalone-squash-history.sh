#!/usr/bin/env bash
set -euo pipefail
mode=$1
SOURCE=$(git config --get remote.origin.url)
SOURCE=${SOURCE#file://}
case "$mode" in
prepare)
  # SOURCE is the case-local seeded repository, never the managed c2j checkout.
  for i in {1..10}; do
    git -C "$SOURCE" commit --quiet --allow-empty -m "Published ancestor $i"
  done
  published=$(git -C "$SOURCE" rev-parse HEAD)
  tree=$(git -C "$SOURCE" rev-parse HEAD^{tree})
  unrelated=$(git -C "$SOURCE" commit-tree "$tree" -m 'Unrelated published root')
  git -C "$SOURCE" update-ref refs/heads/unrelated "$unrelated"
  git fetch --quiet --depth=1 "$SOURCE" "$published"
  git reset --quiet --hard "$published"
  printf '\000\377\200\000\012' > binary
  printf '#!/bin/sh\nexit 0\n' > executable
  chmod 755 executable
  ln -s binary 'symbolic link'
  git rm --quiet obsolete.txt
  git add .
  git commit --quiet -m 'Unpublished correction'
  candidate=$(git rev-parse HEAD)
  if git -C "$SOURCE" cat-file -e "$candidate^{commit}" 2>/dev/null; then
    printf 'Fixture published the candidate unexpectedly\n' >&2; exit 1
  fi
  git rev-parse HEAD > "$OUTBOX/before.head"
  git rev-parse HEAD^{tree} > "$OUTBOX/before.tree"
  git diff --binary --full-index "$BASE" HEAD > "$OUTBOX/before.diff"
  git ls-files --stage -z > "$OUTBOX/before.index"
  git status --porcelain=v1 -z > "$OUTBOX/before.status"
  node - "$BASE" "$published" "$candidate" "$unrelated" "$PWD" <<'JS'
const [base, published, candidate, unrelated, dir] = process.argv.slice(2);
process.stdout.write(JSON.stringify({base, published, candidate, unrelated, dir}));
JS
  ;;
restored)
  test "$(git rev-parse HEAD)" = "$CANDIDATE"
  test "$(git rev-parse --is-shallow-repository)" = false
  git merge-base --is-ancestor "$BASE" HEAD
  printf '%s' "$PWD"
  ;;
check|rejected)
  test "$(git diff --binary --full-index "$BASE" HEAD | sha256sum)" = "$(sha256sum < "$INBOX/before.diff")"
  if [ "${REBASED:-false}" = false ]; then
    cmp <(git ls-files --stage -z) "$INBOX/before.index"
  fi
  cmp <(git status --porcelain=v1 -z) "$INBOX/before.status"
  test -x executable
  test "$(readlink 'symbolic link')" = binary
  test ! -e obsolete.txt
  if [ "$mode" = rejected ]; then
    test "$(git rev-parse HEAD)" = "$CANDIDATE"
    cmp <(git rev-parse HEAD^{tree}) "$INBOX/before.tree"
  else
    test "$(git rev-parse HEAD^)" = "$BASE"
    if [ "${REBASED:-false}" = false ]; then
      cmp <(git rev-parse HEAD^{tree}) "$INBOX/before.tree"
    else
      test "$(cat upstream.txt)" = 'New upstream content'
    fi
  fi
  printf '%s' "$PWD"
  ;;
advance)
  printf 'New upstream content\n' > "$SOURCE/upstream.txt"
  git -C "$SOURCE" add .
  git -C "$SOURCE" commit --quiet -m 'Upstream after saved candidate'
  git -C "$SOURCE" rev-parse HEAD | tr -d '\n'
  ;;
*) printf 'Unknown fixture mode: %s\n' "$mode" >&2; exit 2 ;;
esac
