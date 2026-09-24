import type { UsageDataStatus, UsageReport } from './usage-data'

// Test-only: a report and status shaped like the server's.
export function exampleReport(patch: Partial<UsageReport> = {}): UsageReport {
  return {
    schema: 3,
    version: '1.15.0', os: 'darwin', arch: 'arm64', installMethod: 'homebrew', mode: 'local',
    periodStart: '2026-09-23', periodEnd: '2026-09-24',
    setup: {
      authMode: 'none', timelineStorage: 'memory', mcpEnabled: true, prometheus: 'none',
      costSource: 'auto', browsers: ['chrome'],
    },
    engagement: { sessions: 2, activeMinutes: '20-49' },
    views: {}, actions: {}, mcpTools: {}, uiEvents: {}, errors: {},
    clusters: {
      contexts: '5-9', used: 1,
      shapes: [{ kubernetesVersion: '1.33', platform: 'eks', nodes: '10-19', integrations: ['argo-cd'] }],
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
    ask: true,
    shared: false,
    ownersDecide: false,
    choiceStorage: 'local',
    ...patch,
  }
}
