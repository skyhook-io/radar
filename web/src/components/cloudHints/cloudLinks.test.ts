import { describe, expect, it } from 'vitest'
import { alertRuleUrl, cloudResourceUrl, clusterIdFromUrl } from './cloudLinks'

function decodeFragment(url: string): Record<string, unknown> {
  const frag = new URL(url).hash.replace(/^#radar-rule=/, '')
  const b64 = frag.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4))
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0))))
}

const subject = {
  intent: 'alert' as const,
  category: 'Crash loop',
  categoryRaw: 'crashloop',
  issueId: 'a1b2c3',
  kind: 'Deployment',
  group: 'apps',
  name: 'checkout-api',
  namespace: 'shop',
  context: 'prod-eu',
}

describe('clusterIdFromUrl', () => {
  it('reads the cluster ID from a Radar Cloud cluster URL', () => {
    expect(clusterIdFromUrl('https://app.radarhq.io/c/cl_123')).toBe('cl_123')
    expect(clusterIdFromUrl('https://app.radarhq.io/c/cl_123/resources/pods')).toBe('cl_123')
  })
  it('returns nothing for anything else', () => {
    expect(clusterIdFromUrl('https://app.radarhq.io/clusters/one')).toBeUndefined()
    expect(clusterIdFromUrl('not a url')).toBeUndefined()
    expect(clusterIdFromUrl(undefined)).toBeUndefined()
  })
})

describe('alertRuleUrl', () => {
  it('puts the issue in the fragment, never the query string', () => {
    const url = alertRuleUrl('https://app.radarhq.io', subject, { clusterId: 'cl_123' })!
    const u = new URL(url)
    expect(u.pathname).toBe('/settings/organization/notifications')
    expect(u.search).not.toContain('checkout-api')
    expect(u.search).not.toContain('shop')
    expect(decodeFragment(url)).toEqual({
      v: 1,
      issue_id: 'a1b2c3',
      cluster_id: 'cl_123',
      category: 'crashloop',
      kind: 'Deployment',
      name: 'checkout-api',
      namespace: 'shop',
    })
  })

  it('names the context only when the cluster ID is unknown', () => {
    expect(decodeFragment(alertRuleUrl('https://app.radarhq.io', subject, { context: 'prod-eu' })!)).toMatchObject({ context: 'prod-eu' })
    expect(decodeFragment(alertRuleUrl('https://app.radarhq.io', subject, { clusterId: 'cl_1', context: 'x' })!)).not.toHaveProperty('context')
  })

  it('has no link without the issue ID to pin', () => {
    expect(alertRuleUrl('https://app.radarhq.io', { ...subject, issueId: undefined }, { clusterId: 'cl_1' })).toBeNull()
  })

  it('round-trips non-ASCII names', () => {
    const url = alertRuleUrl('https://app.radarhq.io', { ...subject, name: 'café-api' }, { clusterId: 'cl_1' })!
    expect(decodeFragment(url).name).toBe('café-api')
  })
})

describe('cloudResourceUrl', () => {
  it('opens the same resource inside the cluster page, starting nothing', () => {
    const url = cloudResourceUrl('https://app.radarhq.io/c/cl_123/', subject)!
    const u = new URL(url)
    expect(u.pathname).toBe('/c/cl_123/resources/deployments')
    expect(u.searchParams.get('resource')).toBe('shop/checkout-api')
    expect(u.searchParams.get('apiGroup')).toBe('apps')
    expect(url).not.toContain('investigate')
  })

  it('keeps a query the cluster URL already carries', () => {
    const u = new URL(cloudResourceUrl('https://app.radarhq.io/c/cl_123?utm_source=radar-oss&utm_term=team-findings', subject)!)
    expect(u.pathname).toBe('/c/cl_123/resources/deployments')
    expect(u.searchParams.get('utm_term')).toBe('team-findings')
    expect(u.searchParams.get('resource')).toBe('shop/checkout-api')
  })

  it('builds from the cluster, whatever page of it the link pointed at', () => {
    const u = new URL(cloudResourceUrl('https://app.radarhq.io/c/cl_123/resources/pods?apiGroup=x', { ...subject, group: '' })!)
    expect(u.pathname).toBe('/c/cl_123/resources/deployments')
    expect(u.searchParams.has('apiGroup')).toBe(false)
  })

  it('gives no link for anything but a cluster page', () => {
    expect(cloudResourceUrl('https://app.radarhq.io/clusters', subject)).toBeNull()
    expect(cloudResourceUrl('not a url', subject)).toBeNull()
  })
})
