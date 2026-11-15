import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  formatSandboxExpiry,
  getAgentRuntimeSortValue,
  getAgentRuntimeStatus,
  getSandboxClaimStatus,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getWorkerPoolSandboxConfig,
  getWorkerPoolStatus,
} from './resource-utils-agent-runtimes'
import { getResourceStatus } from '../shared/ResourceRendererDispatch'
import { getCellFilterValue } from './resource-utils'

const ready = (status: string, reason?: string, message?: string) => ({ type: 'Ready', status, reason, message })

function sandbox(conditions: any[], spec: Record<string, unknown> = {}) {
  return { apiVersion: 'agents.x-k8s.io/v1beta1', kind: 'Sandbox', metadata: { name: 's' }, spec, status: { conditions } }
}

interface PoolFixture { generation?: number; observed?: number; desired?: number; ready?: number; pods?: number; resolved?: string; reason?: string }

function pool({ generation = 1, observed = 1, desired = 3, ready: readyCount = 3, pods, resolved = 'True', reason = 'Resolved' }: PoolFixture = {}) {
  return {
    apiVersion: 'ate.dev/v1',
    kind: 'WorkerPool',
    metadata: { name: 'p', generation },
    spec: { replicas: desired, sandboxClasses: [{ name: 'gvisor' }] },
    status: {
      replicas: pods ?? readyCount,
      readyReplicas: readyCount,
      sandboxClasses: [{
        name: 'gvisor',
        configRef: { name: 'gvisor-default' },
        conditions: [{ type: 'Resolved', status: resolved, reason, message: 'SandboxConfig "x" not found', observedGeneration: observed }],
      }],
    },
  }
}

const vectors = JSON.parse(readFileSync(new URL('../../../../../pkg/health/testdata/agent_sandbox.json', import.meta.url), 'utf8'))

describe('Agent Sandbox status (shared with pkg/health)', () => {
  for (const vector of vectors) {
    it(vector.name, () => {
      const isClaim = vector.object.kind === 'SandboxClaim'
      const expected = { text: vector.text, level: vector.uiLevel }
      expect(isClaim ? getSandboxClaimStatus(vector.object) : getSandboxStatus(vector.object)).toMatchObject(expected)
      expect(getResourceStatus(isClaim ? 'sandboxclaims' : 'sandboxes', vector.object)).toMatchObject(expected)
      expect(getAgentRuntimeStatus(vector.object)).toMatchObject(expected)
      expect(getCellFilterValue(vector.object, 'status', isClaim ? 'agentsandboxclaims' : 'agentsandboxes')).toBe(vector.text)
    })
  }
})

describe('Agent Sandbox warm pools and expiry', () => {
  it('reads warm pools from ready versus desired and observed generation', () => {
    const warm = (replicas: number | undefined, readyReplicas: number, generation = 1, observedGeneration = 1) => ({
      metadata: { generation }, spec: { replicas }, status: { readyReplicas, observedGeneration },
    })
    expect(getSandboxWarmPoolStatus(warm(3, 3))).toMatchObject({ text: 'Ready', level: 'healthy' })
    expect(getSandboxWarmPoolStatus(warm(3, 1))).toMatchObject({ text: 'Warming', level: 'degraded' })
    expect(getSandboxWarmPoolStatus(warm(3, 3, 2, 1))).toMatchObject({ text: 'Progressing' })
    expect(getSandboxWarmPoolStatus(warm(0, 0))).toMatchObject({ text: 'Scaled to zero', level: 'neutral' })
  })

  it('formats expiry relative to now', () => {
    const now = Date.parse('2026-11-15T12:00:00Z')
    expect(formatSandboxExpiry('2026-11-15T14:00:00Z', now)).toBe('in 2h')
    expect(formatSandboxExpiry('2026-11-15T09:00:00Z', now)).toBe('3h ago')
    expect(formatSandboxExpiry(undefined, now)).toBe('-')
  })
})

