import { describe, expect, it } from 'vitest'
import type { TimelineEvent } from '../types/core'
import type { AppHistory, AppRow } from './applications'
import { buildAppMembershipIndex } from './applications'
import { buildApplicationHistoryItems } from './application-history'

const app: AppRow = {
  key: 'Application/shop',
  name: 'shop',
  namespace: 'prod',
  health: 'healthy',
  sourceRef: {
    type: 'gitops',
    tool: 'argocd',
    kind: 'Application',
    namespace: 'argocd',
    name: 'shop',
  },
  workloads: [{
    kind: 'Deployment',
    namespace: 'prod',
    name: 'shop-api',
    health: 'healthy',
    ready: 2,
    desired: 2,
    restarts: 0,
  }],
  relationships: {
    serviceRefs: [{ kind: 'Service', namespace: 'prod', name: 'shop-api' }],
    configRefs: [{ kind: 'ConfigMap', namespace: 'prod', name: 'shop-config' }],
    storageRefs: [{ kind: 'PersistentVolumeClaim', namespace: 'prod', name: 'shop-data' }],
  },
  matchKeys: ['instance:prod:shop', 'name-stem:shop'],
}

function event(overrides: Partial<TimelineEvent> = {}): TimelineEvent {
  return {
    id: 'deployment-update',
    timestamp: '2026-07-13T09:30:00.000Z',
    source: 'informer',
    kind: 'Deployment',
    namespace: 'prod',
    name: 'shop-api',
    eventType: 'update',
    diff: {
      summary: 'Image changed to shop:v2',
      fields: [{ path: 'spec.template.spec.containers[0].image', oldValue: 'shop:v1', newValue: 'shop:v2' }],
    },
    ...overrides,
  }
}

describe('application history membership', () => {
  it('indexes workloads, concrete relationships, and exact evidence for one app', () => {
    const index = buildAppMembershipIndex([app])

    expect([...index.byResource.keys()].sort()).toEqual([
      'Application/argocd/shop',
      'ConfigMap/prod/shop-config',
      'Deployment/prod/shop-api',
      'PersistentVolumeClaim/prod/shop-data',
      'Service/prod/shop-api',
    ])
    expect(index.byEvidence.has('instance:prod:shop')).toBe(true)
    expect(index.byEvidence.has('name-stem:shop')).toBe(false)
  })
})

