// shortRevisionForCommit normalizes a controller-reported revision to a
// commit-ish short form. Git SHAs collapse to 7 chars; revisions that
// already look short (tags, semver, "master@sha1:..." truncated) pass
// through. Returns empty when the input is empty/whitespace.
export function shortRevisionForCommit(revision: string): string {
  const trimmed = revision.trim()
  if (!trimmed) return ''
  // Flux records "master@sha1:9f4969..." — strip the prefix and shorten.
  const sha1Match = trimmed.match(/sha1:([0-9a-f]{7,40})/i)
  if (sha1Match) return sha1Match[1].slice(0, 7)
  // Pure 40-char SHA → 7 chars.
  if (/^[0-9a-f]{40}$/i.test(trimmed)) return trimmed.slice(0, 7)
  return trimmed
}

// gitTreeURL derives a "browse the source directory" URL from a joined
// summary.source ("repoURL · path · chart") plus a revision. Used when a
// resource is Missing — we can't show its live state, but we CAN point at
// where it's declared in Git so the operator can read the source. Returns
// null when the host isn't recognized; caller hides the affordance then.
export function gitTreeURL(source: string | undefined, revision: string): string | null {
  if (!source) return null
  const parts = source.split(' · ').map((s) => s.trim()).filter(Boolean)
  if (parts.length === 0) return null
  const repoOnly = parts[0]!
  const subPath = parts[1] || ''
  const clean = repoOnly.replace(/\.git$/, '').replace(/\/$/, '')
  let sha = revision || 'HEAD'
  const m = (revision || '').match(/sha1:([0-9a-f]{7,40})/i)
  if (m) sha = m[1]
  const pathSegment = subPath ? `/${encodeURI(subPath)}` : ''
  if (/^https?:\/\/github\.com\//.test(clean)) return `${clean}/tree/${sha}${pathSegment}`
  if (/^https?:\/\/gitlab\.com\//.test(clean)) return `${clean}/-/tree/${sha}${pathSegment}`
  if (/^https?:\/\/bitbucket\.org\//.test(clean)) return `${clean}/src/${sha}${pathSegment}`
  return null
}

// commitURLForRepo derives the remote URL for a commit when we recognize
// the source host. Returns null for unrecognized hosts (private gitea,
// self-hosted, etc.) — the caller renders the SHA as plain text in that
// case rather than a wrong-looking link.
//
// `source` arrives joined as "repoURL · path · chart" (see Summary.Source
// builder server-side). The repo URL is always the first segment; the rest
// are path/chart suffixes that would confuse the commit URL constructor.
export function commitURLForRepo(source: string | undefined, revision: string): string | null {
  if (!source || !revision) return null
  const repoOnly = source.split(' · ')[0].trim()
  const clean = repoOnly.replace(/\.git$/, '').replace(/\/$/, '')
  // Pull out a usable SHA: full or short SHA; or the sha1:... form Flux uses.
  let sha = revision
  const m = revision.match(/sha1:([0-9a-f]{7,40})/i)
  if (m) sha = m[1]
  if (!/^[0-9a-f]{7,40}$/i.test(sha)) return null
  // Recognized hosts. Self-hosted GitLab/Gitea variants would need the
  // server to surface a `commitUrlTemplate` annotation — out of scope here.
  if (/^https?:\/\/github\.com\//.test(clean)) return `${clean}/commit/${sha}`
  if (/^https?:\/\/gitlab\.com\//.test(clean)) return `${clean}/-/commit/${sha}`
  if (/^https?:\/\/bitbucket\.org\//.test(clean)) return `${clean}/commits/${sha}`
  return null
}

// Two-dot comparisons compare the two recorded trees, including after a rollback.
export function compareSyncURL(repo: string, previous: string | undefined, revision: string): string | null {
  if (!previous || previous === revision || !/^[0-9a-f]{40}$/i.test(previous) || !/^[0-9a-f]{40}$/i.test(revision)) return null
  const commit = commitURLForRepo(repo, revision)
  if (commit?.startsWith('https://github.com/')) return commit.replace(`/commit/${revision}`, `/compare/${previous}..${revision}`)
  if (commit?.startsWith('https://gitlab.com/')) return commit.replace(`/-/commit/${revision}`, `/-/compare/${previous}...${revision}?straight=true`)
  return null
}
