---
name: whats-new
description: Prepare, update, audit, or preview Radar's in-app What's New release notes. Use when selecting release highlights from merged work, scoring cards, checking upgrade announcements, coordinating optional docs/changelog coverage, or showing the dialog locally.
---

# Radar What's New

Turn the release's actual work into useful, accurate highlights. Start with the
whole release range, group related changes by user benefit, and use the existing
scoring and acknowledgement model. Paths below are relative to the repo root.

## Ask about companion work

Unless the request or earlier instructions already answer these, ask together:

1. Should this also check for missing or outdated documentation in Radar OSS,
   `radar-docs`, or both?
2. Should this also check the Radar changelog in `marketing`, and is the scope
   audit-only or preparing/updating entries?

Honor explicit inclusions and skips; do not ask again when already instructed.
Continue the core What's New work while awaiting answers, but do not start the
optional work without an answer. Report unanswered companion scope as pending,
not as approved or silently completed.

When either is included, read [companion-repos.md](references/companion-repos.md)
for sibling discovery, clean-main/GPRA versus isolated-worktree handling, docs
ownership, the marketing changelog skill, and mandatory worktree cleanup.

## Read the contract

- `web/src/components/whats-new/releaseNotes.ts`: catalog, importance scale,
  composition, weights, card/line limits, and auto-open threshold.
- `web/src/components/whats-new/WhatsNew.tsx`: Home-only announcement,
  acknowledgement, preview parameters, and local/shared/cloud behavior.
- `WhatsNew.test.ts` and `WhatsNewDialog.test.tsx` in that directory: pure
  composition and rendered-dialog regression coverage.
- `scripts/check-whats-new.sh`: release-entry gate. A passing gate proves that
  an entry exists, not that its copy is accurate or that it auto-opens.
- `DESIGN.md` for UI changes. Read `internal/server/whats_new.go` and the API
  hooks in `web/src/api/client.ts` if changing storage or acknowledgement.

## Gather and group before choosing cards

1. Establish the target version/ref and previous minor-release tag. Include all
   merged work since that tag, including patches already merged for the target
   minor. Refresh remote history when the task asks what has merged recently.
   Use `git log --reverse --format='%h %s' <previous-minor-tag>..<target-ref>` as
   an inventory, then inspect the actual diffs, producer code, and area docs.
   A commit count is not a PR count, and a commit subject is not proof of a claim.
2. Build candidate buckets around coherent user benefits, not one card per PR.
   Several related fixes can make a substantial improvement together. For
   example, reading-position stability, selection without auto-scroll, and
   container-start waiting belong in **Log viewer improvements**.
3. Compare candidates against the existing cards and previous releases before
   deciding that nothing else makes the cut. Keep separate outcomes separate;
   do not split one improvement across cards just to count it twice.
4. Show the proposed titles, included work, evidence, importance, and resulting
   composition. Surface real editorial choices before changing the release
   story. For a narrow wording edit, keep the scope narrow.

## Score and write honestly

- Use the importance scale in the catalog. A useful bucket need not be a major
  new feature; polish still deserves a lower score. Do not inflate importance
  to obtain automatic opening, and do not rule out a bucket just because its
  constituent PRs are fixes.
- Compute the displayed cards' weighted sum using `composeReleaseNotes` and
  check `whatsNewToOpen`. Show both a direct upgrade and skipped releases:
  which cards lead, which fall into the tail, and whether the threshold is met.
  Compare weighted scores, not just the raw importance of older releases.
- Keep the announcement schema and gate unchanged unless the user actually
  requests a policy change. Do not add an override or change the threshold as
  an unrequested shortcut when the intended approach is scored highlights.
- Use at most the current card and line limits; they are caps, not quotas.
  Prefer a coherent set to filler. Do not repeat a promoted highlight in the
  tail. Preserve material behavior changes when refreshing improvement lines.
