#!/usr/bin/env bash
# Fast dev-loop checks: only what the change touches.
#
#   scripts/test-changed.sh [base]     (default base: origin/main)
#
# Changed files = committed since the merge base with <base>, plus staged,
# unstaged and untracked files. For each side that changed it runs:
#   Go:       go vet + go test on the changed packages only (root and pkg/ modules).
#             A deleted file still selects its package; a testdata file selects the
#             package that owns the testdata directory.
#   Frontend: each project's `npm run tsc`, then `vitest related` on the changed files
#             (including shared testdata fixtures the tests import).
#   Config:   go.mod/go.sum → build + vet the whole module; a frontend config
#             (package.json, tsconfig, vite/vitest config) → both type-checks, and a
#             notice that the full vitest suite is needed once.
#
# This is the iteration tier. The completion check is CI on the pushed head
# (or, for a branch CI doesn't run, `make test` plus both full vitest suites once).
# Go test results come from Go's test cache when nothing they depend on changed;
# GO_TEST_FRESH=1 runs them anyway (-count=1), e.g. to recheck a flaky test.
# GO_TEST_RUN=<regexp> passes -run to go test, e.g. to keep internal/server short.
# Works with the bash 3.2 macOS ships.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

base=$(git merge-base "${1:-origin/main}" HEAD)
changed=$({ git diff --name-only "$base"; git diff --name-only HEAD; git ls-files --others --exclude-standard; } | sort -u)
[ -z "$changed" ] && { echo "no changes"; exit 0; }

pkg_pkgs=""; root_pkgs=""; k8sui=""; web=""
root_module=0; pkg_module=0; fe_config=0
add_go() { # $1 = repo-relative package directory
  [ -d "$1" ] && ls "$1"/*.go >/dev/null 2>&1 || return 0
  case "$1" in
    pkg/*) pkg_pkgs="$pkg_pkgs ./${1#pkg/}" ;;
    pkg) pkg_pkgs="$pkg_pkgs ." ;;
    *) root_pkgs="$root_pkgs ./$1" ;;
  esac
}
while IFS= read -r f; do
  case "$f" in
    go.mod|go.sum) root_module=1 ;;
    pkg/go.mod|pkg/go.sum) pkg_module=1 ;;
    package.json|package-lock.json|web/package.json|packages/k8s-ui/package.json|web/tsconfig*.json|packages/k8s-ui/tsconfig*.json|web/vite*.config.*|web/vitest*.config.*|packages/k8s-ui/vite*.config.*|packages/k8s-ui/vitest*.config.*|scripts/vitest-*.ts) fe_config=1 ;;
    */testdata/*)
      add_go "${f%%/testdata/*}"
      [ -e "$f" ] && k8sui="$k8sui ../../$f" && web="$web ../$f" ;;
    *.go) add_go "$(dirname "$f")" ;;
    packages/k8s-ui/*.ts|packages/k8s-ui/*.tsx|packages/k8s-ui/*.json) [ -e "$f" ] && k8sui="$k8sui ${f#packages/k8s-ui/}" ;;
    web/*.ts|web/*.tsx|web/*.json) [ -e "$f" ] && web="$web ${f#web/}" ;;
  esac
done <<< "$changed"
dedup() { tr ' ' '\n' <<< "$1" | sed '/^$/d' | sort -u | tr '\n' ' '; }
pkg_pkgs=$(dedup "$pkg_pkgs"); root_pkgs=$(dedup "$root_pkgs")
run_flag=(); [ -n "${GO_TEST_RUN:-}" ] && run_flag=(-run "$GO_TEST_RUN")
[ -n "${GO_TEST_FRESH:-}" ] && run_flag+=(-count=1)

# Node 25 exposes a localStorage global that breaks jsdom's.
if [ "$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)" -ge 25 ]; then
  export NODE_OPTIONS="${NODE_OPTIONS:-} --no-experimental-webstorage"
fi

# Word splitting of the package and file lists below is intended.
# shellcheck disable=SC2086
{
  if [ "$pkg_module" = 1 ]; then echo "== go (pkg module): go.mod changed, build + vet ./..."; (cd pkg && go build ./... && go vet ./...); fi
  if [ "$root_module" = 1 ]; then echo "== go: go.mod changed, build + vet ./..."; go build ./... && go vet ./...; fi
  if [ -n "$pkg_pkgs" ]; then
    echo "== go (pkg module): $pkg_pkgs"
    (cd pkg && go vet $pkg_pkgs && go test ${run_flag[@]+"${run_flag[@]}"} $pkg_pkgs)
  fi
  if [ -n "$root_pkgs" ]; then
    echo "== go: $root_pkgs"
    go vet $root_pkgs && go test ${run_flag[@]+"${run_flag[@]}"} $root_pkgs
  fi
  if [ -n "$k8sui$web" ] || [ "$fe_config" = 1 ]; then
    echo "== tsc"
    (cd web && npm run --silent tsc) && (cd packages/k8s-ui && npm run --silent tsc)
    if [ -n "$k8sui" ]; then echo "== vitest related (k8s-ui)"; (cd packages/k8s-ui && npx vitest related --run $k8sui); fi
    if [ -n "$web" ]; then echo "== vitest related (web)"; (cd web && npx vitest related --run $web); fi
    if [ "$fe_config" = 1 ]; then echo "!! frontend config changed: run the full vitest suites once (npm run test in web/ and packages/k8s-ui)"; fi
  fi
}
echo "== done"
