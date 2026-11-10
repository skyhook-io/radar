import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NamespaceRenderer, quotaUsageRatio } from './NamespaceRenderer'

describe('quotaUsageRatio', () => {
  // status.used is computed with quantity arithmetic, so a used pod count of
  // 1000 serializes canonically as "1k". A raw-float read sees 1 and reports a
  // saturated quota as barely used.
  it('reads count quantities with SI suffixes on either side of the ratio', () => {
    expect(quotaUsageRatio('pods', '1k', '1k')).toBe(1)
    expect(quotaUsageRatio('pods', '900', '1k')).toBeCloseTo(0.9)
    expect(quotaUsageRatio('count/pods', '1k', '2k')).toBeCloseTo(0.5)
  })

  it('leaves plain counts unchanged', () => {
    expect(quotaUsageRatio('pods', '5', '10')).toBe(0.5)
    expect(quotaUsageRatio('services', '0', '10')).toBe(0)
  })

  it('parses cpu and memory with their own unit parsers', () => {
    expect(quotaUsageRatio('cpu', '500m', '1')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('requests.cpu', '2', '4')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('memory', '512Mi', '1Gi')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('requests.storage', '1Gi', '2Gi')).toBeCloseTo(0.5)
  })

  it('yields null when hard is unset or unparseable so the row falls back to raw strings', () => {
    expect(quotaUsageRatio('pods', '5', '')).toBeNull()
    expect(quotaUsageRatio('pods', '5', 'garbage')).toBeNull()
  })
})

describe('namespace deletion diagnostics', () => {
  afterEach(() => vi.useRealTimers())

  it('shows controller blockers, namespace finalizers and time since deletion began', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-10T12:00:00Z'))
    const html = renderToString(createElement(NamespaceRenderer, { data: {
      metadata: {
        creationTimestamp: '2023-01-01T00:00:00Z',
        deletionTimestamp: '2026-10-10T10:00:00Z',
        finalizers: ['metadata.example.com/not-the-namespace-finalizer'],
      },
      spec: { finalizers: ['kubernetes'] },
      status: { phase: 'Terminating', conditions: [
        { type: 'NamespaceContentRemaining', status: 'True', reason: 'SomeResourcesRemain', message: 'Some resources are remaining: configmaps. has 1 resource instances' },
        { type: 'NamespaceFinalizersRemaining', status: 'True', reason: 'SomeFinalizersRemain', message: 'Some content has finalizers remaining: example.com/hold in 1 resource instances' },
        { type: 'NamespaceDeletionDiscoveryFailure', status: 'False', reason: 'ResourcesDiscovered', message: 'All resources successfully discovered' },
      ] },
    } }))
    expect(html).toContain('Terminating for')
    expect(html).toContain('>2h<')
    expect(html).toContain('Namespace finalizers')
    expect(html).toContain('kubernetes')
    expect(html).not.toContain('metadata.example.com/not-the-namespace-finalizer')
    expect(html).toContain('Conditions (3) · 2 failing')
    expect(html).toContain('SomeResourcesRemain')
    expect(html).toContain('configmaps. has 1 resource instances')
    expect(html).toContain('example.com/hold in 1 resource instances')
  })

  it('omits deletion duration, finalizers and empty conditions for an active namespace', () => {
    const html = renderToString(createElement(NamespaceRenderer, { data: {
      metadata: { creationTimestamp: '2023-01-01T00:00:00Z' },
      spec: { finalizers: ['kubernetes'] },
      status: { phase: 'Active' },
    } }))
    expect(html).toContain('Active')
    expect(html).not.toContain('Terminating for')
    expect(html).not.toContain('Conditions (')
    expect(html).not.toContain('Namespace finalizers')
  })

  it('does not substitute creation age when a terminating namespace lacks a deletion timestamp', () => {
    const html = renderToString(createElement(NamespaceRenderer, { data: {
      metadata: { creationTimestamp: '2023-01-01T00:00:00Z' },
      status: { phase: 'Terminating', conditions: [] },
    } }))
    expect(html).toContain('Terminating')
    expect(html).not.toContain('Terminating for')
    expect(html).not.toContain('Conditions (')
    expect(html).not.toContain('Namespace finalizers')
  })

  it('retains conditions with unknown status without calling them failures', () => {
    const html = renderToString(createElement(NamespaceRenderer, { data: {
      status: { phase: 'Active', conditions: [
        { type: 'NamespaceDeletionDiscoveryFailure', status: 'Unknown', reason: 'DiscoveryPending', message: 'Waiting for discovery' },
      ] },
    } }))
    expect(html).toContain('Conditions (1)')
    expect(html).toContain('Waiting for discovery')
    expect(html).not.toContain('failing')
  })
})
