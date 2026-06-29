import { describe, it, expect } from 'vitest'
import { buildRepoBrowseUrl, buildPathBrowseUrl } from './git-provider-urls'

describe('buildRepoBrowseUrl', () => {
  // Pass-through semantics: link the user's URL exactly as configured, no rewriting.
  it('returns the input verbatim for https URL', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment')).toBe(
      'https://github.com/KoalaOps/deployment'
    )
  })

  it('preserves trailing .git suffix (no manipulation)', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment.git')).toBe(
      'https://github.com/KoalaOps/deployment.git'
    )
  })

  it('preserves trailing slash (no manipulation)', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment/')).toBe(
      'https://github.com/KoalaOps/deployment/'
    )
  })

  it('trims surrounding whitespace', () => {
    expect(buildRepoBrowseUrl('  https://github.com/o/r  ')).toBe(
      'https://github.com/o/r'
    )
  })

  it('keeps unknown hosts (still a valid http(s) link)', () => {
    expect(buildRepoBrowseUrl('https://git.internal.example.com/team/proj')).toBe(
      'https://git.internal.example.com/team/proj'
    )
  })

  it('preserves non-default port in self-hosted URLs', () => {
    expect(buildRepoBrowseUrl('https://gitea.internal:3000/team/repo')).toBe(
      'https://gitea.internal:3000/team/repo'
    )
  })

  it('preserves http:// scheme (does not silently upgrade to https)', () => {
    expect(buildRepoBrowseUrl('http://corp.example.com/team/repo')).toBe(
      'http://corp.example.com/team/repo'
    )
  })

  it('returns null for SCP-form (git@host:owner/repo) — not http(s)', () => {
    expect(buildRepoBrowseUrl('git@github.com:KoalaOps/deployment.git')).toBe(null)
  })

  it('returns null for ssh:// scheme', () => {
    expect(buildRepoBrowseUrl('ssh://git@github.com/KoalaOps/deployment.git')).toBe(null)
  })

  it('returns null for git+ssh:// scheme', () => {
    expect(buildRepoBrowseUrl('git+ssh://git@github.com/o/r.git')).toBe(null)
  })

  it('returns null for oci:// scheme (Helm OCI registry)', () => {
    expect(buildRepoBrowseUrl('oci://registry.example.com/charts/nginx')).toBe(null)
  })

  it('returns null for file:// scheme', () => {
    expect(buildRepoBrowseUrl('file:///tmp/repo')).toBe(null)
  })

  it('returns null for javascript: scheme (XSS-safe)', () => {
    expect(buildRepoBrowseUrl('javascript:alert(1)')).toBe(null)
  })

  it('returns null for empty / nullish input', () => {
    expect(buildRepoBrowseUrl('')).toBe(null)
    expect(buildRepoBrowseUrl(undefined)).toBe(null)
    expect(buildRepoBrowseUrl(null)).toBe(null)
    expect(buildRepoBrowseUrl('   ')).toBe(null)
  })

  it('returns null for non-URL garbage', () => {
    expect(buildRepoBrowseUrl('not a url')).toBe(null)
  })
})

