import { describe, expect, it } from 'vitest'
import { getServiceMonitorStatus, getPrometheusRuleStatus, getPodMonitorStatus } from './resource-utils-prometheus'
import { getKyvernoPolicyStatus } from './resource-utils-kyverno'
import { getServiceEntryStatus, getVirtualServiceStatus } from './resource-utils-istio'
import { getCNPGObjectStoreStatus } from './resource-utils-cnpg'

// A green badge asserts something a controller checked. These pin the line
// between what an object declares and what something actually confirmed.

describe('Prometheus configuration resources', () => {
  // Status lives at status.bindings[].conditions, only condition type
  // "Accepted" exists, and the operator writes it only under the
  // StatusForConfigurationResources feature gate.
  const accepted = (status: string, extra: Record<string, unknown> = {}) => ({
    metadata: { generation: 3 },
    status: { bindings: [{ conditions: [{ type: 'Accepted', status, observedGeneration: 3, ...extra }] }] },
  })

  for (const [name, fn] of [
    ['ServiceMonitor', getServiceMonitorStatus],
    ['PrometheusRule', getPrometheusRuleStatus],
    ['PodMonitor', getPodMonitorStatus],
  ] as const) {
    it(`${name} does not report health when the gate wrote nothing`, () => {
      // The overwhelmingly common shape: no status at all.
      expect(fn({ spec: {} })).toMatchObject({ text: 'Not assessed', level: 'unknown' })
    })

    it(`${name} ignores a status.conditions that these kinds never have`, () => {
      // This path is not part of these resources' schema.
      expect(fn({ spec: {}, status: { conditions: [{ type: 'Reconciled', status: 'True' }] } }))
        .toMatchObject({ text: 'Not assessed', level: 'unknown' })
    })

    it(`${name} surfaces an explicit rejection`, () => {
      expect(fn(accepted('False', { reason: 'InvalidConfig' })))
        .toMatchObject({ text: 'InvalidConfig', level: 'unhealthy' })
    })

    it(`${name} reports acceptance when a workload confirmed it`, () => {
      expect(fn(accepted('True'))).toMatchObject({ text: 'Accepted', level: 'healthy' })
    })
  }

  it('does not read a condition observed against an older spec', () => {
    const stale = {
      metadata: { generation: 7 },
      status: { bindings: [{ conditions: [{ type: 'Accepted', status: 'True', observedGeneration: 4 }] }] },
    }
    expect(getServiceMonitorStatus(stale)).toMatchObject({ text: 'Not assessed', level: 'unknown' })
  })

  it('does not read one workload as all of them when another has not decided', () => {
    const undecided = {
      metadata: { generation: 1 },
      status: {
        bindings: [
          { name: 'a', conditions: [{ type: 'Accepted', status: 'True', observedGeneration: 1 }] },
          { name: 'b', conditions: [{ type: 'Accepted', status: 'Unknown', observedGeneration: 1 }] },
        ],
      },
    }
    expect(getServiceMonitorStatus(undecided)).toMatchObject({ text: 'Accepted by 1/2', level: 'unknown' })
  })

  it('reports partial acceptance when workloads disagree', () => {
    // One config resource can be selected by several Prometheus workloads, and
    // each reports separately.
    const mixed = {
      metadata: { generation: 1 },
      status: {
        bindings: [
          { name: 'a', conditions: [{ type: 'Accepted', status: 'True', observedGeneration: 1 }] },
          { name: 'b', conditions: [{ type: 'Accepted', status: 'False', observedGeneration: 1 }] },
        ],
      },
    }
    expect(getServiceMonitorStatus(mixed)).toMatchObject({ text: 'Accepted by 1/2', level: 'degraded' })
  })
})

describe('getKyvernoPolicyStatus', () => {
  it('reads the Ready condition the controller writes', () => {
    expect(getKyvernoPolicyStatus({ status: { conditions: [{ type: 'Ready', status: 'True' }] } }))
      .toMatchObject({ text: 'Ready', level: 'healthy' })
    expect(getKyvernoPolicyStatus({ status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Failed' }] } }))
      .toMatchObject({ text: 'Failed', level: 'unhealthy' })
  })

  it('does not read declared rules as the controller accepting them', () => {
    // Fires before the controller reports, and for Ready=Unknown.
    expect(getKyvernoPolicyStatus({ spec: { rules: [{}, {}] } }))
      .toMatchObject({ text: 'Not assessed', level: 'unknown' })
    expect(getKyvernoPolicyStatus({ spec: { rules: [{}] }, status: { conditions: [{ type: 'Ready', status: 'Unknown' }] } }))
      .toMatchObject({ text: 'Not assessed', level: 'unknown' })
  })
})

describe('getServiceEntryStatus', () => {
  it('treats location as a classification, not reachability', () => {
    // MESH_EXTERNAL/MESH_INTERNAL say where the host sits relative to the mesh.
    // Nothing here establishes it resolves or answers.
    expect(getServiceEntryStatus({ spec: { hosts: ['api.example.com'] } }))
      .toMatchObject({ text: 'External', level: 'neutral' })
    expect(getServiceEntryStatus({ spec: { hosts: ['a'], location: 'MESH_INTERNAL' } }))
      .toMatchObject({ text: 'Internal', level: 'neutral' })
  })

  it('still flags an entry that selects nothing', () => {
    expect(getServiceEntryStatus({ spec: {} })).toMatchObject({ text: 'No Hosts', level: 'unhealthy' })
  })
})

describe('getVirtualServiceStatus', () => {
  it('does not call a hostless VirtualService broken', () => {
    // Istio requires hosts to be empty on a delegate VirtualService, and this
    // object cannot see whether a parent delegates to it.
    expect(getVirtualServiceStatus({ spec: { http: [{ route: [{}] }] } }))
      .toMatchObject({ text: 'No Hosts', level: 'unknown' })
  })

  it('does not read a declaration as working routing', () => {
    expect(getVirtualServiceStatus({ spec: { hosts: ['a'], http: [{ route: [{}] }] } }))
      .toMatchObject({ text: 'Defined', level: 'neutral' })
  })

  it('still flags hosts with nowhere to send them', () => {
    expect(getVirtualServiceStatus({ spec: { hosts: ['a'] } }))
      .toMatchObject({ text: 'No Routes', level: 'unhealthy' })
  })

  it('flags a routeless object even when it declares no hosts', () => {
    // A delegate omits hosts, but it exists to supply routes.
    expect(getVirtualServiceStatus({ spec: {} }))
      .toMatchObject({ text: 'No Routes', level: 'unhealthy' })
  })
})

describe('getCNPGObjectStoreStatus', () => {
  it('does not call a listed server recoverable with no recovery point', () => {
    // Every timestamp on a RecoveryWindow is optional, so the server can be
    // listed while holding nothing restorable.
    expect(getCNPGObjectStoreStatus({ status: { serverRecoveryWindow: { pg: {} } } }))
      .toMatchObject({ text: 'No recovery point', level: 'unknown' })
  })

  it('reports recoverable once a real point exists', () => {
    expect(getCNPGObjectStoreStatus({
      status: { serverRecoveryWindow: { pg: { firstRecoverabilityPoint: '2026-01-01T00:00:00Z' } } },
    })).toMatchObject({ text: 'Recoverable', level: 'healthy' })
  })

  it('still reports failing backups ahead of everything else', () => {
    expect(getCNPGObjectStoreStatus({
      status: { serverRecoveryWindow: { pg: { lastFailedBackupTime: '2026-02-01T00:00:00Z' } } },
    })).toMatchObject({ text: 'Backups Failing', level: 'unhealthy' })
  })
})
