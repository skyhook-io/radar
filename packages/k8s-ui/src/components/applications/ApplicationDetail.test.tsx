import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ApplicationDetail, type ApplicationDetailProps } from './ApplicationDetail'
import type { AppHistory, AppRow } from '../../utils/applications'
import { workloadKey } from '../../utils/topology-neighborhood'

const app: AppRow = {
  key: 'app:prod:checkout',
  name: 'checkout',
  namespace: 'prod',
  health: 'healthy',
  workload_class: 'mixed',
  appVersion: '1.2.3',
  workloads: [
    {
      kind: 'Deployment',
      namespace: 'prod',
      name: 'checkout-api',
      workload_class: 'service',
      health: 'healthy',
      ready: 3,
      desired: 3,
      restarts: 0,
      version: '1.2.3',
    },
    {
      kind: 'Deployment',
      namespace: 'prod',
      name: 'checkout-worker',
      workload_class: 'worker',
      health: 'healthy',
      ready: 1,
      desired: 1,
      restarts: 0,
      version: '1.2.3',
    },
  ],
  relationships: {
    services: ['checkout-api'],
    ingresses: ['checkout'],
    configs: 2,
    scalers: 1,
    storage: 1,
  },
  events: [
    {
      type: 'Warning',
      reason: 'BackOff',
      object: 'Deployment/checkout-api',
      message: 'Back-off restarting failed container',
      count: 2,
      lastSeen: '1m ago',
    },
  ],
}

function renderDetail(props: Partial<ApplicationDetailProps> = {}) {
  const componentProps = {
    app,
    onBack: () => {},
    renderWorkload: (workload) => <div>Runtime for {workload.name}</div>,
    ...props,
  } as ApplicationDetailProps
  return renderToString(<ApplicationDetail {...componentProps} />)
}

