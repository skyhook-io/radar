// Shaped from the objects a real agent-sandbox controller (v1.0.11) wrote on a
// test cluster: nine Sandboxes in mixed states, two claims, one template and
// one warm pool short of ready. Times are relative to now.
import type { ResourceCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import type { AgentSandboxLists, KindRead } from './data'

export const NS = 'radar-agents-live'

export const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
export const hoursAgo = (h: number) => minutesAgo(h * 60)

export const COMPLETE: ResourceCoverage = { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] }
export const PARTIAL: ResourceCoverage = {
  status: 'partial',
  scope: { type: 'all_namespaces' },
  read: { type: 'namespaces', namespaces: { count: 1, names: [NS] } },
  gaps: [{ code: 'user_forbidden', namespaces: { count: 1, names: ['team-b'] } }],
}

const cond = (type: string, status: string, reason: string, message = '', at = hoursAgo(40)) => ({ type, status, reason, message, lastTransitionTime: at, observedGeneration: 1 })
const notSuspended = (at = hoursAgo(40)) => cond('Suspended', 'False', 'NotSuspended', 'Sandbox is not suspended', at)

export function sandbox(name: string, opts: { conditions?: any[]; spec?: any; annotations?: any; ownerReferences?: any[]; labels?: any; created?: string; nodeName?: string; namespace?: string } = {}): any {
  return {
    apiVersion: 'agents.x-k8s.io/v1beta1',
    kind: 'Sandbox',
    metadata: {
      name,
      namespace: opts.namespace ?? NS,
      generation: 1,
      creationTimestamp: opts.created ?? hoursAgo(40),
      ...(opts.annotations ? { annotations: opts.annotations } : {}),
      ...(opts.labels ? { labels: opts.labels } : {}),
      ...(opts.ownerReferences ? { ownerReferences: opts.ownerReferences } : {}),
    },
    spec: { failedPodPolicy: 'Retain', operatingMode: 'Running', shutdownPolicy: 'Retain', ...opts.spec },
    status: { conditions: opts.conditions ?? [], ...(opts.nodeName ? { nodeName: opts.nodeName } : {}) },
  }
}

export const POOL_UID = '26a86a25-f249-44ca-9cbc-2139d29b66ba'
const poolOwner = [{ apiVersion: 'extensions.agents.x-k8s.io/v1beta1', kind: 'SandboxWarmPool', name: 'warm', uid: POOL_UID, controller: true, blockOwnerDeletion: true }]
const UNSCHEDULABLE = '0/4 nodes are available: 4 Too many pods. no new claims to deallocate, preemption: 0/4 nodes are available: 4 No preemption victims found for incoming pod.'

export const ready = (name: string, opts: Parameters<typeof sandbox>[1] = {}) =>
  sandbox(name, { ...opts, conditions: [notSuspended(), cond('PodScheduled', 'True', 'PodScheduled'), cond('Ready', 'True', 'DependenciesReady', 'Pod is Ready; Service Exists')], nodeName: 'node-a' })

export function warmMember(name: string, sinceMinutes: number): any {
  const at = minutesAgo(sinceMinutes)
  return sandbox(name, {
    created: at,
    annotations: { 'agents.x-k8s.io/sandbox-template-ref': 'tmpl' },
    labels: { 'agents.x-k8s.io/launch-type': 'warm' },
    ownerReferences: poolOwner,
    conditions: [notSuspended(at), cond('PodScheduled', 'False', 'Unschedulable', UNSCHEDULABLE, at), cond('Ready', 'False', 'DependenciesNotReady', 'Pod exists with phase: Pending', at)],
  })
}

