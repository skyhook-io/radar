import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ApplicationDetail, type ApplicationDetailProps } from './ApplicationDetail'
import type { AppHistory, AppRow, AppSourceRef } from '../../utils/applications'
import { workloadKey } from '../../utils/topology-neighborhood'

const app: AppRow = {
  key: "app:prod:checkout",
  name: "checkout",
  namespace: "prod",
  health: "healthy",
  workload_class: "mixed",
  appVersion: "1.2.3",
  workloads: [
    {
      kind: "Deployment",
      namespace: "prod",
      name: "checkout-api",
      workload_class: "service",
      health: "healthy",
      ready: 3,
      desired: 3,
      restarts: 0,
      version: "1.2.3",
    },
    {
      kind: "Deployment",
      namespace: "prod",
      name: "checkout-worker",
      workload_class: "worker",
      health: "healthy",
      ready: 1,
      desired: 1,
      restarts: 0,
      version: "1.2.3",
    },
  ],
  relationships: {
    services: ["checkout-api"],
    ingresses: ["checkout"],
    configs: 2,
    scalers: 1,
    storage: 1,
    networkPolicies: 1,
    serviceRefs: [{ kind: 'Service', namespace: 'prod', name: 'checkout-api' }],
    ingressRefs: [{ kind: 'Ingress', namespace: 'prod', name: 'checkout' }],
    configRefs: [
      { kind: 'ConfigMap', namespace: 'prod', name: 'checkout-config' },
      { kind: 'Secret', namespace: 'prod', name: 'checkout-secret' },
    ],
    scalerRefs: [{ kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'checkout-api' }],
    storageRefs: [{ kind: 'PersistentVolumeClaim', namespace: 'prod', name: 'checkout-data' }],
    networkPolicyRefs: [{ kind: 'NetworkPolicy', namespace: 'prod', name: 'checkout-ingress' }],
  },
  events: [
    {
      type: "Warning",
      reason: "BackOff",
      object: "Deployment/checkout-api",
      message: "Back-off restarting failed container",
      count: 2,
      lastSeen: "1m ago",
    },
  ],
};

function renderDetail(props: Partial<ApplicationDetailProps> = {}) {
  const componentProps = {
    app,
    onBack: () => {},
    renderWorkload: (workload) => <div>Runtime for {workload.name}</div>,
    renderOverviewIssues: () => null,
    ...props,
  } as ApplicationDetailProps;
  return renderToString(<ApplicationDetail {...componentProps} />);
}

