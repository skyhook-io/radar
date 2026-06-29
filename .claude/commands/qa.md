---
description: Radar verification — type-check, tests, and visual-test only when a UI change warrants it
---

# QA — radar

What "verify this change" means in radar. Run the checks that fit the *delta*;
skip the rest. Don't run everything reflexively.

## Code changed → type-check + tests
- `make tsc` — frontend type-check. **Never** `npm run lint` (broken).
- `make test` — unit tests.
- `make build` — only before a PR, or when Go changed (does FE build + embed +
  binary; heavier, so not every round).

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
`make tsc` / `make test` as relevant — no visual-test.
