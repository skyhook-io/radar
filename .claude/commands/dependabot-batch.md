---
description: Review Radar's weekly Dependabot updates and batch only soaked, low-risk bumps into one tested PR
---

# Dependabot Batch

Replace Radar's open weekly Dependabot PRs with one evidence-backed dependency PR. Include only updates that have been published for at least 72 hours and look safe after upstream review and Radar-specific testing. Do not merge the resulting PR.

This command is end-to-end unless the user asks for an audit only or says not to create a PR. Creating the batch PR includes pushing its branch and closing only the Dependabot PRs it supersedes. It does not authorize merging, publishing, tagging, or changing release artifacts.

## Non-negotiable gates

- Measure soak from the target version's upstream publication time, never from the Dependabot PR creation time. Record the screening time in UTC. A version must be at least 72 hours old at that instant; do not round up or make exceptions without explicit user approval.
- Apply the gate to the final resolved dependency graph, including transitive upgrades and newly introduced packages. A lockfile or module resolver can move beyond the version named in a PR.
- Read the official release notes or changelog for every direct candidate across the complete old-to-new range. Review official release metadata and meaningful changes for every changed transitive dependency too. If notes are missing or vague, inspect the upstream compare/commit range; uncertainty raises risk rather than counting as evidence of safety.
- Use primary sources: upstream releases, changelogs, compare views, issue trackers/security advisories, npm registry metadata, the Go module proxy, and GitHub tag/release metadata. Dependabot's summary and compatibility score are leads, not the review.
- Do not execute lifecycle/install scripts from a candidate package before it clears the soak and review gates. `npm install --package-lock-only --ignore-scripts` or `npm ci --ignore-scripts` is appropriate while constructing/auditing the graph; run the repository's normal verification after the included set is approved.
- Keep the batch dependency-only. If an update requires Radar source changes, a migration, or a speculative compatibility fallback, exclude it and explain the needed follow-up. Version-locked companion packages may move together when upstream requires it, but each resolved version must clear the same review and soak gates.
- Preserve unrelated work. Start from current `origin/main` on a dedicated `deps/dependabot-batch-YYYY-MM-DD` branch, using a separate worktree when the current tree is dirty or in active use.

## 1. Inventory and reconcile

List all open PRs authored by `app/dependabot`; do not infer the set from labels or title text. For each PR, inspect its body, files, exact diff, commits, comments/reviews, and checks.

Fetch `origin` before taking the inventory. Repeat the fetch and reconciliation immediately before final QA so a long research pass cannot batch against a stale base or miss a newly superseded bot PR.

Before evaluating a PR, compare its target with current `origin/main`:

- If the target is already present, classify the PR as stale/already landed rather than batching it again.
- If its base versions no longer match, reconstruct the real old-to-new movement from current manifests and lockfiles.
- Treat grouped PRs as collections of resolved versions, not one indivisible title. Do not split ecosystems that must stay aligned, such as Kubernetes modules or React/React DOM, without upstream and repository evidence that the split is valid.

Capture a baseline of direct and resolved versions for every affected ecosystem before editing. Radar currently has root Go and npm dependency graphs plus GitHub Actions; inspect the repository rather than assuming that list is exhaustive.

## 2. Establish release age

For each proposed target and every resolved transitive addition/upgrade, record the authoritative publication timestamp and calculated age at screening:

- npm: use registry version/time metadata for the exact package and version.
- Go: use exact-version metadata from `proxy.golang.org` or equivalent `go list -m -json` data sourced from the module proxy.
- GitHub Actions: prefer the upstream GitHub release publication time or annotated-tag time. If only a commit timestamp is available, call out the weaker evidence; hold the update if a reliable 72-hour age cannot be established.

If a grouped update contains a too-fresh version, either pin/exclude that member when the ecosystem supports it cleanly or hold the group. Never let a package-manager refresh silently float to a newer, unsoaked version.

