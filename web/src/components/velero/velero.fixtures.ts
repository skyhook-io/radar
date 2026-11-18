import type { VeleroLocationReading, VeleroScheduleReading, VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
const hoursAhead = (h: number) => new Date(Date.now() + h * 3_600_000).toISOString()

export function schedule(overrides: Partial<VeleroScheduleReading> = {}): VeleroScheduleReading {
  return {
    namespace: 'velero',
    name: 'nightly',
    created: hoursAgo(720),
    cron: '0 2 * * *',
    zone: { name: 'UTC', source: 'serverDefault' },
    state: 'onSchedule',
    attention: false,
    lastSubmitted: hoursAgo(2),
    nextRun: hoursAhead(22),
    nextRunCertain: true,
    runs: { read: 'full', kept: 1, newest: { name: 'nightly-1', phase: 'Completed', created: hoursAgo(2), ranAt: hoursAgo(2) }, newestCompleted: { name: 'nightly-1', phase: 'Completed', created: hoursAgo(2), ranAt: hoursAgo(2) } },
    location: { name: 'default' },
    ttl: '',
    includedNamespaces: [],
    excludedNamespaces: [],
    ...overrides,
  }
}

export function location(overrides: Partial<VeleroLocationReading> = {}): VeleroLocationReading {
  return {
    namespace: 'velero',
    name: 'default',
    phase: 'Available',
    provider: 'aws',
    bucket: 'backups',
    lastValidation: hoursAgo(0.01),
    frequency: { seconds: 60, source: 'server' },
    default: true,
    defaultSource: 'spec',
    backups: { targeting: 3, completedNotExpired: 2, expired: 1 },
    schedules: [{ name: 'nightly' }],
    attention: false,
    ...overrides,
  }
}

export function workspace(overrides: Partial<VeleroWorkspaceResponse> = {}): VeleroWorkspaceResponse {
  const full = { state: 'full' as const }
  return {
    observedAt: new Date().toISOString(),
    installed: true,
    context: 'kind-radar-velero-demo',
    coverage: { schedules: full, backups: full, backupStorageLocations: full, restores: full, backupRepositories: full },
    installations: [{ namespace: 'velero', server: { source: 'deployment', timeZone: 'UTC', defaultLocation: 'default', validationFrequencySeconds: 60 }, defaultLocation: 'default', defaultSource: 'spec', unassignedBackups: 0 }],
    schedules: [schedule()],
    locations: [location()],
    issues: [],
    issueTotals: {},
    ...overrides,
  }
}

export { hoursAgo, hoursAhead }
