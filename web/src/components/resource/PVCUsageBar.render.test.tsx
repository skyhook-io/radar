import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { NavCustomizationProvider } from '../../context/NavCustomization'
import { ApiError, type PrometheusPVCUsage } from '../../api/client'

let canConfigure = true
let roleLoading = false
let previousMetrics = false
let statusResult: { data?: { connected: boolean; discovering?: boolean }; error?: Error }
let usageResult: { data?: PrometheusPVCUsage; error?: Error }
vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  usePrometheusPVCUsage: () => usageResult,
  usePrometheusStatus: () => statusResult,
  useAutoPromConnect: () => undefined,
  useCloudRole: () => ({ canAtLeast: () => canConfigure, isLoading: roleLoading }),
}))
const { PVCUsageBar } = await import('./PVCUsageBar')
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({
  usePreviousIntegrationSettings: (relevant: boolean) => ({ metrics: relevant && previousMetrics }),
  previousSettingsAction: () => ({ label: 'Review previous settings', note: 'Previous metrics settings are available to review for this cluster.' }),
}))
const measured: PrometheusPVCUsage = { namespace: 'demo', name: 'disk', used: 0, capacity: 1024, ratio: 0, hasData: true, status: 'available' }
const render = () => renderToString(<PVCUsageBar namespace="demo" name="disk" />)

describe('PVC usage availability', () => {
  it('offers previous settings only for a missing connection, not missing series or denied access', () => {
    previousMetrics = true
    statusResult = { data: { connected: false } }
    expect(render()).toContain('Review previous settings')
    statusResult = { data: { connected: true } }
    usageResult = { data: { ...measured, hasData: false, status: 'no_series' } }
    expect(render()).not.toContain('Review previous settings')
    statusResult = { error: new ApiError('denied', 403) }
    expect(render()).not.toContain('Review previous settings')
  })
  beforeEach(() => {
    canConfigure = true
    roleLoading = false
    previousMetrics = false
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
  it('keeps older agent unavailable responses from becoming zero gauges', () => {
    usageResult = { data: { ...measured, status: undefined, hasData: false, capacity: 0 } }
    expect(render()).toContain('Usage measurements are unavailable')
    expect(render()).not.toContain('width:')
  })
  it('retains older agent valid measurements', () => {
    usageResult = { data: { ...measured, status: undefined } }
    expect(render()).toContain('width:0%')
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


describe('PVC metrics next step', () => {
  beforeEach(() => {
    canConfigure = true
    roleLoading = false
    statusResult = { data: { connected: true } }
    usageResult = { data: { ...measured, hasData: false, status: 'no_series' } }
  })
  it.each(['no_series', 'invalid_data', 'query_failed'] as const)('offers the existing settings flow for %s', status => {
    usageResult = { data: { ...measured, hasData: false, status } }
    expect(render()).toContain('Configure metrics')
    expect(render()).toContain('type="button"')
  })
  it('offers configuration when disconnected', () => {
    statusResult = { data: { connected: false } }
    expect(render()).toContain('Configure metrics')
  })
  it('offers configuration after a status query failure', () => {
    statusResult = { error: new Error('status failed') }
    expect(render()).toContain('Configure metrics')
  })
  it('offers configuration for older-agent unavailable measurements', () => {
    usageResult = { data: { ...measured, status: undefined, hasData: false } }
    expect(render()).toContain('Configure metrics')
  })
  it('gives a non-owner factual guidance without a locked-settings link', () => {
    canConfigure = false
    expect(render()).not.toContain('Configure metrics')
    expect(render()).toContain('check metrics availability for this volume')
    expect(render()).not.toContain('check the metrics connection')
  })
  it('gives a non-owner connection guidance only when disconnected', () => {
    canConfigure = false
    statusResult = { data: { connected: false } }
    expect(render()).toContain('check the metrics connection for this cluster')
    expect(render()).not.toContain('Configure metrics')
  })
  it('does not offer inaccessible standalone settings in an embedded host', () => {
    const text = renderToString(<NavCustomizationProvider value={{ embedded: true }}><PVCUsageBar namespace="demo" name="disk" /></NavCustomizationProvider>)
    expect(text).not.toContain('Configure metrics')
    expect(text).toContain('check metrics availability')
  })
  it('does not flash an action while permissions load', () => {
    roleLoading = true
    expect(render()).not.toContain('Configure metrics')
    expect(render()).not.toContain('Ask your operator')
  })
  it.each(['usage', 'status'])('guides %s denial toward access rather than connection settings', source => {
    if (source === 'usage') usageResult = { error: new ApiError('forbidden', 403) }
    else statusResult = { error: new ApiError('forbidden', 403) }
    expect(render()).not.toContain('Configure metrics')
    expect(render()).toContain('review your metrics access')
  })
  it.each([true, false])('keeps discovery quiet with a retained usage error (owner=%s)', owner => {
    canConfigure = owner
    statusResult = { data: { connected: false, discovering: true } }
    usageResult = { error: new ApiError('retained query failure', 500) }
    expect(render()).toContain('Discovering Prometheus')
    expect(render()).not.toContain('Configure metrics')
    expect(render()).not.toContain('Ask your operator')
  })
  it('keeps status loading quiet with a retained usage error', () => {
    statusResult = {}
    usageResult = { error: new ApiError('retained query failure', 500) }
    expect(render()).toContain('Checking metrics availability')
    expect(render()).not.toContain('Configure metrics')
  })
  it('retains access guidance when denied during discovery', () => {
    statusResult = { data: { connected: false, discovering: true } }
    usageResult = { error: new ApiError('forbidden', 403) }
    expect(render()).toContain('review your metrics access')
    expect(render()).not.toContain('Configure metrics')
  })
  it.each(['status', 'discovery', 'usage', 'available'])('does not add an action during %s', state => {
    if (state === 'status') statusResult = {}
    if (state === 'discovery') statusResult = { data: { connected: false, discovering: true } }
    if (state === 'usage') usageResult = {}
    if (state === 'available') usageResult = { data: measured }
    expect(render()).not.toContain('Configure metrics')
    expect(render()).not.toContain('Ask your operator')
  })
})
