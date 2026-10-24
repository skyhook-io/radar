import { describe, expect, it } from 'vitest'
import { searchTrivyFindings, trivyReportImageRef } from './resource-utils-trivy'

const vuln = {
  metadata: { name: 'replicaset-billing-5cfccf7776-php-nginx', namespace: 'legacy' },
  report: {
    registry: { server: 'index.docker.io' },
    artifact: { repository: 'trafex/php-nginx', tag: '3.6.0', digest: 'sha256:abc' },
    vulnerabilities: [
      { vulnerabilityID: 'CVE-2025-0001', resource: 'libssl3', installedVersion: '3.3.2-r0', fixedVersion: '3.3.3-r0' },
      { vulnerabilityID: 'CVE-2026-109618', resource: 'php83', installedVersion: '8.3.20-r0', fixedVersion: '8.3.36-r0' },
      { vulnerabilityID: 'CVE-2026-0002', resource: 'busybox', installedVersion: '1.36.1-r29' },
    ],
  },
}
const secrets = {
  metadata: { name: 'replicaset-billing-secrets', namespace: 'legacy' },
  report: { secrets: [{ ruleID: 'aws-access-key-id', target: '/var/www/html/.env', match: 'AWS_ACCESS_KEY_ID=AKIAREVIEWFIXTURE01' }] },
}
const audit = {
  metadata: { name: 'replicaset-api-8467889d75', namespace: 'team-b' },
  report: { checks: [{ checkID: 'KSV012', success: false }, { checkID: 'KSV001', success: true }] },
}
const sbom = {
  metadata: { name: 'replicaset-web-sbom', namespace: 'shop' },
  report: { components: { components: [{ name: 'guzzlehttp/guzzle', version: '7.8.1' }] } },
}

describe('searchTrivyFindings', () => {
  it('matches a CVE and names the finding with its versions', () => {
    const { matches, truncated } = searchTrivyFindings([vuln, secrets], 'cve-2026-109618')
    expect(truncated).toBe(false)
    expect([...matches.keys()]).toEqual([vuln])
    expect(matches.get(vuln)).toEqual({ site: 'finding', text: 'CVE-2026-109618 php83 8.3.20-r0 → 8.3.36-r0' })
  })

  it('matches packages, unfixed findings, SBOM components and failed checks', () => {
    const text = (r: any, term: string) => searchTrivyFindings([r], term).matches.get(r)?.text
    expect(text(vuln, 'php83')).toContain('php83')
    expect(text(vuln, 'busybox')).toBe('CVE-2026-0002 busybox 1.36.1-r29 → unfixed')
    expect(text(sbom, 'guzzle')).toBe('guzzlehttp/guzzle 7.8.1')
    expect(text(audit, 'ksv012')).toBe('KSV012')
  })

  it('tells an image match apart from a finding', () => {
    expect(searchTrivyFindings([vuln], 'sha256:abc').matches.get(vuln)).toEqual({ site: 'image', text: 'index.docker.io/trafex/php-nginx:3.6.0@sha256:abc' })
  })

  it('treats the term as text, not a pattern', () => {
    expect(searchTrivyFindings([vuln], '8.3.20-r0 →').matches.has(vuln)).toBe(true)
    expect(searchTrivyFindings([vuln], 'php8.').matches.size).toBe(0)
  })

  it('caps a finding at 256 UTF-8 bytes for phrases, like server search', () => {
    const wide = { metadata: { name: 'r', namespace: 'n' }, report: { components: { components: [{ name: 'é'.repeat(128), version: 'needle' }] } } }
    expect(searchTrivyFindings([wide], 'é needle').matches.size).toBe(0)
    expect(searchTrivyFindings([wide], 'needle').matches.size).toBe(1)
  })

  it('matches a phrase across one finding, never across findings', () => {
    expect(searchTrivyFindings([vuln], 'php83 8.3.20').matches.has(vuln)).toBe(true)
    expect(searchTrivyFindings([vuln], 'libssl3 8.3.20').matches.has(vuln)).toBe(false)
  })

  it('never searches a passing check or a secret finding\'s matched text', () => {
    expect(searchTrivyFindings([audit], 'ksv001').matches.size).toBe(0)
    expect(searchTrivyFindings([secrets], 'akiareview').matches.size).toBe(0)
    expect(searchTrivyFindings([secrets], 'aws-access-key-id').matches.get(secrets)?.text).toBe('aws-access-key-id /var/www/html/.env')
  })

  it('skips rows whose name or namespace already match', () => {
    expect(searchTrivyFindings([vuln], 'legacy').matches.size).toBe(0)
  })

  it('stops at the findings limit and says the search was cut short', () => {
    const { matches, truncated } = searchTrivyFindings([vuln, vuln], 'cve-2026-0002', 4)
    expect(truncated).toBe(true)
    expect(matches.size).toBe(1)
  })
})

describe('trivyReportImageRef', () => {
  it('renders registry/repository:tag@digest like server search', () => {
    expect(trivyReportImageRef(vuln)).toBe('index.docker.io/trafex/php-nginx:3.6.0@sha256:abc')
    expect(trivyReportImageRef({ report: {} })).toBe('')
  })
})
