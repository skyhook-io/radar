export type GitProvider = 'github' | 'gitlab' | 'bitbucket' | 'azure-devops' | 'unknown'

interface ParsedRepo {
  provider: GitProvider
  browseUrl: string
  segments?: {
    owner?: string
    repo?: string
    azureOrg?: string
    azureProject?: string
    azureRepo?: string
  }
}

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
  const browseUrl = `https://${host}${url.pathname.replace(/\/+$/, '')}`

  if (hostname === 'github.com' || hostname === 'bitbucket.org') {
    if (pathParts.length < 2) return { provider: 'unknown', browseUrl }
    return {
      provider: hostname === 'github.com' ? 'github' : 'bitbucket',
      browseUrl,
      segments: { owner: pathParts[0], repo: pathParts[1] },
    }
  }

  if (hostname === 'gitlab.com') {
    // GitLab supports nested groups: the owner segment may itself be a slash-joined path.
    if (pathParts.length < 2) return { provider: 'unknown', browseUrl }
    const repo = pathParts[pathParts.length - 1]
    const owner = pathParts.slice(0, -1).join('/')
    return { provider: 'gitlab', browseUrl, segments: { owner, repo } }
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
      segments: {
        azureOrg: pathParts.slice(0, gitIdx - 1).join('/'),
        azureProject: pathParts[gitIdx - 1],
        azureRepo: pathParts[gitIdx + 1],
      },
    }
  }

  if (hostname.endsWith('.visualstudio.com')) {
    const org = hostname.slice(0, -'.visualstudio.com'.length)
    const gitIdx = pathParts.indexOf('_git')
    if (gitIdx < 1 || gitIdx + 1 >= pathParts.length) {
      return { provider: 'unknown', browseUrl }
    }
    return {
      provider: 'azure-devops',
      browseUrl,
      segments: {
        azureOrg: org,
        azureProject: pathParts.slice(0, gitIdx).join('/'),
        azureRepo: pathParts[gitIdx + 1],
      },
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
  if (!parsed || parsed.provider === 'unknown' || !parsed.segments) return null

  const rawRef = (targetRevision ?? '').trim()
  // GitHub/GitLab/Bitbucket browse URLs accept "HEAD" as a valid ref token that
  // resolves to the repo's default branch, so it's a safe stand-in when the
  // ArgoCD Application leaves targetRevision empty.
  const hasExplicitRef = rawRef !== '' && rawRef.toUpperCase() !== 'HEAD'
  const ref = hasExplicitRef ? rawRef : 'HEAD'
  const encodedPath = encodePath(path)
  if (!encodedPath) return null

  switch (parsed.provider) {
    case 'github': {
      const { owner, repo } = parsed.segments
      return `https://github.com/${owner}/${repo}/tree/${encodeRef(ref)}/${encodedPath}`
    }
    case 'gitlab': {
      const { owner, repo } = parsed.segments
      return `https://gitlab.com/${owner}/${repo}/-/tree/${encodeRef(ref)}/${encodedPath}`
    }
    case 'bitbucket': {
      const { owner, repo } = parsed.segments
      return `https://bitbucket.org/${owner}/${repo}/src/${encodeRef(ref)}/${encodedPath}`
    }
    case 'azure-devops': {
      const { azureOrg, azureProject, azureRepo } = parsed.segments
      const isSha = hasExplicitRef && SHA_RE.test(rawRef)
      const versionParam = hasExplicitRef ? `&version=${isSha ? 'GC' : 'GB'}${encodeURIComponent(rawRef)}` : ''
      return `https://dev.azure.com/${encodeURIComponent(azureOrg!)}/${encodeURIComponent(azureProject!)}/_git/${encodeURIComponent(azureRepo!)}?path=/${encodedPath}${versionParam}`
    }
  }
  return null
}