describe('ApplicationDetail shell', () => {
  it('defaults to the application overview scope', () => {
    const html = renderDetail()

    expect(html).toContain('Application views')
    expect(html).toContain('Overview')
    expect(html).toContain('Topology')
    expect(html).toContain('History')
    expect(html).toContain('BackOff on checkout-api')
    expect(html).toContain('Source &amp; provenance')
    expect(html).toContain('Entrypoints')
    expect(html).toContain('Dependencies')
    expect(html).toContain('Service<!-- -->/</span>checkout-api')
    expect(html).toContain('Ingress<!-- -->/</span>checkout')
    expect(html).toContain('Configuration')
    expect(html).toContain('Autoscaling')
    expect(html).toContain('Storage')
    expect(html).not.toContain('Related resources')
    expect(html).toContain('Workloads')
    expect(html).not.toContain('Runtime for')
    expect(html).not.toContain('Components')
    expect(html).not.toContain('Changes')
    expect(html).not.toContain('YAML')
    expect(html).not.toContain('Cost')
    expect(html).not.toContain('>Deploy<')
  })

  it('shows workload scope without application view tabs when the URL names a workload', () => {
    const selected = app.workloads[1]
    const html = renderDetail({
      selectedView: 'overview',
      onSelectView: () => {},
      selectedWorkloadKey: workloadKey(selected),
      onSelectWorkload: () => {},
    })

    expect(html).toContain('Runtime for')
    expect(html).toContain('checkout-worker')
    expect(html).not.toContain('Application views')
    expect(html).not.toContain('Overview')
    expect(html).not.toContain('Topology')
    expect(html).not.toContain('History')
    expect(html).not.toContain('Source &amp; provenance')
  })

  it('does not render a workload selector for a single-workload app', () => {
    const singleApp: AppRow = {
      ...app,
      workloads: [app.workloads[0]],
    }
    const selected = singleApp.workloads[0]
    const html = renderDetail({
      app: singleApp,
      selectedWorkloadKey: workloadKey(selected),
      onSelectWorkload: () => {},
    })

    expect(html).toContain('Runtime for')
    expect(html).toContain('checkout-api')
    expect(html).toContain('Workload')
    expect(html).toContain('Deployment<!-- -->/<!-- -->checkout-api')
    expect(html).not.toContain('aria-haspopup="listbox"')
    expect(html).not.toContain('Application views')
  })

  it('opens the workload directly for uncontrolled single-workload apps', () => {
    const singleApp: AppRow = {
      ...app,
      workloads: [app.workloads[0]],
    }
    const html = renderDetail({ app: singleApp })

    expect(html).toContain('Runtime for')
    expect(html).toContain('checkout-api')
    expect(html).not.toContain('Application views')
  })

  it('renders application incidents in History', () => {
    const html = renderDetail({
      selectedView: 'history',
      onSelectView: () => {},
      selectedWorkloadKey: null,
      onSelectWorkload: () => {},
    })

    expect(html).toContain('Current incidents')
    expect(html).toContain('BackOff on Deployment/checkout-api')
    expect(html).toContain('Deployment/checkout-api')
    expect(html).toContain('Open workload')
  })

  it('previews the latest retained deployment change in Overview', () => {
    const healthyApp: AppRow = { ...app, events: [] }
    const history: AppHistory = {
      appKey: healthyApp.key,
      sourceRef: { type: 'gitops', tool: 'argocd', group: 'argoproj.io', kind: 'Application', namespace: 'argocd', name: 'checkout' },
      summary: { state: 'change', title: 'Argo CD sync', detail: 'Succeeded · abc123', timestamp: '2026-07-08T12:00:00Z' },
      anchors: [{ type: 'gitops', title: 'Argo CD sync', status: 'Succeeded', revision: 'abc123', timestamp: '2026-07-08T12:00:00Z' }],
    }
    const html = renderDetail({
      app: healthyApp,
      history,
      onOpenSource: () => {},
    })

    expect(html).toContain('Latest change')
    expect(html).toContain('Argo CD sync')
    expect(html).toContain('View history')
    expect(html).toContain('View GitOps source')
  })

  it('does not duplicate current incidents in the Overview history preview', () => {
    const history: AppHistory = {
      appKey: app.key,
      summary: { state: 'incident', title: 'Current incident: BackOff on Deployment/checkout-api', detail: 'Back-off restarting failed container' },
      incidents: [{ severity: 'warning', title: 'BackOff on Deployment/checkout-api', object: 'Deployment/checkout-api' }],
    }
    const html = renderDetail({ history })

    expect(html).toContain('BackOff on checkout-api')
    expect(html).not.toContain('Current incident: BackOff on Deployment/checkout-api')
    expect(html).not.toContain('Latest change')
  })

  it('does not report idle zero-replica workloads as application issues', () => {
    const idleApp: AppRow = {
      ...app,
      health: 'healthy',
      events: [],
      workloads: [
        {
          kind: 'CronJob',
          namespace: 'prod',
          name: 'checkout-cleanup',
          workload_class: 'job',
          health: 'neutral',
          ready: 0,
          desired: 0,
          restarts: 0,
          version: '1.2.3',
        },
        {
          kind: 'Job',
          namespace: 'prod',
          name: 'checkout-cleanup-123',
          workload_class: 'job',
          health: 'unknown',
          ready: 0,
          desired: 0,
          restarts: 0,
          version: '1.2.3',
        },
      ],
    }
    const html = renderDetail({ app: idleApp })

    expect(html).toContain('No application issues detected')
    expect(html).not.toContain('needs attention')
  })

  it('does not link ambiguous event objects to a workload', () => {
    const ambiguousApp: AppRow = {
      ...app,
      workloads: [
        ...app.workloads,
        {
          kind: 'Deployment',
          namespace: 'canary',
          name: 'checkout-api',
          workload_class: 'service',
          health: 'healthy',
          ready: 1,
          desired: 1,
          restarts: 0,
          version: '1.2.3',
        },
      ],
    }
    const html = renderDetail({
      app: ambiguousApp,
      selectedView: 'history',
      onSelectView: () => {},
      selectedWorkloadKey: null,
      onSelectWorkload: () => {},
    })

    expect(html).toContain('Deployment/checkout-api')
    expect(html).not.toContain('Open workload')
  })
})
