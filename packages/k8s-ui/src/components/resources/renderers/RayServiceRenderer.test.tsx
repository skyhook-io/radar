import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { RayServiceRenderer, rayServiceConditionTone, serveStateSeverity } from './RayServiceRenderer'

const condition = (type: string, status = 'True', extra = {}) => ({ type, status, observedGeneration: 3, ...extra })
const root = (status = {}, spec = {}) => ({ apiVersion: 'ray.io/v1', kind: 'RayService', metadata: { namespace: 'ml', name: 'serve', uid: 'root', generation: 3 }, spec, status: { observedGeneration: 3, ...status } })
const render = (data: any) => renderToStaticMarkup(<RayServiceRenderer data={data} />)

describe('RayService detail', () => {
  it('keeps proxy readiness, upgrade and rollback independent', () => {
    const html = render(root({ conditions: [condition('Ready'), condition('UpgradeInProgress'), condition('RollbackInProgress')], numServeEndpoints: 2 }))
    for (const text of ['Proxy Readiness', 'Ready', 'UpgradeInProgress', 'RollbackInProgress', 'Reported Serve Endpoints', 'not that every application']) expect(html).toContain(text)
  })
  it('shows suspension intent separately from acknowledgement and retains observed teardown after intent changes', () => {
    expect(render(root({}, { suspend: true }))).toContain('Suspension Requested')
    expect(render(root({ conditions: [condition('Suspending')] }, { suspend: false }))).toContain('Suspending')
    expect(render(root({ conditions: [condition('Suspended')] }))).toContain('Suspended')
  })
  it('does not turn absent maps or absent percentages into zeros or an outage', () => {
    const html = render(root({ activeServiceStatus: { rayClusterName: 'old' }, pendingServiceStatus: { rayClusterName: 'new', targetCapacity: 0, trafficRoutedPercent: 0 } }))
    for (const text of ['Active Revision', 'Pending Revision', 'Application status is not reported', 'endpoint still serves requests', '0%', 'configured route weight']) expect(html).toContain(text)
    expect(html).not.toContain('Applications (0)')
    expect(html.match(/Serve Target Capacity/g)).toHaveLength(1)
    expect(html.match(/Configured Traffic Share/g)).toHaveLength(1)
  })
  it('supports traffic-only incremental observations and strategy None without inventing pending slots', () => {
    const html = render(root({ activeServiceStatus: { rayClusterName: 'old', trafficRoutedPercent: 100 } }, { upgradeStrategy: { type: 'None' } }))
    expect(html).toContain('100%')
    expect(html).not.toContain('Serve Target Capacity')
    expect(html).not.toContain('Pending Revision')
    expect(html).toContain('None')
  })
  it('preserves app/deployment failures even when Ready and a runtime is healthy', () => {
    const data = root({ conditions: [condition('Ready')], pendingServiceStatus: { rayClusterName: 'new', applicationStatuses: { model: { status: 'DEPLOY_FAILED', message: 'Model import failed', serveDeploymentStatuses: { decoder: { status: 'UNHEALTHY', message: 'Replica constructor failed' } } } } } })
    const html = renderToStaticMarkup(<RayServiceRenderer data={data} runtimeEvidence={{ pending: <p>HeadPodReady</p> }} />)
    for (const text of ['Ready', 'HeadPodReady', 'DEPLOY_FAILED', 'Model import failed', 'UNHEALTHY', 'Replica constructor failed']) expect(html).toContain(text)
  })
  it('never trusts embedded cluster conditions and bounds large application maps with nested failures first', () => {
    const apps = Object.fromEntries(Array.from({ length: 21 }, (_, i) => [`app-${i}`, { status: 'RUNNING' }])) as Record<string, any>
    apps['zz-failed-deployment'] = { status: 'RUNNING', serveDeploymentStatuses: { replica: { status: 'UNHEALTHY', message: 'Visible failure' } } }
    const html = render(root({ activeServiceStatus: { rayClusterName: 'old', rayClusterStatus: { conditions: [condition('HeadPodReady', 'False', { message: 'OBSOLETE EMBEDDED FAILURE' })] }, applicationStatuses: apps } }))
    expect(html).toContain('Visible failure')
    expect(html).toContain('Showing 20 of 22 applications')
    expect(html).not.toContain('OBSOLETE EMBEDDED FAILURE')
  })
  it('labels missing and stale observations without inventing conditions', () => {
    expect(render(root({ observedGeneration: 2 }))).toContain('earlier generation')
    expect(render(root({ observedGeneration: undefined }))).toContain('not reported an observed generation')
    expect(render(root({ conditions: [condition('Ready', 'True', { observedGeneration: 2 })] }))).not.toContain('(stale)')
    expect(rayServiceConditionTone(condition('Ready', 'True', { observedGeneration: 2 }))).toBe('ok')
    const suspended = render(root({ observedGeneration: 2, conditions: [condition('Suspended')] }, { suspend: true }))
    expect(suspended).toContain('Reconciliation is paused')
    expect(suspended).not.toContain('Reported state describes an earlier generation')
    expect(serveStateSeverity('FUTURE_STATE')).toBe('neutral')
    expect(serveStateSeverity('DELETING')).toBe('warning')
  })
})