## 3. Review risk in Radar's actual usage

For every candidate:

1. Read every intervening release note/changelog entry and the upstream compare range. Look for breaking changes, removed/renamed exports, changed defaults, new runtime/toolchain requirements, migrations, security-sensitive behavior, yanks/retractions, and regressions reported against the release.
2. Search Radar for imports, commands, configuration, deep imports, generated output, and affected APIs. Judge the dependency by how this repository uses it, not by semver alone.
3. Review the individual Dependabot PR's checks and discussion. Diagnose failures; do not assume the combined batch will fix them.
4. Decide `include`, `hold for soak`, or `exclude for risk`, and write down the evidence. A major version is not automatically unsafe and a patch is not automatically safe.

Give extra scrutiny to auth/security, persistence, networking/protocols, serialization, Kubernetes/Helm behavior, desktop/runtime frameworks, compilers/bundlers, CSS/fonts/icons, and CI/release actions. Include these only when the affected Radar surface is understood and a meaningful targeted test is available. Hold documented breakage, unresolved upstream regressions, unexplained graph churn, or changes whose relevant behavior cannot be exercised with reasonable confidence.

## 4. Build the unified change

Apply only the accepted direct version changes, then regenerate affected module and lock files with the ecosystem's native tooling. Do not merge wholesale from Dependabot branches or accept unrelated resolver churn without understanding it.

Audit the final diff against the baseline:

- enumerate every direct and transitive version added, removed, or changed;
- re-run the 72-hour and changelog review for resolver-introduced movement;
- verify lockstep packages remain compatible;
- confirm no application source, generated frontend bundle, tag, release, or publishing configuration changed accidentally;
- run `git diff --check`, inspect the complete diff, and check worktree status after verification for generated artifacts.

If the accepted set becomes empty, do not create an empty PR. Report the holds/exclusions and close only PRs proven stale because their target is already on `main`.

## 5. Test according to the risk

Read `.claude/commands/qa.md` and follow it. Before opening the PR, dependency batches normally require:

- `make tsc`
- `make test`
- `make build`

Also run ecosystem integrity checks and targeted tests for the affected usage. Examples include `go mod verify`, tests for packages importing a changed Go runtime dependency, workspace tests for changed frontend runtime/tooling, cross-platform compilation for desktop/runtime changes, and a before/after supported check when a lint/test tool itself changes. Do not run the repository's known-broken npm lint command.

Dependency-only does not always mean visually inert. Consider `/visual-test` when React, routing, icons, fonts, Tailwind/PostCSS, or another dependency can materially change rendered output; otherwise explicitly report `visual-test: skipped` with the reason.

Exclude an update rather than wave through a relevant failed or unavailable test. Keep successful safe updates in the batch when they remain independent of the excluded one.

## 6. Open the PR and retire superseded bot PRs

Commit without `Co-Authored-By` trailers. Push the dedicated branch and open one PR whose description contains:

- the UTC screening time and exact 72-hour cutoff;
- an included table with original PR, dependency, old/new versions, publication time, soak age, and concise risk conclusion;
- all excluded/held PRs with concrete reasons;
- links to the upstream release notes/changelogs used for material conclusions;
- unexpected direct/transitive graph movement and why it is acceptable;
- Radar usage/risk notes and targeted tests for nontrivial bumps;
- exact verification results and explicit visual-test status;
- a `Supersedes #...` list for included Dependabot PRs.

Wait for the batch PR's CI and automated review results, triage failures/findings skeptically, and update the batch if needed. Do not retire viable Dependabot PRs while the replacement is failing or blocked.

Once the batch PR is green and ready for human merge, close each included Dependabot PR with a comment linking the batch PR. Close stale already-landed PRs with evidence of where the target landed. Leave held or risk-excluded PRs open unless the user explicitly asks to close them.

End with the PR link, included and excluded counts, any testing caveats, and confirmation that the PR was not merged.
