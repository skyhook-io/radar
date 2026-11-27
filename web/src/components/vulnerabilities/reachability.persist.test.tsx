// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { getApiBase } from '../../api/config'
import type { VulnerabilityReachability, VulnerabilityWorkloadRef } from '../../api/vulnerabilities'

const web: VulnerabilityWorkloadRef = { kind: 'Deployment', group: 'apps', namespace: 'prio', name: 'lb-app' }
const result: VulnerabilityReachability = {
  allowed: true, namespace: 'prio', maxProbes: 5, checkedAt: '2026-11-26T20:00:00Z',
  routes: [{ route: { via: 'LoadBalancer', namespace: 'prio', name: 'lb-app' }, checks: [{ route: 'r', target: 'lb-app:80', outcome: 'answered', detail: 'HTTP 200' }] }],
}

afterEach(() => {
  sessionStorage.clear()
  vi.resetModules()
})

// A finished check outlives a reload of the tab, for that cluster and that
// workload only: another API group's workload of the same name gets nothing.
describe('the kept reachability result', () => {
  it('is read back after a reload, per cluster and workload', async () => {
    const key = `${getApiBase()}\x00ctx\x00apps/Deployment/prio/lb-app`
    sessionStorage.setItem('radar-vulnerability-reachability', JSON.stringify([[key, result]]))
    const { ReachabilityBadge, ReachabilityScope } = await import('./reachability')
    const container = document.createElement('div')
    const root = createRoot(container)
    const twin = { ...web, group: 'apps.example.com' }
    act(() => root.render(
      <>
        <ReachabilityScope scope="ctx"><span data-w="web"><ReachabilityBadge workload={web} /></span><span data-w="twin"><ReachabilityBadge workload={twin} /></span></ReachabilityScope>
        <ReachabilityScope scope="other"><span data-w="other"><ReachabilityBadge workload={web} /></span></ReachabilityScope>
      </>,
    ))
    expect(container.querySelector('[data-w="web"]')!.textContent).toBe('Answers inside the cluster')
    expect(container.querySelector('[data-w="twin"]')!.textContent).toBe('')
    expect(container.querySelector('[data-w="other"]')!.textContent).toBe('')
    act(() => root.unmount())
  })

  it('starts empty when storage is unreadable', async () => {
    sessionStorage.setItem('radar-vulnerability-reachability', '{not json')
    const { ReachabilityBadge, ReachabilityScope } = await import('./reachability')
    const container = document.createElement('div')
    const root = createRoot(container)
    act(() => root.render(<ReachabilityScope scope="ctx"><ReachabilityBadge workload={web} /></ReachabilityScope>))
    expect(container.textContent).toBe('')
    act(() => root.unmount())
  })
})
