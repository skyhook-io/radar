import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ApiError, type PrometheusPVCUsage } from '../../api/client'

let statusResult: { data?: { connected: boolean; discovering?: boolean }; error?: Error }
let usageResult: { data?: PrometheusPVCUsage; error?: Error }
vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  usePrometheusPVCUsage: () => usageResult,
  usePrometheusStatus: () => statusResult,
  useAutoPromConnect: () => undefined,
}))
const { PVCUsageBar } = await import('./PVCUsageBar')
const measured: PrometheusPVCUsage = { namespace: 'demo', name: 'disk', used: 0, capacity: 1024, ratio: 0, hasData: true, status: 'available' }
const render = () => renderToString(<PVCUsageBar namespace="demo" name="disk" />)

describe('PVC usage availability', () => {
  beforeEach(() => {
    statusResult = { data: { connected: true } }
    usageResult = {}
  })
  it.each([
    ['no_series', 'No usage measurements reported for this volume'],
    ['invalid_data', 'Volume usage measurements are invalid'],
    ['query_failed', 'The usage query failed'],
  ] as const)('explains %s without a gauge', (status, message) => {
    usageResult = { data: { ...measured, hasData: false, status } }
    expect(render()).toContain(message)
    expect(render()).not.toContain('width:')
  })
  it('reports discovery as in progress', () => {
    statusResult = { data: { connected: false, discovering: true } }
    expect(render()).toContain('Discovering Prometheus')
    expect(render()).not.toContain('not connected')
  })
  it('does not attribute status denial to this PVC', () => {
    statusResult = { error: new ApiError('forbidden', 403) }
    expect(render()).toContain('Metrics access denied')
  })
  it('renders valid zero usage as a measurement', () => {
    usageResult = { data: measured }
    expect(render().replaceAll('<!-- -->', '')).toContain('(0%)')
    expect(render()).toContain('width:0%')
  })
  it('shows loading instead of an empty row', () => {
    expect(render()).toContain('Loading usage measurements')
  })
  it('does not present stale usage after a refresh failure', () => {
    usageResult = { data: measured, error: new ApiError('failed', 500) }
    expect(render()).toContain('The usage query failed')
    expect(render()).not.toContain('width:')
  })
  it('does not present stale usage when disconnected', () => {
    statusResult = { data: { connected: false } }
    usageResult = { data: measured }
    expect(render()).toContain('Prometheus is not connected')
    expect(render()).not.toContain('width:')
  })
  it('distinguishes denied access from unavailable measurements', () => {
    usageResult = { data: measured, error: new ApiError('forbidden', 403) }
    expect(render()).toContain('access to usage metrics for this PVC')
    expect(render()).not.toContain('width:')
  })
  it('does not reuse metrics if connection status cannot be checked', () => {
    statusResult = { data: { connected: true }, error: new Error('failed') }
    usageResult = { data: measured }
    expect(render()).toContain('Could not check the metrics connection')
    expect(render()).not.toContain('width:')
  })
})
