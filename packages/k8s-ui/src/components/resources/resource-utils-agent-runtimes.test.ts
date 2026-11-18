import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  formatSandboxExpiry,
  getAgentRuntimeSortValue,
  getAgentRuntimeStatus,
  getSandboxClaimStatus,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getWorkerPoolSandboxConfig,
  getWorkerPoolStatus,
  imagePullNextStep,
  REGISTRY_DNS_NEXT_STEP,
  sandboxPodImagePullNextStep,
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

const pullVectors = JSON.parse(readFileSync(new URL('../../../../../pkg/agentsandbox/testdata/image_pull_next_steps.json', import.meta.url), 'utf8'))

describe('Agent Sandbox image pull next step (shared with pkg/agentsandbox)', () => {
  it('pins the same step as Go', () => {
    expect(REGISTRY_DNS_NEXT_STEP).toBe(pullVectors.registryDNSNextStep)
    expect(REGISTRY_DNS_NEXT_STEP.split(/\s+/).length).toBeLessThanOrEqual(25)
  })
  for (const c of pullVectors.cases) {
    it(c.name, () => {
      expect(imagePullNextStep(c.reason, c.message)).toBe(c.dns ? REGISTRY_DNS_NEXT_STEP : undefined)
    })
  }

  const [dns, , , auth] = pullVectors.cases
  const pod = (...waiting: any[]) => ({ status: { containerStatuses: waiting.map((w, i) => ({ name: `c${i}`, state: w ? { waiting: w } : { running: {} } })) } })
  it('reads the step from the Pod only where every container that can’t pull agrees on it', () => {
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', pod({ reason: dns.reason, message: dns.message }))).toBe(REGISTRY_DNS_NEXT_STEP)
    // A running sidecar doesn't count against it; an init container's pull does.
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', pod({ reason: dns.reason, message: dns.message }, undefined))).toBe(REGISTRY_DNS_NEXT_STEP)
    expect(sandboxPodImagePullNextStep('ErrImagePull', { status: { initContainerStatuses: [{ name: 'i', state: { waiting: { reason: 'ErrImagePull', message: dns.message } } }] } })).toBe(REGISTRY_DNS_NEXT_STEP)
    // Two failures, one of them not DNS: no single cause.
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', pod({ reason: dns.reason, message: dns.message }, { reason: auth.reason, message: auth.message }))).toBeUndefined()
    // No message, no pull failure, or no Pod read: the server's step stands.
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', pod({ reason: 'ImagePullBackOff' }))).toBeUndefined()
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', pod({ reason: 'CrashLoopBackOff', message: dns.message }))).toBeUndefined()
    expect(sandboxPodImagePullNextStep('ImagePullBackOff', undefined)).toBeUndefined()
    // Only where the reading is the pull failure: a crash reading keeps its own step.
    expect(sandboxPodImagePullNextStep('CrashLoopBackOff', pod({ reason: dns.reason, message: dns.message }))).toBeUndefined()
  })
})

