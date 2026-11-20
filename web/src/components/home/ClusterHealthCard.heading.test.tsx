// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ClusterHealthCard } from './ClusterHealthCard'
import type { DashboardResponse } from '../../api/client'

vi.mock('./HomeAgentBlock', () => ({ HomeAgentBlock: () => null }))

const counts: DashboardResponse['resourceCounts'] = {
  pods: { total: 0, running: 0, pending: 0, failed: 0, succeeded: 0 },
  deployments: { total: 0, available: 0, unavailable: 0 },
  statefulSets: { total: 0, ready: 0, unready: 0 },
  daemonSets: { total: 0, ready: 0, unready: 0 },
  services: 0,
  ingresses: 0,
  nodes: { total: 0, ready: 0, notReady: 0, cordoned: 0 },
  namespaces: 0,
  jobs: { total: 0, active: 0, succeeded: 0, failed: 0 },
  cronJobs: { total: 0, active: 0, suspended: 0 },
  configMaps: 0,
  secrets: 0,
  pvcs: { total: 0, bound: 0, pending: 0, unbound: 0 },
}

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
})

function render(context: string) {
  const cluster: DashboardResponse['cluster'] = { name: context, kubeconfigCluster: 'gitlab_com', platform: 'kind', version: '', connected: true }
  act(() => {
    root!.render(
      <ClusterHealthCard
        health={{ healthy: 0, warning: 0, error: 0, warningEvents: 0 }}
        counts={counts}
        cluster={cluster}
        metrics={null}
        metricsServerAvailable={false}
        issueCount={0}
        hasCriticalIssues={false}
        nodeVersionSkew={null}
        onNavigateToKind={() => {}}
        onNavigateToView={() => {}}
      />,
    )
  })
  return host!.querySelector('h2')!
}

it('names the heading with the full context, which the middle cut hides from screen readers', () => {
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  const a = 'gitlab-org/infra/platform/k8s-agents:production-eu-west-agent'
  expect(render(a).getAttribute('aria-label')).toBe(a)
  const b = 'gitlab-org/infra/platform/k8s-agents:staging-us-east-agent'
  expect(render(b).getAttribute('aria-label')).toBe(b)
})

it('names an OpenShift heading with the full context, so two projects on one server stay apart', () => {
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  const a = 'proj-a/api-prod-example-com:6443/alice'
  const heading = render(a)
  expect(heading.getAttribute('aria-label')).toBe(a)
  expect(heading.textContent).not.toBe(a)
})