describe('buildPathBrowseUrl - GitHub', () => {
  it('builds tree URL with branch ref', () => {
    expect(
      buildPathBrowseUrl(
        'https://github.com/KoalaOps/deployment',
        'argocd/addons/keda/keda/nonprod-cluster-us-east1',
        'main'
      )
    ).toBe(
      'https://github.com/KoalaOps/deployment/tree/main/argocd/addons/keda/keda/nonprod-cluster-us-east1'
    )
  })

  it('uses HEAD when targetRevision is empty', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', '')
    ).toBe('https://github.com/o/r/tree/HEAD/src')
  })

  it('uses HEAD when targetRevision is literally "HEAD"', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', 'HEAD')
    ).toBe('https://github.com/o/r/tree/HEAD/src')
  })

  it('passes SHA through as the ref (no special prefix)', () => {
    const sha = 'a'.repeat(40)
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', sha)
    ).toBe(`https://github.com/o/r/tree/${sha}/src`)
  })

  it('works with .git suffix on repo URL', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r.git', 'a/b', 'main')
    ).toBe('https://github.com/o/r/tree/main/a/b')
  })

  it('url-encodes path segments with spaces', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'dir with space/file', 'main')
    ).toBe('https://github.com/o/r/tree/main/dir%20with%20space/file')
  })

  it('drops empty path segments from double slashes', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'a//b', 'main')
    ).toBe('https://github.com/o/r/tree/main/a/b')
  })

  it('preserves slashes in branch names (feature/foo)', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', 'feature/foo')
    ).toBe('https://github.com/o/r/tree/feature/foo/src')
  })

  it('treats uppercase 40-hex as a SHA (no special prefix)', () => {
    const sha = 'A'.repeat(40)
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', sha)
    ).toBe(`https://github.com/o/r/tree/${sha}/src`)
  })
})

describe('buildPathBrowseUrl - GitLab', () => {
  it('builds /-/tree URL', () => {
    expect(
      buildPathBrowseUrl('https://gitlab.com/group/proj', 'src/app', 'main')
    ).toBe('https://gitlab.com/group/proj/-/tree/main/src/app')
  })

  it('supports nested subgroups (full group path before /-/tree)', () => {
    expect(
      buildPathBrowseUrl('https://gitlab.com/group/sub/proj', 'src', 'main')
    ).toBe('https://gitlab.com/group/sub/proj/-/tree/main/src')
  })
})

describe('buildPathBrowseUrl - Bitbucket', () => {
  it('builds /src URL with explicit ref', () => {
    expect(
      buildPathBrowseUrl('https://bitbucket.org/team/proj', 'src/app', 'develop')
    ).toBe('https://bitbucket.org/team/proj/src/develop/src/app')
  })

  // Bitbucket Cloud /src/ doesn't accept "HEAD" — better no link than a 404 link.
  it('returns null when ref is empty (HEAD not a valid Bitbucket ref token)', () => {
    expect(
      buildPathBrowseUrl('https://bitbucket.org/team/proj', 'src/app', '')
    ).toBe(null)
  })
  it('returns null when ref is literally "HEAD"', () => {
    expect(
      buildPathBrowseUrl('https://bitbucket.org/team/proj', 'src/app', 'HEAD')
    ).toBe(null)
  })
})

describe('buildPathBrowseUrl - Azure DevOps', () => {
  it('builds dev.azure.com URL with GB prefix for branches', () => {
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src/app',
        'main'
      )
    ).toBe(
      'https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src/app&version=GBmain'
    )
  })

  it('uses GC prefix for SHA refs (lowercase)', () => {
    const sha = 'a'.repeat(40)
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src',
        sha
      )
    ).toBe(
      `https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src&version=GC${sha}`
    )
  })

  it('uses GC prefix for SHA refs (uppercase)', () => {
    const sha = 'A'.repeat(40)
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src',
        sha
      )
    ).toBe(
      `https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src&version=GC${sha}`
    )
  })

  it('percent-encodes slashes in branch names (query-string context)', () => {
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src',
        'feature/foo'
      )
    ).toBe(
      'https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src&version=GBfeature%2Ffoo'
    )
  })

  it('omits version when ref is HEAD/empty', () => {
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src',
        'HEAD'
      )
    ).toBe('https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src')
  })

  it('supports legacy visualstudio.com host', () => {
    expect(
      buildPathBrowseUrl(
        'https://myorg.visualstudio.com/MyProject/_git/myrepo',
        'src',
        'main'
      )
    ).toBe(
      'https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src&version=GBmain'
    )
  })

  it('does not double-encode project/repo segments that arrived percent-encoded', () => {
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/My%20Project/_git/My%20Repo',
        'src',
        'main'
      )
    ).toBe(
      'https://dev.azure.com/myorg/My%20Project/_git/My%20Repo?path=/src&version=GBmain'
    )
  })
})

