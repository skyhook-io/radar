import { describe, expect, it } from 'vitest'
import { UNKNOWN_SOURCE_LINE, uncheckedSourceLines } from './uncheckedSources'

describe('uncheckedSourceLines', () => {
  it('names the kinds still loading', () => {
    expect(uncheckedSourceLines([{
      source: 'resources', reason: 'kind_sync_pending',
      kinds: [{ group: 'autoscaling', kind: 'HorizontalPodAutoscaler' }, { kind: 'PersistentVolumeClaim' }],
    }])).toEqual(['Still loading HorizontalPodAutoscalers and PersistentVolumeClaims, so their issues may be missing.'])
  })

  it('caps a long kind list', () => {
    const kinds = ['Pod', 'Node', 'Service', 'Secret', 'ConfigMap'].map((kind) => ({ kind }))
    expect(uncheckedSourceLines([{ source: 'resources', reason: 'kind_sync_failed', kinds }]))
      .toEqual(["Pods, Nodes and 3 more couldn't be loaded, so their issues may be missing."])
  })

  it('says custom resources are loading when no kind can be named', () => {
    expect(uncheckedSourceLines([{ source: 'custom_resources', reason: 'kind_sync_pending' }]))
      .toEqual(['Custom resources are still loading, so failing ones may be missing.'])
  })

  it('names the Helm namespaces that failed, so the rest reads as checked', () => {
    expect(uncheckedSourceLines([{ source: 'helm', reason: 'read_failed', namespaces: ['payments'] }]))
      .toEqual(["Helm releases in payments couldn't be checked, so failed or stuck releases there may be missing."])
    expect(uncheckedSourceLines([{ source: 'helm', reason: 'read_failed', namespaces: ['a', 'b'] }]))
      .toEqual(["Helm releases in 2 namespaces (a and b) couldn't be checked, so failed or stuck releases there may be missing."])
  })

  it("says when Radar's own service account is the problem", () => {
    expect(uncheckedSourceLines([{ source: 'helm', reason: 'collector_access_denied' }]))
      .toEqual(["Helm releases couldn't be checked because Radar's service account isn't allowed to impersonate users, so failed or stuck releases may be missing."])
  })

  it('falls back to the source line for a reason this version does not know', () => {
    expect(uncheckedSourceLines([{ source: 'karpenter', reason: 'something_new' }]))
      .toEqual(["Karpenter resources couldn't be checked, so NodePool and NodeClaim issues may be missing."])
  })

  it('keeps the generic line for an unknown source', () => {
    expect(uncheckedSourceLines([{ source: 'brand_new', reason: 'read_failed' }])).toEqual([UNKNOWN_SOURCE_LINE])
  })
})
