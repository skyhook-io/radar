#!/usr/bin/env bash
# Fast dev-loop checks: only what the change touches.
#
#   scripts/test-changed.sh [base]     (default base: origin/main)
#
# Changed files = committed since the merge base with <base>, plus staged,
# unstaged and untracked files. For each side that changed it runs:
#   Go:       go vet + go test on the changed packages only (root and pkg/ modules)
#   Frontend: tsc for web/ and packages/k8s-ui, then `vitest related` on the changed files
#
# This is the iteration tier. The completion check is CI on the pushed head
# (or, for a branch CI doesn't run, `make test` plus both full vitest suites once).
# GO_TEST_RUN=<regexp> passes -run to go test, e.g. to keep internal/server short.
# Works with the bash 3.2 macOS ships.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

base=$(git merge-base "${1:-origin/main}" HEAD)
changed=$({ git diff --name-only "$base"; git diff --name-only HEAD; git ls-files --others --exclude-standard; } | sort -u)
[ -z "$changed" ] && { echo "no changes"; exit 0; }

pkg_pkgs=""; root_pkgs=""; k8sui=""; web=""
while IFS= read -r f; do
  [ -e "$f" ] || continue
  case "$f" in
    pkg/*.go) pkg_pkgs="$pkg_pkgs ./$(dirname "${f#pkg/}")" ;;
    *.go) root_pkgs="$root_pkgs ./$(dirname "$f")" ;;
    packages/k8s-ui/*.ts|packages/k8s-ui/*.tsx) k8sui="$k8sui ${f#packages/k8s-ui/}" ;;
    web/*.ts|web/*.tsx) web="$web ${f#web/}" ;;
  esac
done <<< "$changed"
dedup() { tr ' ' '\n' <<< "$1" | sed '/^$/d' | sort -u | tr '\n' ' '; }
pkg_pkgs=$(dedup "$pkg_pkgs"); root_pkgs=$(dedup "$root_pkgs")
run_flag=""; [ -n "${GO_TEST_RUN:-}" ] && run_flag="-run $GO_TEST_RUN"

# Node 25 exposes a localStorage global that breaks jsdom's.
if [ "$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)" -ge 25 ]; then
  export NODE_OPTIONS="${NODE_OPTIONS:-} --no-experimental-webstorage"
fi

# Word splitting of the package and file lists below is intended.
# shellcheck disable=SC2086
{
  if [ -n "$pkg_pkgs" ]; then
    echo "== go (pkg module): $pkg_pkgs"
    (cd pkg && go vet $pkg_pkgs && go test -count=1 $run_flag $pkg_pkgs)
  fi
  if [ -n "$root_pkgs" ]; then
    echo "== go: $root_pkgs"
    go vet $root_pkgs && go test -count=1 $run_flag $root_pkgs
  fi
  if [ -n "$k8sui$web" ]; then
    echo "== tsc"
    (cd web && npx tsc --noEmit -p .) && (cd packages/k8s-ui && npx tsc --noEmit -p .)
    if [ -n "$k8sui" ]; then echo "== vitest related (k8s-ui)"; (cd packages/k8s-ui && npx vitest related --run $k8sui); fi
    if [ -n "$web" ]; then echo "== vitest related (web)"; (cd web && npx vitest related --run $web); fi
  fi
}
echo "== done"
