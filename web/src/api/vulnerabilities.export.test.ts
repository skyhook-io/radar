import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './client'
import { fetchVulnerabilitiesCSV, vulnerabilitiesCSVFilename } from './vulnerabilities'

afterEach(() => vi.unstubAllGlobals())

describe('fetchVulnerabilitiesCSV', () => {
  it('asks for the view and filters the table shows', async () => {
    const fetch = vi.fn(async () => new Response('cluster,scanned,status\r\n', { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    const csv = await fetchVulnerabilitiesCSV({
      view: 'fix', namespaces: ['demo', 'prod'], severities: ['critical'], fixableOnly: true, query: ' nginx ',
      workload: { kind: 'Deployment', group: 'apps', namespace: 'demo', name: 'web' },
    })
    expect(csv).toBe('cluster,scanned,status\r\n')
    const url = new URL(String((fetch.mock.calls[0] as unknown[])[0]), 'http://radar')
    expect(url.pathname).toMatch(/\/vulnerabilities\/export$/)
    expect(Object.fromEntries(url.searchParams)).toEqual({
      view: 'fix', namespaces: 'demo,prod', severity: 'critical', fixable: '1', q: 'nginx',
      workloadKind: 'Deployment', workloadNamespace: 'demo', workloadName: 'web', workloadGroup: 'apps',
    })
  })

  it('fails with the server message instead of saving an error page', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'vulnerability reports aren\'t ready yet', error_code: 'kind_sync_pending' }), {
      status: 503, headers: { 'Content-Type': 'application/json' },
    })))
    const err = await fetchVulnerabilitiesCSV({ view: 'cve' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(503)
    expect((err as ApiError).message).toBe('vulnerability reports aren\'t ready yet')
  })
})

describe('vulnerabilitiesCSVFilename', () => {
  const day = new Date(2026, 0, 5, 23, 30)
  it('names the view, the cluster and the local date', () => {
    expect(vulnerabilitiesCSVFilename('cve', 'k8sbox-rvuln', day)).toBe('vulnerabilities-by-cve-k8sbox-rvuln-2026-01-05.csv')
  })
  it('keeps the name filesystem-safe', () => {
    expect(vulnerabilitiesCSVFilename('image', 'arn:aws:eks:us-east-1:123:cluster/prod', day)).toBe('vulnerabilities-by-image-arn-aws-eks-us-east-1-123-cluster-prod-2026-01-05.csv')
    expect(vulnerabilitiesCSVFilename('fix', '../..', day)).toBe('vulnerabilities-by-fix-cluster-2026-01-05.csv')
  })
})
