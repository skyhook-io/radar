import { describe, expect, it } from 'vitest'
import { workloadHistoryNotes } from './workload-history-notes'

describe('workloadHistoryNotes', () => {
  it('says nothing when the history is complete and nothing is withheld', () => {
    expect(workloadHistoryNotes(undefined, undefined)).toEqual([])
    expect(workloadHistoryNotes({ incomplete: false, reasons: [] }, [])).toEqual([])
  })

  it('dates the start of complete history when the viewer may see the time', () => {
    const since = '2026-11-03T05:00:00Z'
    const [note] = workloadHistoryNotes({ incomplete: true, complete_since: since, reasons: ['observation_started_late'] })
    expect(note).toBe(`Changes before ${new Date(since).toLocaleString()} may be missing: Radar began recording part of this history later.`)
  })

  it('names both reasons, and no time when the server withheld it', () => {
    expect(workloadHistoryNotes({ incomplete: true, reasons: ['observation_started_late', 'events_evicted'] })).toEqual([
      'Earlier changes may be missing: Radar began recording part of this history later and older history was deleted to stay within its limits.',
    ])
    expect(workloadHistoryNotes({ incomplete: true, reasons: ['something_new'] })).toEqual(['Earlier changes may be missing.'])
  })

  it('names the Kueue kinds a viewer may not list', () => {
    expect(
      workloadHistoryNotes(null, [
        {
          code: 'user_forbidden',
          kinds: { count: 2, names: [{ kind: 'Workload', group: 'kueue.x-k8s.io' }, { kind: 'ProvisioningRequest', group: 'autoscaling.x-k8s.io' }] },
          namespaces: { count: 1, names: ['ml'] },
        },
      ]),
    ).toEqual(["Changes to Kueue Workloads and ProvisioningRequests aren't shown: you don't have access to list them in this namespace."])
  })

  it('separates what Radar does not collect from what is still loading', () => {
    expect(
      workloadHistoryNotes(null, [
        { code: 'radar_forbidden', kinds: { count: 1, names: [{ kind: 'Workload', group: 'kueue.x-k8s.io' }] } },
        { code: 'kind_sync_pending', kinds: { count: 1, names: [{ kind: 'ProvisioningRequest', group: 'autoscaling.x-k8s.io' }] } },
        { code: 'some_future_code' },
      ]),
    ).toEqual([
      "Changes to Kueue Workloads aren't shown: Radar's Kubernetes identity may not list them.",
      "Changes to ProvisioningRequests aren't shown: still loading.",
      "Some related changes aren't shown: the read failed.",
    ])
  })
})


describe('history gap remedies', () => {
  it('preserves known Radar access and collection reasons separately', () => {
    expect(workloadHistoryNotes(null, [
      { code: 'radar_credentials_rejected', action: 'radar_config' },
      { code: 'radar_namespace_not_collected', action: 'radar_config' },
      { code: 'cluster_disconnected', action: 'radar_config' },
    ])).toEqual([
      "Some related changes aren't shown: Kubernetes rejected Radar's credentials.",
      "Some related changes aren't shown: Radar doesn't collect them here.",
      "Some related changes aren't shown: Radar lost its connection.",
    ])
  })

  it.each([
    ['user_access', "you don't have access to list them in this namespace"],
    ['radar_config', "Radar's connection or collection settings need attention"],
    ['retry', 'retry the read'],
    ['wait', 'wait and retry the read'],
    ['check_target', 'check the requested target and retry'],
    ['change_request', 'change the read request'],
  ])('uses the supplied %s action for an unknown code', (action, clause) => {
    expect(workloadHistoryNotes(null, [{ code: 'new_server_code', action }])).toEqual([
      `Some related changes aren't shown: ${clause}.`,
    ])
  })
})