describe('Agent Sandbox refused suspension', () => {
  it('explains the refusal from the Suspended condition, not Ready', () => {
    const sandbox = {
      apiVersion: 'agents.x-k8s.io/v1beta1', kind: 'Sandbox', metadata: { name: 'a', generation: 1 },
      status: { conditions: [
        { ...ready('False', 'SandboxSuspended', 'Sandbox is suspending'), observedGeneration: 1 },
        { type: 'Suspended', status: 'False', reason: 'PodNotOwned', message: 'Refused to delete pod because it is not owned by this sandbox' },
      ] },
    }
    expect(getAgentRuntimeStatus(sandbox)).toMatchObject({
      text: 'Suspension blocked',
      level: 'alert',
      message: 'Refused to delete pod because it is not owned by this sandbox. The conflicting Pod is "a". Check who owns it (kubectl get pod a -o jsonpath=\'{.metadata.ownerReferences}\') before removing it, or recreate the Sandbox under another name.',
    })
  })

  const refused = (extra: Record<string, unknown>) => ({
    apiVersion: 'agents.x-k8s.io/v1beta1', kind: 'Sandbox', metadata: { name: 'a', generation: 1, ...extra },
    status: { conditions: [
      { ...ready('False', 'SandboxSuspended', 'Sandbox is suspending'), observedGeneration: 1 },
      { type: 'Suspended', status: 'False', reason: 'PodNotOwned', message: 'Refused to delete pod because it is not owned by this sandbox' },
    ] },
  })

  it('names the Pod the controller looks for, which an annotation can record', () => {
    expect(getAgentRuntimeStatus(refused({ annotations: { 'agents.x-k8s.io/pod-name': 'a-pod-7f2' } }))?.message).toContain('The conflicting Pod is "a-pod-7f2".')
  })

  it('has the owner checked in the Sandbox’s namespace before the Pod is removed, as pkg/health says it', () => {
    const message = getAgentRuntimeStatus(refused({ namespace: 'agents', annotations: { 'agents.x-k8s.io/pod-name': 'a-pod-7f2' } }))?.message
    expect(message).toContain('Check who owns it (kubectl get pod a-pod-7f2 -n agents -o jsonpath=\'{.metadata.ownerReferences}\') before removing it')
    expect(message).not.toMatch(/\bRemove it\b/)
  })

  it('gives no unblocking advice once an edit outranks the refusal', () => {
    const status = getAgentRuntimeStatus(refused({ generation: 2 }))
    expect(status?.text).not.toBe('Suspension blocked')
    expect(status?.message).not.toContain('The conflicting Pod')
  })
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
    const draining = { metadata: { generation: 2 }, spec: { replicas: 0 }, status: { replicas: 3, readyReplicas: 3, observedGeneration: 2 } }
    expect(getSandboxWarmPoolStatus(draining)).toMatchObject({ text: 'Scaling down', level: 'degraded' })
  })

  it('formats expiry relative to now', () => {
    const now = Date.parse('2026-11-15T12:00:00Z')
    expect(formatSandboxExpiry('2026-11-15T14:00:00Z', now)).toBe('in 2h')
    expect(formatSandboxExpiry('2026-11-15T09:00:00Z', now)).toBe('3h ago')
    expect(formatSandboxExpiry(undefined, now)).toBe('-')
  })
})

const poolVectors = JSON.parse(readFileSync(new URL('../../../../../pkg/health/testdata/substrate_workerpool.json', import.meta.url), 'utf8'))

describe('Agent Substrate WorkerPool status (shared with pkg/health)', () => {
  afterEach(() => { vi.useRealTimers() })
  for (const vector of poolVectors) {
    it(vector.name, () => {
      // Same default as pkg/health's test: inside the provisioning grace period.
      vi.useFakeTimers({ now: Date.parse(vector.now ?? '2026-11-14T10:05:00Z'), toFake: ['Date'] })
      const expected = { text: vector.text, level: vector.uiLevel }
      expect(getWorkerPoolStatus(vector.object)).toMatchObject(expected)
      expect(getResourceStatus('workerpools', vector.object)).toMatchObject(expected)
      expect(getAgentRuntimeStatus(vector.object)).toMatchObject(expected)
      expect(getCellFilterValue(vector.object, 'status', 'substrateworkerpools')).toBe(vector.text)
    })
  }
})

describe('Agent Substrate WorkerPool status', () => {
  it('explains a reading no condition explains, instead of repeating it', () => {
    const pending = { apiVersion: 'ate.dev/v1', kind: 'WorkerPool', metadata: { name: 'p' }, spec: { replicas: 5 } }
    expect(getAgentRuntimeStatus(pending)).toMatchObject({ text: 'Pending', message: "Substrate's controller hasn't reported on this pool yet." })
    expect(getAgentRuntimeStatus(pool({ ready: 1 }))).toMatchObject({ text: 'Scaling', message: '1 of 3 workers ready.' })
    expect(getAgentRuntimeStatus(pool({ desired: 0, ready: 0, pods: 2 }))).toMatchObject({ text: 'Scaling down', message: 'Scaled to zero; 2 worker Pods are still running.' })
    expect(getAgentRuntimeStatus(pool({ observed: 1, generation: 2 }))).toMatchObject({ text: 'Resolving' })
    expect(getAgentRuntimeStatus(pool())?.message).toBeUndefined()
  })

  it('says a failure was reported for an older generation', () => {
    const staleFailure = pool({ generation: 2, observed: 1, resolved: 'False', reason: 'SandboxConfigNotFound', ready: 0 })
    expect(getAgentRuntimeStatus(staleFailure)?.message).toMatch(/^Reported for generation 1; the pool is now at generation 2\./)
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

