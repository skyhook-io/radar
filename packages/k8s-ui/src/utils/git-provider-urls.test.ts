import { describe, it, expect } from 'vitest'
import { buildRepoBrowseUrl, buildPathBrowseUrl } from './git-provider-urls'

describe('buildRepoBrowseUrl', () => {
  it('returns canonical https for github https URL', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment')).toBe(
      'https://github.com/KoalaOps/deployment'
    )
  })

  it('strips trailing .git', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment.git')).toBe(
      'https://github.com/KoalaOps/deployment'
    )
  })

  it('normalises SSH form to https', () => {
    expect(buildRepoBrowseUrl('git@github.com:KoalaOps/deployment.git')).toBe(
      'https://github.com/KoalaOps/deployment'
    )
  })

  it('handles trailing slash', () => {
    expect(buildRepoBrowseUrl('https://github.com/KoalaOps/deployment/')).toBe(
      'https://github.com/KoalaOps/deployment'
    )
  })

  it('keeps unknown hosts (still a valid link)', () => {
    expect(buildRepoBrowseUrl('https://git.internal.example.com/team/proj')).toBe(
      'https://git.internal.example.com/team/proj'
    )
  })

  it('preserves non-default port in self-hosted URLs', () => {
    expect(buildRepoBrowseUrl('https://gitea.internal:3000/team/repo')).toBe(
      'https://gitea.internal:3000/team/repo'
    )
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

  it('rejects non-http(s) schemes after normalisation', () => {
    expect(buildRepoBrowseUrl('file:///tmp/repo')).toBe(null)
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
  it('builds /src URL', () => {
    expect(
      buildPathBrowseUrl('https://bitbucket.org/team/proj', 'src/app', 'develop')
    ).toBe('https://bitbucket.org/team/proj/src/develop/src/app')
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
})
