import { describe, expect, it } from 'vitest'
import { getSandboxClaimStatus, getSandboxStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import { RESERVE_BELOW_TARGET, readingLabel, serverReadings, unschedulablePools, warmPoolBadge } from './labels'
import { claim, liveSandboxes, warmPool } from './agentsandbox.fixtures'
import { liveAttention, liveWorkspace } from './workspace.fixtures'

describe('Agent Sandbox reading labels', () => {
  it('put the controller’s codes in plain words and keep the code', () => {
    const by = Object.fromEntries(liveSandboxes().map((s) => [s.metadata.name, readingLabel(getSandboxStatus(s))]))
    expect(by['bad-image']).toEqual({ label: 'Image can’t be pulled', reason: 'ImagePullBackOff' })
    expect(by.failed).toEqual({ label: 'Pod failed', reason: 'PodFailed' })
    expect(by['warm-4xmkm']).toEqual({ label: 'Can’t schedule', reason: 'Unschedulable' })
    expect(readingLabel(getSandboxClaimStatus(claim('c', { reason: 'WarmPoolNotFound' })))).toEqual({ label: 'Warm pool not found', reason: 'WarmPoolNotFound' })
  })

  it('leave readings that are already words alone', () => {
    const by = Object.fromEntries(liveSandboxes().map((s) => [s.metadata.name, readingLabel(getSandboxStatus(s))]))
    expect(by.expired).toEqual({ label: 'Expired' })
    expect(by.foreign).toEqual({ label: 'Suspension blocked' })
  })
})

describe('warm pools that can’t schedule', () => {
  it('come from the server’s reserve rows whose source is the scheduler', () => {
    expect([...unschedulablePools(liveWorkspace())]).toEqual(['radar-agents-live/warm'])
    const controller = liveAttention().map((a) => (a.rule === 'reserveBelowTarget' ? { ...a, rawDetailSource: 'controller' as const } : a))
    expect(unschedulablePools(liveWorkspace({ attention: controller })).size).toBe(0)
    expect(unschedulablePools(undefined).size).toBe(0)
  })

  it('produces one canonical Reserve below target verdict for warm pools with a reserve row', () => {
    const server = serverReadings(liveWorkspace())
    const b = warmPoolBadge(warmPool('warm', { readyReplicas: 0 }), server)
    expect(b.label).toBe(RESERVE_BELOW_TARGET)
    expect(b.level).toBe('alert')
    expect(b.title).toContain('scheduler')
    // Without a server reserve row, a refilling pool stays Warming.
    const refilling = warmPoolBadge(warmPool('warm', { readyReplicas: 0 }), undefined)
    expect(refilling.label).toBeUndefined()
    expect(refilling.status.text).toBe('Warming')
  })
})
