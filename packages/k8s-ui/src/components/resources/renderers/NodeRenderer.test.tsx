import { describe, expect, it } from 'vitest'
import { renderToString, renderToStaticMarkup } from 'react-dom/server'
import { NodeRenderer } from './NodeRenderer'

const node = {
  metadata: { name: 'kind-worker', labels: {} },
  spec: {},
  status: {
    nodeInfo: {
      osImage: 'Container-Optimized OS',
      architecture: 'amd64',
      kernelVersion: '6.1.0',
      containerRuntimeVersion: 'containerd://1.7',
      kubeletVersion: 'v1.33.0',
      kubeProxyVersion: 'v1.33.0',
    },
    capacity: { cpu: '4', memory: '8Gi', pods: '110' },
    allocatable: { cpu: '4', memory: '7Gi', pods: '110' },
    conditions: [],
  },
}

describe('NodeRenderer metrics', () => {
  it('renders a calm unavailable state when metrics-server is absent', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metricsHistory={{
          dataPoints: [],
          metricsUnavailableReason: 'the server could not find the requested resource',
        }}
      />,
    )

    expect(html).toContain('Resource Usage')
    expect(html).toContain('Metrics unavailable')
    expect(html).toContain('Radar cannot read metrics.k8s.io')
    expect(html).toContain('Metrics error details')
    expect(html).not.toContain('Install or repair metrics-server and its APIService')
    expect(html).not.toContain('Metrics collection error')
    expect(html).not.toContain('Collecting metrics data')
  })

  it('does not let stale live metrics hide an unavailable state', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metrics={{ usage: { cpu: '100m', memory: '256Mi' }, timestamp: '2026-06-30T00:00:00Z' }}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).not.toContain('100m')
    expect(html).not.toContain('Last updated')
  })

  it('keeps buffered historical charts visible under an unavailable notice', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsUnavailable
        metricsHistory={{
          dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
        }}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).toContain('CPU')
    expect(html).toContain('Memory')
    expect(html).not.toContain('Last updated')
  })

  it('keeps non-absence collection errors visible', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsHistory={{ dataPoints: [], collectionError: 'forbidden: no access to nodes' }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to nodes')
  })

  it('warns about non-absence collection errors even when historical samples exist', () => {
    const html = renderToString(
      <NodeRenderer
        data={node}
        metricsHistory={{
          collectionError: 'forbidden: no access to nodes',
          dataPoints: [{ timestamp: '2026-06-30T00:00:00Z', cpu: 100000000, memory: 268435456 }],
        }}
      />,
    )

    expect(html).toContain('Metrics collection error')
    expect(html).toContain('forbidden: no access to nodes')
    expect(html).toContain('CPU')
  })
})

describe('NodeRenderer removal advice', () => {
  const removal = {
    ...node,
    spec: { unschedulable: true, taints: [{ key: 'ToBeDeletedByClusterAutoscaler', value: String(Math.floor(Date.now() / 1000) - 60), effect: 'NoSchedule' }] },
    status: { ...node.status, conditions: [{ type: 'Ready', status: 'Unknown', lastTransitionTime: new Date(Date.now() - 1000).toISOString(), message: 'Kubelet stopped posting' }] },
  }
  it('does not give an autoscaler removal an error or uncordon advice', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={removal} />)
    expect(html).toContain('Removing (cluster autoscaler)')
    expect(html).not.toContain('Issues Detected')
    expect(html).not.toContain('Uncordon')
    expect(html).toContain('Pod and disruption-budget checks are not available')
  })
  it('does not assert who caused a bare cordon', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={{ ...node, spec: { unschedulable: true } }} />)
    expect(html).toContain('Cordoned')
    expect(html).toContain('who cordoned it is not recorded')
    expect(html).not.toContain('Uncordon to resume')
  })
  it('keeps observed readiness False neutral when it follows the removal start', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={{ ...removal, status: { ...removal.status, conditions: [{ ...removal.status.conditions[0], status: 'False' }] } }} />)
    expect(html).toContain('Removing (cluster autoscaler)')
    expect(html).toContain('Kubelet stopped posting')
    expect(html).not.toContain('1 failing')
    expect(html).not.toContain('Issues Detected')
  })
  it('shows named possible PDB blockers and terminating pods without invented progress', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={removal} removalPlan={{
      node: 'kind-worker', generatedAt: new Date().toISOString(), estimate: true,
      options: { ignoreDaemonSets: true, deleteEmptyDirData: true, force: true },
      summary: { evict: 1, skip: 0, mayBlock: 1 }, pdbsEvaluated: true,
      pods: [
        { namespace: 'shop', name: 'web', outcome: 'may-block', reason: 'PodDisruptionBudget shop/web allows no disruptions', pdb: 'shop/web', pdbChecked: true, emptyDir: false, terminating: false },
        { namespace: 'shop', name: 'old', outcome: 'evict', reason: 'already terminating', pdbChecked: false, emptyDir: false, terminating: true },
      ],
    }} />)
    expect(html).toContain('2 pods in the current snapshot')
    expect(html).toContain('1 terminating')
    expect(html).toContain('shop/web')
    expect(html).toContain('may block removal')
    expect(html).not.toContain('evicted')
  })
  it('uses warning tone for independent network failure during removal', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={{ ...removal, status: { conditions: [
      ...removal.status.conditions,
      { type: 'NetworkUnavailable', status: 'True', lastTransitionTime: new Date(Date.now() - 180_000).toISOString() },
    ] } }} />)
    expect(html).toContain('Removing (cluster autoscaler) · Network unavailable')
    expect(html).toContain('bg-amber-50')
    expect(html).not.toContain('rounded-lg bg-red-50')
    expect(html).not.toContain('rounded-lg bg-sky-50')
  })
  it('describes skip outcomes as drain estimates', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={removal} removalPlan={{
      node: 'kind-worker', generatedAt: new Date().toISOString(), estimate: true,
      options: { ignoreDaemonSets: true, deleteEmptyDirData: true, force: true },
      summary: { evict: 0, skip: 1, mayBlock: 0 }, pdbsEvaluated: true,
      pods: [{ namespace: 'shop', name: 'daemon', outcome: 'skip', reason: 'DaemonSet', pdbChecked: false, emptyDir: false, terminating: false }],
    }} />)
    expect(html).toContain('Skipped by drain estimate')
    expect(html).toContain('1 skipped by drain estimate')
    expect(html).not.toContain('Retained')
  })
  it('reports unavailable drain evidence as unavailable', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={removal} removalPlanError="forbidden: cannot list pods" />)
    expect(html).toContain('Drain details unavailable')
    expect(html).toContain('forbidden: cannot list pods')
    expect(html).not.toContain('0 pods')
  })
  it('does not present a cached snapshot as current after a refresh fails', () => {
    const html = renderToStaticMarkup(<NodeRenderer data={removal} removalPlanError="forbidden: cannot list pods" removalPlan={{
      node: 'kind-worker', generatedAt: new Date().toISOString(), estimate: true,
      options: { ignoreDaemonSets: true, deleteEmptyDirData: true, force: true },
      summary: { evict: 0, skip: 0, mayBlock: 0 }, pdbsEvaluated: true, pods: [],
    }} />)
    expect(html).toContain('Drain details unavailable')
    expect(html).not.toContain('0 pods in the current snapshot')
  })
})