- Titles name the capability or improvement; descriptions explain its concrete
  benefit. Broad buckets need broad, factual framing: traffic warnings,
  scheduling failures, and incomplete checks are not all workload blockers.
- Respect the catalog's description-length and lead-description constraints.
  Keep detailed qualifications in the lead when needed, rather than making
  every short card an implementation checklist.

## Verify claims at the surface users see

For each new or revised claim, identify its implementation and where it appears:
UI, REST, MCP, or only a conditional integration. Compare with the previous
release so established behavior is not presented as newly added.

Trace data through **producer → filtering/authorization → adapter → renderer**.
A diff in history or a renderer's ability to display a field does not prove
that the investigation feed delivers it. Likewise, rich search API snippets do
not prove that the UI row displays workload or version details. Inspect both.

Use producer behavior to resolve disagreements with docs or reviewers. Narrow
unsupported copy instead of silently building a missing feature. Distinguish
recorded declarations, observed evidence, and verified outcomes. Explain
important integration requirements and coverage limits without adding unrelated
warnings. Preserve consequential changes such as confirmed chart-source
requirements even when adding a more appealing discovery feature to the line.

For nontrivial regrouping or a requested audit, get an independent product/copy
review using the available review workflow. Give it the full range and scenario
list; triage findings against source and rendered evidence, not reviewer rank.
Do not churn accurate copy to satisfy optional preferences.

## Minor releases, patches, and delivery

- The current catalog is normally minor-level: patches use the latest entry at
  or below their running version. The code can support a separate patch entry;
  add one only when that is the intended announcement, not to force replay.
- An unacknowledged minor entry can open on a later patch if its composed score
  meets the threshold. Once acknowledged, editing that entry does not make it
  unread again. Fresh installs stay silent; deep links keep the unread dot.
- Already-published binaries and tags do not change when the catalog changes.
  State which next build can deliver the update. Tests with a controlled
  version are policy coverage, not proof of retroactively changing a release.
- When patch suitability is in scope, assess the overall size and new major
  capabilities. Small refinements to existing features can fit a patch; flag
  material behavior/permission changes separately. Publishing remains subject
  to `CLAUDE.md`'s release authorization rule.

## Check and preview

Follow `.claude/commands/qa.md`, scaling checks to the delta. For catalog edits:

```bash
make tsc
(cd web && ../node_modules/.bin/vitest run src/components/whats-new)
```

Run `bash scripts/check-whats-new.sh <version>` when checking the release-entry
gate. Before a PR or local preview, use **`make build`**: the Go server embeds
`internal/static/dist`, so a frontend-only build can serve stale assets.

For a safe content preview without a real cluster, use the existing fixture:

```bash
go run ./cmd/testserver -port 19389
```

Choose an unused port, wait for readiness, and open
`http://127.0.0.1:19389/?whats-new=vX.Y` using the target minor. Add
`&whats-new-from=vA.B.0` to inspect a skipped upgrade. The fixture seeds fake
Kubernetes data and isolates config writes; label that evidence as fixture data.
Keep a requested preview running and return its URL. Do not kill unrelated
processes or use a live cluster just to preview release copy.

**Preview mode forces the dialog and never acknowledges it.** It does not prove
that an upgrade auto-opens. For announcement changes, retain or add meaningful
regressions for direct/skipped upgrades, fresh installs, deep links,
acknowledged patches, local/shared storage, and preview/cloud guards as relevant.
Use a built-UI fixture browser smoke when runtime proof is warranted; controlled
version/seen responses must be disclosed. Inspect the rendered copy and card
layout. Use real-cluster visual testing only when the feature under review needs
it; a fake fixture is not a real-cluster test.

Finish with the final card scores/composition, intended announcement behavior,
checks and their revision, preview URL when running, and any factual or delivery
gaps. Include companion docs/changelog results and cleanup of created worktrees.
Update the full PR description around the final behavior, not review history.
Keep unresolved reviewer disagreements and deferred findings visible.