describe('Agent Substrate WorkerPool status', () => {
  it('reads the nested Resolved condition by ready workers', () => {
    expect(getWorkerPoolStatus(pool())).toMatchObject({ text: 'Ready', level: 'healthy' })
    expect(getWorkerPoolStatus(pool({ resolved: 'False', reason: 'SandboxConfigNotFound', ready: 0 })))
      .toMatchObject({ text: 'SandboxConfigNotFound', level: 'unhealthy' })
    expect(getWorkerPoolStatus(pool({ resolved: 'False', reason: 'SandboxConfigNotFound', ready: 2 })))
      .toMatchObject({ level: 'alert' })
    // Pods that exist but are not ready are not capacity.
    expect(getWorkerPoolStatus(pool({ resolved: 'False', reason: 'SandboxConfigNotFound', ready: 0, pods: 3 })))
      .toMatchObject({ level: 'unhealthy' })
  })

  it('does not trust a condition reported for an older generation', () => {
    expect(getWorkerPoolStatus(pool({ generation: 2, observed: 1 }))).toMatchObject({ text: 'Resolving', level: 'degraded' })
    const staleFailure = pool({ generation: 2, observed: 1, resolved: 'False', reason: 'SandboxConfigNotFound', ready: 0 })
    expect(getWorkerPoolStatus(staleFailure)).toMatchObject({ text: 'SandboxConfigNotFound', level: 'degraded' })
    expect(getAgentRuntimeStatus(staleFailure)?.message).toMatch(/^Reported for generation 1; the pool is now at generation 2\./)
  })

  it('separates requested size from observed population', () => {
    expect(getWorkerPoolStatus(pool({ desired: 0, ready: 0, pods: 0 }))).toMatchObject({ text: 'Scaled to zero', level: 'neutral' })
    expect(getWorkerPoolStatus(pool({ desired: 0, ready: 1, pods: 2 }))).toMatchObject({ text: 'Scaling down', level: 'degraded' })
    expect(getWorkerPoolStatus(pool({ desired: 3, ready: 1 }))).toMatchObject({ text: 'Scaling', level: 'degraded' })
    expect(getWorkerPoolStatus({ apiVersion: 'ate.dev/v1', spec: { replicas: 3 } })).toMatchObject({ text: 'Pending' })
  })

  it('shows a config switch the controller is still confirming', () => {
    const switching = pool()
    ;(switching.status.sandboxClasses[0] as any).pendingConfigRef = { name: 'gvisor-v2' }
    expect(getWorkerPoolSandboxConfig(switching)).toBe('gvisor-default → gvisor-v2')
  })
})

describe('agent runtime kinds claim only their own API group', () => {
  it('header status, banner status and table filter agree for the curated groups', () => {
    const suspended = sandbox([ready('False', 'SandboxSuspended'), { type: 'Suspended', status: 'True' }])
    expect(getResourceStatus('sandboxes', suspended)).toMatchObject({ text: 'Suspended' })
    expect(getAgentRuntimeStatus(suspended)).toMatchObject({ text: 'Suspended', level: 'neutral' })
    expect(getCellFilterValue(suspended, 'status', 'agentsandboxes')).toBe('Suspended')
    expect(getResourceStatus('workerpools', pool())).toMatchObject({ text: 'Ready' })
  })

  it('leaves another project\'s same-named kinds on the generic path', () => {
    const foreign = { apiVersion: 'example.io/v1', kind: 'Sandbox', status: { conditions: [ready('False', 'SandboxSuspended')] } }
    expect(getAgentRuntimeStatus(foreign)).toBeNull()
    expect(getResourceStatus('sandboxes', foreign)).toMatchObject({ text: 'SandboxSuspended' })
    expect(getAgentRuntimeStatus({ apiVersion: 'cloudbuild.cnrm.cloud.google.com/v1beta1', kind: 'WorkerPool', status: {} })).toBeNull()
    // kagent's SandboxTemplate shares the kind with Agent Sandbox's and is not curated.
    expect(getAgentRuntimeStatus({ apiVersion: 'api.kagent.dev/v1alpha3', kind: 'SandboxTemplate' })).toBeNull()
  })
})

describe('agent runtime column filter and sort values', () => {
  it('match the cells and sort counts and expiry as numbers', () => {
    const p = pool({ ready: 2 })
    expect(getCellFilterValue(p, 'workers', 'substrateworkerpools')).toBe('2/3')
    expect(getCellFilterValue(p, 'sandboxConfig', 'substrateworkerpools')).toBe('gvisor-default')
    expect(getAgentRuntimeSortValue(p, 'workers')).toBe(2)
    const s = sandbox([], { shutdownTime: '2026-11-15T14:00:00Z' })
    expect(getAgentRuntimeSortValue(s, 'expires')).toBe(Date.parse('2026-11-15T14:00:00Z'))
    expect(getAgentRuntimeSortValue(sandbox([]), 'expires')).toBe(Number.MAX_SAFE_INTEGER)
    expect(getAgentRuntimeSortValue({ apiVersion: 'example.io/v1', kind: 'Sandbox' }, 'expires')).toBeUndefined()
  })
})

