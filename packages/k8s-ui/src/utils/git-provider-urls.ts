type ParsedRepo =
  | { provider: 'github' | 'gitlab' | 'bitbucket'; browseUrl: string; owner: string; repo: string }
  | { provider: 'azure-devops'; browseUrl: string; org: string; project: string; repo: string }
  | { provider: 'unknown'; browseUrl: string }

const SHA_RE = /^[0-9a-f]{40}$/i

function stripDotGit(s: string): string {
  return s.replace(/\.git$/i, '')
}

// `git@host:owner/repo` lacks a scheme that `new URL` accepts; rewrite to https for parsing.
function sshToHttps(input: string): string {
  const m = input.match(/^git@([^:]+):(.+)$/)
  if (!m) return input
  return `https://${m[1]}/${m[2]}`
}

function encodePath(path: string): string {
  return path
    .split('/')
    .filter(seg => seg.length > 0)
    .map(encodeURIComponent)
    .join('/')
}

// Refs (branches) can contain slashes — feature/foo — which providers serve as
// literal path segments. encodeURIComponent would turn those into %2F and 404.
function encodeRef(ref: string): string {
  return ref.split('/').map(encodeURIComponent).join('/')
}

function parseRepoUrl(repoURL: string | undefined | null): ParsedRepo | null {
  if (!repoURL) return null
  const trimmed = repoURL.trim()
  if (!trimmed) return null

  let url: URL
  try {
    url = new URL(stripDotGit(sshToHttps(trimmed)))
  } catch {
    return null
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return null

  // host includes the port (when non-default) so self-hosted servers on custom ports stay reachable.
  const host = url.host.toLowerCase()
  const hostname = url.hostname.toLowerCase()
  const pathParts = url.pathname.replace(/^\/+/, '').replace(/\/+$/, '').split('/')
  // Preserve the input scheme — self-hosted http-only servers shouldn't be silently upgraded to https.
  const browseUrl = `${url.protocol}//${host}${url.pathname.replace(/\/+$/, '')}`

  if (hostname === 'github.com' || hostname === 'bitbucket.org') {
    if (pathParts.length < 2) return { provider: 'unknown', browseUrl }
    return {
      provider: hostname === 'github.com' ? 'github' : 'bitbucket',
      browseUrl,
      owner: pathParts[0],
      repo: pathParts[1],
    }
  }

  if (hostname === 'gitlab.com') {
    // GitLab supports nested groups: the owner segment may itself be a slash-joined path.
    if (pathParts.length < 2) return { provider: 'unknown', browseUrl }
    return {
      provider: 'gitlab',
      browseUrl,
      owner: pathParts.slice(0, -1).join('/'),
      repo: pathParts[pathParts.length - 1],
    }
  }

  // Azure DevOps URL shape: /{org}/{project}/_git/{repo} (dev.azure.com)
  // or {org}.visualstudio.com/{project}/_git/{repo} (legacy).
  if (hostname === 'dev.azure.com') {
    const gitIdx = pathParts.indexOf('_git')
    if (gitIdx < 2 || gitIdx + 1 >= pathParts.length) {
      return { provider: 'unknown', browseUrl }
    }
    return {
      provider: 'azure-devops',
      browseUrl,
      org: pathParts.slice(0, gitIdx - 1).join('/'),
      project: pathParts[gitIdx - 1],
      repo: pathParts[gitIdx + 1],
    }
  }

  if (hostname.endsWith('.visualstudio.com')) {
    const gitIdx = pathParts.indexOf('_git')
    if (gitIdx < 1 || gitIdx + 1 >= pathParts.length) {
      return { provider: 'unknown', browseUrl }
    }
    return {
      provider: 'azure-devops',
      browseUrl,
      org: hostname.slice(0, -'.visualstudio.com'.length),
      project: pathParts.slice(0, gitIdx).join('/'),
      repo: pathParts[gitIdx + 1],
    }
  }

  return { provider: 'unknown', browseUrl }
}

export function buildRepoBrowseUrl(repoURL: string | undefined | null): string | null {
  const parsed = parseRepoUrl(repoURL)
  return parsed ? parsed.browseUrl : null
}

export function buildPathBrowseUrl(
  repoURL: string | undefined | null,
  path: string | undefined | null,
  targetRevision: string | undefined | null
): string | null {
  if (!path || !path.trim()) return null
  const parsed = parseRepoUrl(repoURL)
  if (!parsed || parsed.provider === 'unknown') return null

  const rawRef = (targetRevision ?? '').trim()
  // GitHub and GitLab browse URLs accept "HEAD" as a ref token that resolves
  // to the default branch. Bitbucket Cloud's /src/ path does not — see the
  // bitbucket case below.
  const hasExplicitRef = rawRef !== '' && rawRef.toUpperCase() !== 'HEAD'
  const ref = hasExplicitRef ? rawRef : 'HEAD'
  const encodedPath = encodePath(path)
  if (!encodedPath) return null

  switch (parsed.provider) {
    case 'github':
      return `https://github.com/${parsed.owner}/${parsed.repo}/tree/${encodeRef(ref)}/${encodedPath}`
    case 'gitlab':
      return `https://gitlab.com/${parsed.owner}/${parsed.repo}/-/tree/${encodeRef(ref)}/${encodedPath}`
    case 'bitbucket':
      // Bitbucket Cloud's /src/{ref}/... endpoint requires a real branch name
      // or commit hash; HEAD 404s. Without an explicit ref we can't build a
      // working deep link, so fall through to plain text.
      if (!hasExplicitRef) return null
      return `https://bitbucket.org/${parsed.owner}/${parsed.repo}/src/${encodeRef(ref)}/${encodedPath}`
    case 'azure-devops': {
      const isSha = hasExplicitRef && SHA_RE.test(rawRef)
      const versionParam = hasExplicitRef ? `&version=${isSha ? 'GC' : 'GB'}${encodeURIComponent(rawRef)}` : ''
      // org/project/repo are taken straight from url.pathname segments, which the URL
      // parser leaves percent-encoded — re-encoding would double-encode (My%20X → My%2520X).
      return `https://dev.azure.com/${parsed.org}/${parsed.project}/_git/${parsed.repo}?path=/${encodedPath}${versionParam}`
    }
  }
}