describe("ApplicationDetail shell", () => {
  it("preserves the built-in issue surface when the host slot is omitted", () => {
    const html = renderDetail({ renderOverviewIssues: undefined });

    expect(html).toContain('Application views')
    expect(html).toContain('Overview')
    expect(html).toContain('Topology')
    expect(html).toContain('History')
    expect(html).toContain('BackOff on checkout-api')
    expect(html).toContain('Application identity')
    expect(html).toContain('Grouped from Kubernetes ownership')
    expect(html).toContain('Entrypoints')
    expect(html).toContain('Dependencies')
    expect(html).toContain('svc<!-- -->/</span>checkout-api')
    expect(html).toContain('ing<!-- -->/</span>checkout')
    expect(html).toContain('Configuration')
    expect(html).toContain('Autoscaling')
    expect(html).toContain('Storage')
    expect(html).toContain('Network policy')
    expect(html).not.toContain('Related resources')
    expect(html).toContain('Workloads')
    expect(html).not.toContain('Runtime for')
    expect(html).not.toContain('Components')
    expect(html).not.toContain('Changes')
    expect(html).not.toContain('YAML')
    expect(html).not.toContain('Cost')
    expect(html).not.toContain('>Deploy<')
  })

  it("defaults to the application overview scope", () => {
    const html = renderDetail();

    expect(html).toContain("Application views");
    expect(html).toContain("Overview");
    expect(html).toContain("Topology");
    expect(html).toContain("History");
    expect(html).not.toContain("BackOff on checkout-api");
    expect(html).toContain("Application identity");
    expect(html).toContain("Entrypoints");
    expect(html).toContain("Dependencies");
    expect(html).toContain("svc<!-- -->/</span>checkout-api");
    expect(html).toContain("ing<!-- -->/</span>checkout");
    expect(html).toContain("Configuration");
    expect(html).toContain("Autoscaling");
    expect(html).toContain("Storage");
    expect(html).not.toContain("Related resources");
    expect(html).toContain("Workloads");
    expect(html).not.toContain("Runtime for");
    expect(html).not.toContain("Components");
    expect(html).not.toContain("Changes");
    expect(html).not.toContain("YAML");
    expect(html).not.toContain("Cost");
    expect(html).not.toContain(">Deploy<");
  });

  it("shows workload scope without application view tabs when the URL names a workload", () => {
    const selected = app.workloads[1];
    const html = renderDetail({
      selectedView: "overview",
      onSelectView: () => {},
      selectedWorkloadKey: workloadKey(selected),
      onSelectWorkload: () => {},
    });

    expect(html).toContain('Runtime for')
    expect(html).toContain('checkout-worker')
    expect(html).not.toContain('Application views')
    expect(html).not.toContain('Overview')
    expect(html).not.toContain('Topology')
    expect(html).not.toContain('History')
    expect(html).not.toContain('Application identity')
  })

  it("does not render a workload selector for a single-workload app", () => {
    const singleApp: AppRow = {
      ...app,
      workloads: [app.workloads[0]],
    };
    const selected = singleApp.workloads[0];
    const html = renderDetail({
      app: singleApp,
      selectedWorkloadKey: workloadKey(selected),
      onSelectWorkload: () => {},
    });

    expect(html).toContain("Runtime for");
    expect(html).toContain("checkout-api");
    expect(html).toContain("Workload");
    expect(html).toContain("Deployment<!-- -->/<!-- -->checkout-api");
    expect(html).not.toContain('aria-haspopup="listbox"');
    expect(html).not.toContain("Application views");
  });

  it("opens the workload directly for uncontrolled single-workload apps", () => {
    const singleApp: AppRow = {
      ...app,
      workloads: [app.workloads[0]],
    };
    const html = renderDetail({ app: singleApp });

    expect(html).toContain("Runtime for");
    expect(html).toContain("checkout-api");
    expect(html).not.toContain("Application views");
  });

  it("renders application incidents in History", () => {
    const html = renderDetail({
      selectedView: "history",
      onSelectView: () => {},
      selectedWorkloadKey: null,
      onSelectWorkload: () => {},
    });

    expect(html).toContain("Current incidents");
    expect(html).toContain("BackOff on Deployment/checkout-api");
    expect(html).toContain("Deployment/checkout-api");
    expect(html).toContain("Open workload");
  });

  it("previews the latest retained deployment change in Overview", () => {
    const healthyApp: AppRow = { ...app, events: [] };
    const history: AppHistory = {
      appKey: healthyApp.key,
      sourceRef: {
        type: "gitops",
        tool: "argocd",
        group: "argoproj.io",
        kind: "Application",
        namespace: "argocd",
        name: "checkout",
      },
      summary: {
        state: "change",
        title: "Argo CD sync",
        detail: "Succeeded · abc123",
        timestamp: "2026-07-08T12:00:00Z",
      },
      anchors: [
        {
          type: "gitops",
          title: "Argo CD sync",
          status: "Succeeded",
          revision: "abc123",
          timestamp: "2026-07-08T12:00:00Z",
        },
      ],
    };
    const html = renderDetail({
      app: healthyApp,
      history,
      onOpenSource: () => {},
    });

    expect(html).toContain('Latest change')
    expect(html).toContain('Argo CD sync')
    expect(html).toContain('History')
    expect(html).toContain('View Argo CD application')
    expect(html).not.toContain('No application issues detected')
  })

  it('shows an authoritative deployment source without duplicating its action in history', () => {
    const sourceRef: AppSourceRef = { type: 'gitops', tool: 'flux', group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'checkout' }
    const sourceApp: AppRow = { ...app, events: [], sourceRef }
    const history: AppHistory = {
      appKey: sourceApp.key,
      sourceRef,
      summary: { state: 'change', title: 'Flux reconciliation', timestamp: '2026-07-08T12:00:00Z' },
    }
    const html = renderDetail({ app: sourceApp, history, onOpenSource: () => {} })

    expect(html).toContain('Deployment source')
    expect(html).toContain('Flux Kustomization')
    expect(html).toContain('flux-system/checkout')
    expect(html.match(/View Flux Kustomization/g)).toHaveLength(1)
    expect(html).not.toContain('App key')
    expect(html).not.toContain('Confidence')
    expect(html).not.toContain('Source &amp; provenance')
  })

  it('separates healthy runtime from degraded delivery and rolls up the application status', () => {
    const sourceRef: AppSourceRef = { type: 'gitops', tool: 'argocd', group: 'argoproj.io', kind: 'Application', namespace: 'argocd', name: 'checkout' }
    const html = renderDetail({
      app: {
        ...app,
        health: 'degraded',
        runtimeHealth: 'healthy',
        sourceRef,
        sourceStatus: { sync: 'Synced', health: 'Degraded' },
      },
      onOpenSource: () => {},
    })

    expect(html).toContain('Degraded')
    expect(html).toContain('Runtime')
    expect(html).toContain('Healthy')
    expect(html).toContain('4/4 ready')
    expect(html).toContain('Delivery')
    expect(html.match(/Synced/g)).toHaveLength(2)
    expect((html.match(/Degraded/g) ?? []).length).toBeGreaterThanOrEqual(3)
  })

  it('warns when application workloads resolve to different deployment sources', () => {
    const html = renderDetail({ app: { ...app, sourceConflict: true } })

    expect(html).toContain('Deployment source')
    expect(html).toContain('Multiple deployment sources detected')
    expect(html).toContain('do not share one deployment manager')
  })

  it('distinguishes inferred application identity from declared identity', () => {
    const inferred = renderDetail({
      app: {
        ...app,
        identity: { key: 'checkout', env: 'prod', confidence: 'medium', evidence: 'name stem checkout + shared image repo', source: 'name-stem' },
      },
    })
    expect(inferred).toContain('Application identity')
    expect(inferred).toContain('Inferred application boundary')
    expect(inferred).toContain('a shared name + image')

    const declared = renderDetail({
      app: {
        ...app,
        identity: { key: 'checkout', env: 'prod', confidence: 'high', evidence: 'app.skyhook.io/app=checkout', source: 'explicit' },
      },
    })
    expect(declared).toContain('Identified by the app.skyhook.io/app annotation')
    expect(declared).not.toContain('Inferred application boundary')
  })

  it('uses tier provenance when a grouped app has no cross-app identity', () => {
    const html = renderDetail({ app: { ...app, tier: 5 } })

    expect(html).toContain('Application identity')
    expect(html).toContain('Grouped by Helm metadata')
    expect(html).not.toContain('Grouped from Kubernetes ownership')
  })

  it('does not present completed init containers as a long-running workload state', () => {
    const statefulApp: AppRow = {
      ...app,
      workloads: [
        { ...app.workloads[0], kind: 'StatefulSet', name: 'redis-master', reason: 'Completed' },
        { ...app.workloads[1], kind: 'StatefulSet', name: 'redis-replicas', reason: 'Completed' },
      ],
    }
    const jobApp: AppRow = {
      ...app,
      workloads: [
        { ...app.workloads[0], kind: 'Job', name: 'database-migration', workload_class: 'job', reason: 'Completed' },
        { ...app.workloads[1], kind: 'CronJob', name: 'database-migration-schedule', workload_class: 'job' },
      ],
    }

    expect(renderDetail({ app: statefulApp })).not.toMatch(/redis-master<\/button><div[^>]*>Completed/)
    expect(renderDetail({ app: jobApp })).toMatch(/database-migration<\/button><div[^>]*>Completed/)
  })

  it("does not duplicate current incidents in the Overview history preview", () => {
    const history: AppHistory = {
      appKey: app.key,
      summary: {
        state: "incident",
        title: "Current incident: BackOff on Deployment/checkout-api",
        detail: "Back-off restarting failed container",
      },
      incidents: [
        {
          severity: "warning",
          title: "BackOff on Deployment/checkout-api",
          object: "Deployment/checkout-api",
        },
      ],
    };
    const html = renderDetail({ history });

    expect(html).not.toContain("BackOff on checkout-api");
    expect(html).not.toContain(
      "Current incident: BackOff on Deployment/checkout-api",
    );
    expect(html).not.toContain("Latest change");
  });

  it('previews retained incidents in Overview when no current issue card exists', () => {
    const healthyApp: AppRow = { ...app, events: [] }
    const history: AppHistory = {
      appKey: healthyApp.key,
      summary: { state: 'incident', title: 'Current incident: FailedScheduling on Pod/checkout-api-abc', detail: '0/9 nodes are available' },
      incidents: [{ severity: 'warning', title: 'FailedScheduling on Pod/checkout-api-abc', object: 'Pod/checkout-api-abc' }],
    }
    const html = renderDetail({
      app: healthyApp,
      history,
    })

    expect(html).toContain('Latest incident')
    expect(html).toContain('Current incident: FailedScheduling on Pod/checkout-api-abc')
    expect(html).toContain('View history')
  })

  it('does not duplicate retained incidents when the host renders current issues', () => {
    const healthyApp: AppRow = { ...app, events: [] }
    const history: AppHistory = {
      appKey: healthyApp.key,
      summary: { state: 'incident', title: 'Current incident: FailedScheduling on Pod/checkout-api-abc' },
    }
    const html = renderDetail({
      app: healthyApp,
      history,
      renderOverviewIssues: () => <div>Operational Issues (1)</div>,
      hasOverviewIssues: true,
    })

    expect(html).toContain('Operational Issues (1)')
    expect(html).not.toContain('Latest incident')
    expect(html).not.toContain('Current incident: FailedScheduling on Pod/checkout-api-abc')
  })

  it('does not report idle zero-replica workloads as application issues', () => {
    const idleApp: AppRow = {
      ...app,
      health: "healthy",
      events: [],
      workloads: [
        {
          kind: "CronJob",
          namespace: "prod",
          name: "checkout-cleanup",
          workload_class: "job",
          health: "neutral",
          ready: 0,
          desired: 0,
          restarts: 0,
          version: "1.2.3",
        },
        {
          kind: "Job",
          namespace: "prod",
          name: "checkout-cleanup-123",
          workload_class: "job",
          health: "unknown",
          ready: 0,
          desired: 0,
          restarts: 0,
          version: "1.2.3",
        },
      ],
    };
    const html = renderDetail({ app: idleApp });

    expect(html).not.toContain('No application issues detected')
    expect(html).not.toContain('needs attention')
  })

  it("does not link ambiguous event objects to a workload", () => {
    const ambiguousApp: AppRow = {
      ...app,
      workloads: [
        ...app.workloads,
        {
          kind: "Deployment",
          namespace: "canary",
          name: "checkout-api",
          workload_class: "service",
          health: "healthy",
          ready: 1,
          desired: 1,
          restarts: 0,
          version: "1.2.3",
        },
      ],
    };
    const html = renderDetail({
      app: ambiguousApp,
      selectedView: "history",
      onSelectView: () => {},
      selectedWorkloadKey: null,
      onSelectWorkload: () => {},
    });

    expect(html).toContain("Deployment/checkout-api");
    expect(html).not.toContain("Open workload");
  });

  it("shows batch activity and injected operational issues on the application overview", () => {
    const batchApp: AppRow = {
      key: "app:prod:batch",
      name: "batch",
      namespace: "prod",
      health: "unhealthy",
      workload_class: "job",
      workloads: [
        {
          kind: "CronJob",
          namespace: "prod",
          name: "nightly",
          workload_class: "job",
          health: "healthy",
          ready: 0,
          desired: 0,
          restarts: 0,
          batch: {
            schedule: "0 3 * * *",
            retainedRuns: 3,
            succeededRuns: 3,
            failedRuns: 0,
            activeRuns: 0,
            latestRunName: "nightly-29720340",
            latestRunPhase: "Succeeded",
            latestStartedAt: "2026-07-05T10:25:00Z",
            lastSuccessfulAt: "2026-07-05T10:29:00Z",
          },
        },
        {
          kind: "Job",
          namespace: "prod",
          name: "ad-hoc-import",
          workload_class: "job",
          health: "unhealthy",
          ready: 0,
          desired: 1,
          restarts: 0,
          batch: {
            retainedRuns: 1,
            succeededRuns: 0,
            failedRuns: 1,
            activeRuns: 0,
            latestRunName: "ad-hoc-import",
            latestRunPhase: "Failed",
            latestStartedAt: "2026-07-05T09:25:00Z",
            message: "Job has reached the specified backoff limit",
          },
        },
      ],
    };
    const html = renderDetail({
      app: batchApp,
      renderOverviewIssues: () => <div>Operational Issues (1)</div>,
    });

    expect(html).toContain("Application views");
    expect(html).toContain("Operational Issues (1)");
    expect(html).not.toContain("Batch activity");
    const workloadsPanel = html.indexOf(">Workloads</h2>");
    expect(workloadsPanel).toBeGreaterThanOrEqual(0);
    expect(html).toContain("Batch resources");
    expect(html).toContain("Active runs");
    expect(html).toContain("Retained runs");
    expect(html).toContain("1 CronJob / 1 Job");
    expect(html).toContain("3 succeeded / 1 failed");
    expect(html).toContain("nightly-29720340");
    expect(html).toContain("ad-hoc-import");
    expect(html).not.toContain("ad-hoc-import is down");
    expect(html).not.toContain("Runtime for");
    expect(html).toContain('2xl:grid-cols-4');
  });

  it("uses the delivery verdict in a successful batch application header", () => {
    const batchApp: AppRow = {
      key: "app:prod:batch",
      name: "batch",
      namespace: "prod",
      health: "unhealthy",
      runtimeHealth: "unhealthy",
      workload_class: "job",
      sourceStatus: { sync: "Synced", health: "Degraded" },
      workloads: [
        {
          kind: "CronJob",
          namespace: "prod",
          name: "nightly",
          workload_class: "job",
          health: "unhealthy",
          ready: 0,
          desired: 0,
          restarts: 0,
          batch: {
            retainedRuns: 2,
            succeededRuns: 1,
            failedRuns: 1,
            activeRuns: 0,
            latestRunPhase: "Succeeded",
          },
        },
      ],
    };
    const html = renderDetail({ app: batchApp });

    expect(html).toMatch(/<span class="text-sm font-semibold">Degraded<\/span>/);
    expect(html).toContain("Succeeded");
  });
});
