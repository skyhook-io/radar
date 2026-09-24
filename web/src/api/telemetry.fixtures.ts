import type { UsageDataStatus, UsageReport } from './telemetry'

// Test-only: a report and status shaped like the server's.
export function exampleReport(patch: Partial<UsageReport> = {}): UsageReport {
  return {
    schema: 2,
    installId: '3f2b6c1e-8a4d-4c1f-9e2a-7b6d5c4e3f21',
    version: '1.15.0', os: 'darwin', arch: 'arm64', installMethod: 'homebrew', mode: 'local',
    periodStart: '2026-09-23', periodEnd: '2026-09-24',
    setup: {
      authMode: 'none', timelineStorage: 'memory', mcpEnabled: true, prometheus: 'none',
      costSource: 'auto', aiAgents: ['claude'], authPlugins: ['aws'], browsers: ['chrome'],
    },
    engagement: { sessions: 2, activeMinutes: '20-49' },
    views: {}, actions: {}, mcpTools: {}, uiEvents: {}, errors: {},
    clusters: {
      contexts: '5-9', used: 1,
      shapes: [{ kubernetesVersion: '1.33', platform: 'eks', nodes: '10-19', pods: '200-499', namespaces: '20-49', crds: '50-99', integrations: ['argo-cd'] }],
    },
    ...patch,
  }
}

export function exampleStatus(patch: Partial<UsageDataStatus> = {}): UsageDataStatus {
  return {
    state: 'undecided', source: 'default', canChange: true,
    endpoint: 'https://releases.skyhook.io/radar/usage', developmentBuild: false,
    preview: exampleReport(),
    firstRunPrompt: false,
    shared: false,
    choiceStorage: 'local',
    ...patch,
  }
}