export function liveSandboxes(): any[] {
  return [
    sandbox('bad-image', {
      nodeName: 'node-a',
      conditions: [notSuspended(), cond('PodScheduled', 'True', 'PodScheduled'), cond('Ready', 'False', 'DependenciesNotReady', 'Pod exists with phase: Pending; container "agent" is waiting: ImagePullBackOff')],
    }),
    sandbox('expired', { spec: { shutdownTime: hoursAgo(41) }, conditions: [cond('Ready', 'False', 'SandboxExpired', 'Sandbox has expired')] }),
    sandbox('failed', {
      nodeName: 'node-a',
      conditions: [notSuspended(), cond('PodScheduled', 'True', 'PodScheduled'), cond('Ready', 'False', 'PodFailed', 'Pod failed'), cond('Finished', 'True', 'PodFailed', 'Pod failed')],
    }),
    sandbox('finished', { conditions: [notSuspended(), cond('Ready', 'False', 'PodSucceeded', 'Pod completed successfully'), cond('Finished', 'True', 'PodSucceeded', 'Pod completed successfully')] }),
    sandbox('foreign', {
      spec: { operatingMode: 'Suspended' },
      conditions: [cond('Suspended', 'False', 'PodNotOwned', 'Refused to delete pod because it is not owned by this sandbox'), cond('Ready', 'False', 'SandboxSuspended', 'Sandbox is suspending')],
    }),
    sandbox('running', { spec: { shutdownTime: hoursAgo(38) }, conditions: [notSuspended(), cond('Ready', 'False', 'SandboxExpired', 'Sandbox has expired', hoursAgo(38))] }),
    sandbox('suspended', {
      spec: { operatingMode: 'Suspended' },
      conditions: [cond('Suspended', 'True', 'PodTerminated', 'Pod has been terminated. Sandbox is suspended'), cond('Ready', 'False', 'SandboxSuspended', 'Sandbox is suspended')],
    }),
    warmMember('warm-4xmkm', 9 * 60),
    warmMember('warm-nd2zw', 4 * 60),
  ]
}

export function claim(name: string, opts: { pool?: string; reason?: string; message?: string; sandbox?: string; shutdownTime?: string; status?: string; namespace?: string } = {}): any {
  return {
    apiVersion: 'extensions.agents.x-k8s.io/v1beta1',
    kind: 'SandboxClaim',
    metadata: { name, namespace: opts.namespace ?? NS, generation: 1, creationTimestamp: hoursAgo(40) },
    spec: { warmPoolRef: { name: opts.pool ?? 'warm' }, ...(opts.shutdownTime ? { lifecycle: { shutdownPolicy: 'Retain', shutdownTime: opts.shutdownTime } } : {}) },
    status: {
      conditions: [cond('Ready', opts.status ?? 'False', opts.reason ?? 'SandboxNotReady', opts.message ?? '')],
      sandbox: opts.sandbox ? { name: opts.sandbox } : {},
    },
  }
}

export function liveClaims(): any[] {
  return [
    claim('claim-missing-pool', { pool: 'no-such-pool', reason: 'WarmPoolNotFound', message: 'SandboxWarmPool "no-such-pool" not found' }),
    claim('claim-ok', { reason: 'ClaimExpired', message: 'Claim expired. Sandbox cleanup initiated.', shutdownTime: hoursAgo(38) }),
  ]
}

export function warmPool(name = 'warm', opts: { replicas?: number; readyReplicas?: number; current?: number; template?: string; created?: string; uid?: string; namespace?: string } = {}): any {
  return {
    apiVersion: 'extensions.agents.x-k8s.io/v1beta1',
    kind: 'SandboxWarmPool',
    metadata: { name, namespace: opts.namespace ?? NS, generation: 1, uid: opts.uid ?? POOL_UID, creationTimestamp: opts.created ?? hoursAgo(40) },
    spec: { replicas: opts.replicas ?? 2, sandboxTemplateRef: { name: opts.template ?? 'tmpl' } },
    status: { observedGeneration: 1, replicas: opts.current ?? 2, ...(opts.readyReplicas !== undefined ? { readyReplicas: opts.readyReplicas } : {}) },
  }
}


export const read = (items: any[], coverage: ResourceCoverage = COMPLETE): KindRead => ({ state: 'read', items, coverage })

type Overrides = { [K in keyof AgentSandboxLists]?: KindRead | any[] }

/** The live cluster's objects as the kind lists read them, any kind replaced by a list or a read state. */
export function liveData(overrides: Overrides = {}): AgentSandboxLists {
  const pick = (k: keyof AgentSandboxLists, fallback: any[]): KindRead => {
    const v = overrides[k]
    return Array.isArray(v) ? read(v) : (v as KindRead | undefined) ?? read(fallback)
  }
  return {
    sandboxes: pick('sandboxes', liveSandboxes()),
    claims: pick('claims', liveClaims()),
    warmPools: pick('warmPools', [warmPool()]),
  }
}

export function emptyData(overrides: Overrides = {}): AgentSandboxLists {
  return liveData({ sandboxes: [], claims: [], warmPools: [], ...overrides })
}
