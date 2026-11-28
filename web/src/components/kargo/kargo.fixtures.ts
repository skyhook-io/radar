import type { KargoProjectResponse, KargoStageView } from '../../api/kargo'

export function stage(overrides: Partial<KargoStageView> = {}): KargoStageView {
  return {
    name: 'test',
    depth: 0,
    warehouses: ['kargo-demo'],
    phase: 'Ready',
    autoPromotion: true,
    current: [{ name: 'c9e2bddf7415b036e09b96946d8ecf3a9a2257e5', origin: 'kargo-demo', alias: 'fallacious-robin', versions: ['nginx:1.31.10'], since: new Date(Date.now() - 2 * 3_600_000).toISOString() }],
    health: { status: 'Healthy' },
    verificationConfigured: false,
    whyNotHere: [{ origin: 'kargo-demo', state: 'current', reason: 'upToDate', message: 'Up to date.' }],
    ...overrides,
  }
}

export function project(overrides: Partial<KargoProjectResponse> = {}): KargoProjectResponse {
  return {
    name: 'kargo-demo',
    coverage: { stages: { state: 'full' }, freight: { state: 'full' }, warehouses: { state: 'full' } },
    freightRead: true,
    stages: [
      stage(),
      stage({ name: 'uat', depth: 1, warehouses: [], upstream: ['test'], verificationConfigured: true, verification: { phase: 'Failed', message: 'Metric "error-rate" assessed Failed', analysisRun: { namespace: 'kargo-demo', name: 'uat.01' } } }),
      stage({
        name: 'prod',
        depth: 2,
        warehouses: [],
        upstream: ['uat'],
        autoPromotion: false,
        current: [{ name: 'b56f6e30e82989f403c760e5d534403854ce59ef', origin: 'kargo-demo', alias: 'illocutionary-skunk', versions: ['nginx:1.31.9'] }],
        whyNotHere: [{
          origin: 'kargo-demo',
          state: 'behind',
          reason: 'verificationFailedUpstream',
          message: 'Failed verification in uat (error-rate). It can’t reach prod until it passes there.',
          newest: { name: 'c9e2bddf7415b036e09b96946d8ecf3a9a2257e5', origin: 'kargo-demo', alias: 'fallacious-robin', versions: ['nginx:1.31.10'] },
          stages: ['uat'],
          analysisRuns: [{ namespace: 'kargo-demo', name: 'uat.01' }],
        }],
        lastPromotion: { name: 'prod.01', phase: 'Errored', failedStep: 'git-push', failure: 'authentication required' },
      }),
    ],
    warehouses: [{ name: 'kargo-demo', conditions: [{ type: 'Ready', status: 'True', reason: 'Ready' }], latest: { name: 'c9e2bddf7415b036e09b96946d8ecf3a9a2257e5', origin: 'kargo-demo', alias: 'fallacious-robin', versions: ['nginx:1.31.10'] }, freightCount: 2 }],
    freight: [],
    freightTotal: 0,
    verificationRuns: {},
    issues: [],
    ...overrides,
  }
}
