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
    diff: { summary: 'Image changed to shop:v2', fields: [] },
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

  it('merges source anchors with curated runtime changes in reverse chronology', () => {
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
        diff: { summary: 'data.API_URL changed', fields: [] },
      }),
    ])

    expect(items.map((item) => item.title)).toEqual([
      'Argo CD sync',
      'Config Map updated',
      'Deployment updated',
    ])
    expect(items.map((item) => item.category)).toEqual(['deployment', 'runtime', 'runtime'])
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

  it('treats historical Job startup as normal lifecycle activity', () => {
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

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ title: 'Job started', category: 'runtime' })
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
