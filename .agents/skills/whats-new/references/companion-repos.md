# Optional docs and marketing changelog work

Use only for the companion work the user selected. The scope questions in
`../SKILL.md` distinguish checking coverage from editing it. Paths below are
relative to the Radar repo root unless noted otherwise.

## Prepare clean repositories before working

1. Check for sibling checkouts at `../radar-docs` and `../marketing` before
   searching elsewhere. Resolve their real paths and verify Git roots/remotes:
   `skyhook-dev/radar-docs` and `skyhook-dev/marketing`. Do not confuse another
   worktree or similarly named repo with the intended checkout. Read its agent
   guidance before changing Git state. If a sibling is
   absent, look for an existing local clone; otherwise clone the selected repo
   to a suitable sibling location rather than inventing its content.
2. Inspect each selected repo's tracked and untracked changes, current branch,
   worktree registrations, in-progress Git operations, and local-only commits.
   A clean status alone does not establish that its main branch is safe to use.
   Leave existing work, stashes, feature branches and unpushed history intact.
3. If the sibling is clean and safely usable on `main`, switch to main when
   needed and run **GPRA** to update it before the docs/changelog inspection.
   Resolve the remote default branch as the GPRA workflow requires; use that
   branch if it is not named main. Do not rebase someone else's feature branch
   or use autostash to borrow a dirty sibling.
4. If the sibling is dirty, on a branch that is not safe to displace, has
   local-only/diverged main commits, is mid-operation, or cannot safely switch
   to main, leave it untouched and create an isolated worktree. Fetch the remote
   default branch and create a new work branch/worktree from its remote tip.
   Run GPRA on the clean new worktree before inspecting or editing content.
5. GPRA means the available `gpra` workflow: resolve the remote default via
   `git ls-remote --symref origin HEAD`, inspect state, fetch, then
   `git pull origin <default> --rebase --autostash`. Never auto-resolve conflicts
   or force-push another branch; honor GPRA's force-push confirmation rule.
   If refreshing fails, report it and do not treat
   stale content as a completed current-main audit. In an isolated worktree,
   skip GPRA's optional local-default-branch move if that branch is checked out
   elsewhere or moving it would affect the original sibling. Update only the
   new work branch; leave the sibling's branch and working files intact.
6. Read each repo's `CLAUDE.md`/`AGENTS.md` and applicable skill before work.
   Create a task branch before edits; never commit the content changes to main.
   Record each worktree created by this run: owning repo, absolute path and
   branch. Use a new real directory, not a symlink into an existing checkout.

This refresh and isolation procedure applies to selected companion repos, not
to rebasing or cleaning the user's active Radar work as a side effect.

## Documentation ownership

Check the selected docs against the same merged-work inventory as What's New:
new capabilities, changed defaults/actions, configuration, prerequisites, and
important limits. Report concrete missing or stale coverage with source
locations. A request to check docs is audit-only unless updates were included.

- `skyhook-io/radar`: `docs/` is authoritative for the synced OSS docs.
- `skyhook-dev/radar-docs`: public Mintlify docs, including curated pages and
  one-way-synced OSS pages. Read its `CLAUDE.md` and the current
  `scripts/sync-oss-docs.mjs` mapping before proposing edits.
- Do not hand-edit generated/synced MDX or images. Fix the OSS source and follow
  the docs repo's sync procedure; edit curated public pages in `radar-docs`.
  Check navigation when adding a public page.

## Marketing changelog: use its own skill

Before any changelog audit or update, locate and read the **Radar** changelog
skill in the prepared marketing checkout. The current entrypoint is
`../marketing/.claude/commands/changelog-radar.md` (`/changelog-radar`). If the
checkout moves that workflow, follow its repo guidance to the current source.
Do not substitute `changelog-skyhook` or recreate its rules from memory.

Read `content/radar-changelog.ts`'s header and recent entries as that workflow
requires. It owns curation, event dates/first-ship verification, OSS/cloud
surface tags, drafts/review, validation and PR preparation. Follow its draft
review requirements before writing. An audit-only answer does not authorize
updates, and an OSS What's New run does not authorize airing Cloud milestones.

In-app minor-release notes and the public changelog have different models: do
not paste one into the other, date an old capability to a recent polish PR,
claim unshipped work is live, or make a changelog entry for every PR. Radar's
marketing changelog has no docs-repo mirror; the docs coverage task is separate.
Prepare the chosen content without publishing or merging it as a side effect.

## Remove worktrees created for this run

Finish by cleaning up **every temporary worktree this run created**, including
audit-only worktrees. Apply cleanup on success, declined drafts, and failure.
Preserve intended edits in commits on a task branch first,
and push/open the PR when that is in scope. Removing the worktree must not lose
uncommitted or otherwise unpreserved work; a retained local branch can preserve
commits even when no PR is requested. Do not delete that branch automatically.

For each recorded worktree, inspect its status and verify the exact path with
`[ -L "$worktree_path" ]` and `readlink` before removal. Never remove through a
symlink: `git worktree remove <link>` can delete the live target. Verify the path
is the isolated worktree created by this run, then use
`git -C <owning-repo> worktree remove -- <absolute-worktree-path>` individually.
Never force removal, delete a pre-existing checkout, or batch-delete paths.

If cleanup is blocked, preserve the work and report the exact remaining path,
branch and reason. Do not silently leave temporary worktrees behind. Summarize
which companion repos were checked/updated, their branches or PRs, and which
created worktrees were removed.