describe('buildApplicationHistoryItems', () => {
  const history: AppHistory = {
    appKey: app.key,
    sourceRef: {
      type: 'gitops',
      tool: 'argocd',
      kind: 'Application',
      namespace: 'argocd',
      name: 'shop',
    },
    anchors: [{
      type: 'gitops',
      title: 'Argo CD sync',
      revision: 'abc123',
      status: 'Succeeded',
      timestamp: '2026-07-13T10:00:00.000Z',
    }],
  }

  it('merges source anchors with curated desired-state changes in reverse chronology', () => {
    const items = buildApplicationHistoryItems(history, [
      event(),
      event({
        id: 'pod-ready',
        kind: 'Pod',
        name: 'shop-api-abc12',
        source: 'k8s_event',
        eventType: 'Normal',
        reason: 'Ready',
        diff: undefined,
      }),
      event({
        id: 'config-update',
        kind: 'ConfigMap',
        name: 'shop-config',
        timestamp: '2026-07-13T09:45:00.000Z',
        diff: {
          summary: 'data.API_URL changed',
          fields: [{ path: 'data.API_URL', oldValue: 'https://old.example', newValue: 'https://new.example' }],
        },
      }),
    ])

    expect(items.map((item) => item.title)).toEqual([
      'Argo CD sync',
      'Config Map updated',
      'Deployment updated',
    ])
    expect(items.map((item) => item.category)).toEqual(['deployment', 'change', 'change'])
  })

  it('coalesces source metadata and operation status for the same deployment change', () => {
    const items = buildApplicationHistoryItems({
      ...history,
      anchors: [
        {
          type: 'gitops',
          title: 'Argo CD sync',
          revision: 'abc123',
          source: 'https://github.com/example/shop · deploy/prod',
          initiatedBy: 'automated',
          timestamp: '2026-07-13T10:00:00.000Z',
        },
        {
          type: 'gitops',
          title: 'Argo CD sync',
          revision: 'abc123',
          status: 'Succeeded',
          message: 'successfully synced (all tasks run)',
          initiatedBy: 'automated',
          timestamp: '2026-07-13T10:00:00.000Z',
        },
      ],
    }, [])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      title: 'Argo CD sync',
      revision: 'abc123',
      status: 'Succeeded',
      detail: 'https://github.com/example/shop · deploy/prod',
    })
  })

  it('omits normal batch run lifecycle activity', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'job-started',
        kind: 'Job',
        name: 'shop-migrate',
        source: 'historical',
        eventType: 'update',
        reason: 'started',
        healthState: 'degraded',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(0)
  })

  it('omits status-only updates while keeping spec and configuration changes', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'status-only',
        diff: {
          summary: 'readyReplicas changed from 1 to 2',
          fields: [{ path: 'status.readyReplicas', oldValue: 1, newValue: 2 }],
        },
      }),
      event({
        id: 'job-suspended',
        kind: 'Job',
        name: 'shop-migrate',
        diff: {
          summary: 'suspend changed from false to true',
          fields: [{ path: 'spec.suspend', oldValue: false, newValue: true }],
        },
      }),
      event({
        id: 'config-updated',
        kind: 'ConfigMap',
        name: 'shop-config',
        diff: {
          summary: 'Modified keys: FEATURE_FLAG',
          fields: [{ path: 'data (modified keys)', oldValue: 'off', newValue: 'on' }],
        },
      }),
      event({
        id: 'config-key-added',
        kind: 'ConfigMap',
        name: 'shop-config',
        diff: {
          summary: 'Added keys: API_URL',
          fields: [{ path: 'data (added keys)', newValue: 'API_URL' }],
        },
      }),
      event({
        id: 'secret-data',
        kind: 'Secret',
        name: 'shop-secret',
        diff: {
          summary: 'data modified keys: [TOKEN]',
          fields: [{ path: 'data (modified keys)', oldValue: ['TOKEN'], newValue: ['TOKEN'] }],
        },
      }),
      event({
        id: 'sealed-secret-data',
        kind: 'SealedSecret',
        name: 'shop-sealed-secret',
        diff: {
          summary: 'spec.encryptedData modified keys: [TOKEN]',
          fields: [{ path: 'spec.encryptedData (modified keys)', oldValue: ['TOKEN'], newValue: ['TOKEN'] }],
        },
      }),
      event({
        id: 'config-immutable',
        kind: 'ConfigMap',
        name: 'shop-config',
        diff: {
          summary: 'immutable changed',
          fields: [{ path: 'immutable', oldValue: false, newValue: true }],
        },
      }),
      event({
        id: 'config-database-status',
        kind: 'ConfigMap',
        name: 'shop-config',
        diff: {
          summary: 'database status changed',
          fields: [{ path: 'database.status', oldValue: 'old', newValue: 'new' }],
        },
      }),
    ])

    expect(items.map((item) => item.id).sort()).toEqual([
      'event:config-immutable',
      'event:config-key-added',
      'event:config-updated',
      'event:job-suspended',
      'event:sealed-secret-data',
      'event:secret-data',
    ])
    expect(items.every((item) => item.category === 'change')).toBe(true)
  })

  it('omits owned batch run creation but keeps standalone Job creation', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'cron-run-created',
        kind: 'Job',
        name: 'shop-report-1234',
        eventType: 'add',
        diff: undefined,
        owner: { kind: 'CronJob', name: 'shop-report' },
      }),
      event({
        id: 'standalone-job-created',
        kind: 'Job',
        name: 'shop-migrate',
        eventType: 'add',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ id: 'event:standalone-job-created', title: 'Job created', category: 'change' })
  })

  it('keeps failed batch runs as problems', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'job-failed',
        kind: 'Job',
        name: 'shop-migrate',
        source: 'k8s_event',
        eventType: 'Warning',
        reason: 'BackoffLimitExceeded',
        message: 'Job has reached the specified backoff limit',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ title: 'Job failed after repeated retries', category: 'problem' })
  })

  it('does not suppress a failed batch signal when its event type is not Warning', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'job-failed-normal-type',
        kind: 'Job',
        name: 'shop-migrate',
        source: 'historical',
        eventType: 'Normal',
        reason: 'Failed',
        message: 'Job failed',
        healthState: 'unhealthy',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ title: 'Job failed', category: 'problem' })
  })

  it('normalizes lowercase batch failure reasons into developer-facing copy', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'job-failed-lowercase',
        kind: 'Job',
        name: 'shop-migrate',
        source: 'k8s_event',
        eventType: 'Warning',
        reason: 'failed',
        message: 'Job has reached the specified backoff limit',
        diff: undefined,
      }),
    ])

    expect(items[0]).toMatchObject({ title: 'Job failed', category: 'problem' })
  })

  it.each([
    ['CronJob', 'Scheduled run could not be created'],
    ['Deployment', 'Pod could not be created'],
  ])('translates FailedCreate on %s into developer-facing copy', (kind, title) => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: `failed-create-${kind}`,
        kind,
        source: 'k8s_event',
        eventType: 'Warning',
        reason: 'FailedCreate',
        message: 'admission webhook denied the request',
        diff: undefined,
      }),
    ])

    expect(items[0]).toMatchObject({ title, category: 'problem' })
  })

  it('rolls repeated pod warnings up to the owning workload without summing cumulative counts', () => {
    const first = event({
      id: 'warning-1',
      source: 'k8s_event',
      eventType: 'Warning',
      reason: 'FailedScheduling',
      message: 'No nodes available',
      count: 3,
      timestamp: '2026-07-13T09:00:00.000Z',
      diff: undefined,
      owner: { kind: 'DaemonSet', name: 'node-agent' },
    })
    const latest = {
      ...first,
      id: 'warning-2',
      name: 'shop-2',
      count: 4,
      timestamp: '2026-07-13T09:10:00.000Z',
    }

    const items = buildApplicationHistoryItems(undefined, [first, latest])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      category: 'problem',
      title: "Can't be scheduled",
      count: 4,
      timestamp: '2026-07-13T09:10:00.000Z',
      resource: { kind: 'DaemonSet', namespace: 'prod', name: 'node-agent' },
    })
  })

  it('suppresses informer churn for an anchored source object but keeps its warnings', () => {
    const sourceUpdate = event({
      id: 'argo-update',
      kind: 'Application',
      namespace: 'argocd',
      name: 'shop',
    })
    const sourceWarning = event({
      id: 'argo-warning',
      kind: 'Application',
      namespace: 'argocd',
      name: 'shop',
      source: 'k8s_event',
      eventType: 'Warning',
      reason: 'SyncError',
      diff: undefined,
    })

    const items = buildApplicationHistoryItems(history, [sourceUpdate, sourceWarning])

    expect(items.map((item) => item.title)).toEqual(['Argo CD sync', 'SyncError'])
  })

  it('suppresses non-problem source-object changes even when source history is unavailable', () => {
    const items = buildApplicationHistoryItems({ ...history, anchors: [] }, [
      event({
        id: 'argo-update',
        kind: 'Application',
        namespace: 'argocd',
        name: 'shop',
      }),
    ])

    expect(items).toHaveLength(0)
  })

  it('coalesces a deployment change burst around a source anchor but keeps later drift', () => {
    const items = buildApplicationHistoryItems(history, [
      event({ id: 'sync-fanout', timestamp: '2026-07-13T10:01:00.000Z' }),
      event({ id: 'sync-boundary', timestamp: '2026-07-13T10:02:00.000Z' }),
      event({ id: 'manual-drift', timestamp: '2026-07-13T10:05:00.000Z' }),
    ])

    expect(items.map((item) => item.id)).toEqual([
      'event:manual-drift',
      'source:gitops:2026-07-13T10:00:00.000Z:abc123',
    ])
  })

  it('keeps configuration data changes near a deployment anchor', () => {
    const items = buildApplicationHistoryItems(history, [
      event({
        id: 'nearby-config-edit',
        kind: 'ConfigMap',
        name: 'shop-config',
        timestamp: '2026-07-13T10:01:00.000Z',
        diff: {
          summary: 'data.FEATURE_FLAG changed',
          fields: [{ path: 'data.FEATURE_FLAG', oldValue: 'off', newValue: 'on' }],
        },
      }),
    ])

    expect(items.map((item) => item.id)).toEqual([
      'event:nearby-config-edit',
      'source:gitops:2026-07-13T10:00:00.000Z:abc123',
    ])
  })

  it('keeps failed pod signals while omitting ordinary pod and ReplicaSet churn', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({ id: 'pod-add', kind: 'Pod', name: 'shop-api-abc12', eventType: 'add', diff: undefined }),
      event({ id: 'rs-update', kind: 'ReplicaSet', name: 'shop-api-abc', diff: { summary: 'replicas changed', fields: [] } }),
      event({
        id: 'pod-warning',
        kind: 'Pod',
        name: 'shop-api-abc12',
        source: 'k8s_event',
        eventType: 'Warning',
        reason: 'BackOff',
        message: 'Back-off restarting container',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ title: 'BackOff', category: 'problem' })
  })

  it('keeps explained degraded pod failures but drops transient degraded startup snapshots', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'pod-started',
        kind: 'Pod',
        name: 'shop-api-abc12',
        reason: 'started',
        healthState: 'degraded',
        diff: undefined,
      }),
      event({
        id: 'pod-unschedulable',
        kind: 'Pod',
        name: 'shop-api-def34',
        reason: 'PodScheduled',
        healthState: 'degraded',
        message: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
        diff: undefined,
        owner: { kind: 'DaemonSet', name: 'node-agent' },
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      title: "Can't be scheduled",
      category: 'problem',
      resource: { kind: 'DaemonSet', namespace: 'prod', name: 'node-agent' },
    })
  })

  it('keeps unexplained degraded workload snapshots as problems', () => {
    const items = buildApplicationHistoryItems(undefined, [
      event({
        id: 'deployment-degraded',
        healthState: 'degraded',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      title: 'Deployment needs attention',
      category: 'problem',
      resource: { kind: 'Deployment', namespace: 'prod', name: 'shop-api' },
    })
  })

  it('keeps a degraded source-object snapshot when no source anchor explains it', () => {
    const historyWithoutAnchors: AppHistory = {
      ...history,
      anchors: [],
    }
    const items = buildApplicationHistoryItems(historyWithoutAnchors, [
      event({
        id: 'argo-degraded',
        kind: 'Application',
        namespace: 'argocd',
        name: 'shop',
        healthState: 'degraded',
        diff: undefined,
      }),
    ])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ title: 'Application needs attention', category: 'problem' })
  })
})