describe('buildPathBrowseUrl - unknown / no path', () => {
  it('returns null for unknown hosts', () => {
    expect(
      buildPathBrowseUrl('https://git.internal.example.com/team/proj', 'src', 'main')
    ).toBe(null)
  })

  it('returns null when path is empty', () => {
    expect(buildPathBrowseUrl('https://github.com/o/r', '', 'main')).toBe(null)
    expect(buildPathBrowseUrl('https://github.com/o/r', '   ', 'main')).toBe(null)
    expect(buildPathBrowseUrl('https://github.com/o/r', null, 'main')).toBe(null)
  })

  it('returns null when path has only slashes', () => {
    expect(buildPathBrowseUrl('https://github.com/o/r', '///', 'main')).toBe(null)
  })

  it('returns null for nullish repo URL', () => {
    expect(buildPathBrowseUrl(undefined, 'src', 'main')).toBe(null)
  })

  // Known host but path too short to identify owner+repo -> downgrade to unknown.
  // Pins the `pathParts.length < 2` guard across all three slash-providers.
  it('returns null when github URL has only owner segment', () => {
    expect(buildPathBrowseUrl('https://github.com/onlyowner', 'src', 'main')).toBe(null)
  })
  it('returns null when bitbucket URL has only owner segment', () => {
    expect(buildPathBrowseUrl('https://bitbucket.org/onlyowner', 'src', 'main')).toBe(null)
  })
  it('returns null when gitlab URL has only one segment', () => {
    expect(buildPathBrowseUrl('https://gitlab.com/onlyone', 'src', 'main')).toBe(null)
  })

  // Azure DevOps `_git` index arithmetic — high-risk branch.
  it('returns null when dev.azure.com URL is missing _git', () => {
    expect(
      buildPathBrowseUrl('https://dev.azure.com/myorg/MyProject/myrepo', 'src', 'main')
    ).toBe(null)
  })
  it('returns null when dev.azure.com URL ends with _git (no repo segment)', () => {
    expect(
      buildPathBrowseUrl('https://dev.azure.com/myorg/MyProject/_git', 'src', 'main')
    ).toBe(null)
  })
  it('returns null when dev.azure.com URL has _git but no project (gitIdx < 2)', () => {
    expect(
      buildPathBrowseUrl('https://dev.azure.com/myorg/_git/myrepo', 'src', 'main')
    ).toBe(null)
  })
  it('returns null when visualstudio.com URL is missing _git', () => {
    expect(
      buildPathBrowseUrl('https://myorg.visualstudio.com/MyProject/myrepo', 'src', 'main')
    ).toBe(null)
  })
  it('returns null when visualstudio.com URL ends with _git', () => {
    expect(
      buildPathBrowseUrl('https://myorg.visualstudio.com/MyProject/_git', 'src', 'main')
    ).toBe(null)
  })

  // Pin .toUpperCase() behavior so a refactor to === 'HEAD' would fail loudly.
  it('treats lowercase "head" the same as "HEAD" (falls back to default branch)', () => {
    expect(
      buildPathBrowseUrl('https://github.com/o/r', 'src', 'head')
    ).toBe('https://github.com/o/r/tree/HEAD/src')
  })

  // Pin SHA_RE's exact-40-char requirement. Loosening to {7,40} would mis-prefix
  // short-hex branch names like "abc1234" as Azure commits (GC) instead of branches (GB).
  it('does not treat 7-char hex as a SHA on Azure (stays GB)', () => {
    expect(
      buildPathBrowseUrl(
        'https://dev.azure.com/myorg/MyProject/_git/myrepo',
        'src',
        'abc1234'
      )
    ).toBe(
      'https://dev.azure.com/myorg/MyProject/_git/myrepo?path=/src&version=GBabc1234'
    )
  })
})
