---
description: Radar verification — type-check, tests, and visual-test only when a UI change warrants it
---

# QA — radar

What "verify this change" means in radar. Run the checks that fit the *delta*;
skip the rest. Don't run everything reflexively.

## Fast dev loop first — the full suite runs once

Verification has two tiers. Use the fast one while iterating; run the full one
once, at the end. A fast loop is the priority: never wait on a full suite to
check a small fix.

**Iteration (every round): `make test-changed`** (`scripts/test-changed.sh`).
It vets and tests only the changed Go packages (root and `pkg/` modules), runs
`tsc` for `web/` and `packages/k8s-ui`, and `vitest related` on the changed
frontend files. Typically minutes rather than the ~20 a full run takes.
Go results come from the test cache when nothing a package depends on changed;
`GO_TEST_FRESH=1` forces a real run (e.g. to recheck a flaky test).
`GO_TEST_RUN=<regexp>` narrows a big package (`internal/server` alone takes
~4.5 min); use it when the change only touches one area there. **Never**
`npm run lint` (broken).

**Completion (once, after the final batch):** CI on the pushed head is the full
run: push and let it go; don't repeat it locally. Run the full local suite
(`make test` plus `npm run test` in `web/` and `packages/k8s-ui`) only for a
branch CI won't run (e.g. a PR stacked on another PR's branch), and then only
once. `make build` is for running the binary (visual or live checks), not for
verification rounds.

**Keep it fast in practice:**
- Batch fixes, then run the fast tier once for the batch, not after each edit.
- Don't run suites while other heavy work (builds, other test runs, agents)
  runs on the machine: it slows everything and creates false failures.
- A failure in a file the change doesn't touch, under load, is load until shown
  otherwise: rerun that file alone (`npx vitest run <file>` / `go test -run`)
  before investigating. Several UI suites (e.g. `ResourcesView.kind-view`) and
  `TestUpgradeReadinessEndpointLargeMetrics` are timing-sensitive.
- Vitest keeps transformed modules in `.vitest-cache/` in each project (one per
  worktree, gitignored), so a second run skips most transforms.
- `packages/k8s-ui` runs most test files without isolation: a worker reuses its
  modules across files (`vitest.config.ts`). Files that call `vi.mock` run
  isolated, picked automatically from their source. A test that passes alone but
  fails in the suite is probably leaking or reading module state from another
  file: reset that state in the tests (see `touch-intent.test.ts`). Shuffling
  the file order finds such pairs:
  `npx vitest run --project shared --sequence.shuffle.files --sequence.seed=<n>`.
- Node 25: run vitest with `NODE_OPTIONS=--no-experimental-webstorage`
  (the script sets it).

## UI changed → `/visual-test` (lean toward it for feature-scale UI; skip is OK for small)
For a change that adds or meaningfully changes **rendered surfaces** — a new
renderer / drawer / dialog / page / flow — **lean toward running `/visual-test`**:
that's where it earns its keep, catching what `tsc`/`test` can't (does it actually
render, read clearly, and behave). For small or non-visual changes (logic, utils,
server/Go, types, refactors, copy, a tiny tweak) **skipping is fine** — don't
ceremony-ize it. When unsure on a broad change, **ask**. Sometimes skipping a
genuine UI change is still the right call — just make it a *decision*, and report
it (below). (Aligns with radar's CLAUDE.md "consider visual-test" note.)

**Always report visual-test status explicitly** — never leave it ambiguous (a bare
`tsc ✓ · test ✓` with no visual-test entry reads as "did it run or not?"):
- **Ran** → state the count **and the screenshot dir as an absolute path** (or a
  `file://` URL) so the terminal linkifies it and the user clicks straight to the
  captures — e.g. `visual-test: ran · 6 shots · /Users/.../radar/.playwright-mcp/visual-test/<run>/`.
  Resolve `$SCREENSHOT_DIR` (from `visual-test-start.sh`) or `$(pwd)/.playwright-mcp/...`
  to a full path. **Never a relative `.playwright-mcp/...` or a `~/...` path — those
  don't reliably linkify in the terminal.**
- **Skipped** → say so with the reason: `visual-test: skipped (no UI delta)`.

## Pure backend / util / type / docs
`make test-changed` — no visual-test. Docs-only: nothing to run.
