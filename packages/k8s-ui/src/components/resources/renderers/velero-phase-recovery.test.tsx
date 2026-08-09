import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroPhaseValue } from './velero-cells'
import { VeleroBackupRenderer } from './VeleroBackupRenderer'
import { VeleroRestoreRenderer } from './VeleroRestoreRenderer'
import { VeleroScheduleRenderer } from './VeleroScheduleRenderer'
import { VeleroBSLRenderer } from './VeleroBSLRenderer'

// The table deliberately shows a shortened label, and three distinct phases
// collapse onto one of them. That is only defensible while the exact phase
// stays reachable without reading YAML, so these tests guard the drawer.

const backup = (phase: string) => ({
  metadata: { name: 'b', namespace: 'velero' },
  spec: {},
  status: { phase },
})

describe('exact Velero phase stays reachable in the drawer', () => {
  it.each([
    'WaitingForPluginOperationsPartiallyFailed',
    'FinalizingPartiallyFailed',
    'PartiallyFailed',
    'WaitingForPluginOperations',
    'FailedValidation',
    'InProgress',
    'ReadyToStart',
  ])('Backup detail prints the raw %s', (phase) => {
    expect(renderToString(<VeleroBackupRenderer data={backup(phase)} />)).toContain(phase)
  })

  it('Restore detail prints the raw phase', () => {
    const html = renderToString(
      <VeleroRestoreRenderer data={{ metadata: { name: 'r' }, spec: {}, status: { phase: 'WaitingForPluginOperationsPartiallyFailed' } }} />
    )
    expect(html).toContain('WaitingForPluginOperationsPartiallyFailed')
  })

  it('Schedule detail prints the raw phase even when the badge says Paused', () => {
    // Velero leaves .status.phase at Enabled while a schedule is paused, so the
    // badge and the phase disagree by design; the drawer must show both.
    const html = renderToString(
      <VeleroScheduleRenderer data={{ metadata: { name: 's' }, spec: { schedule: '0 1 * * *', paused: true }, status: { phase: 'Enabled' } }} />
    )
    expect(html).toContain('Paused')
    expect(html).toContain('Enabled')
  })

  it('BSL detail prints the raw phase', () => {
    const html = renderToString(
      <VeleroBSLRenderer data={{ metadata: { name: 'bsl' }, spec: { provider: 'aws' }, status: { phase: 'Unavailable' } }} />
    )
    expect(html).toContain('Unavailable')
  })
})

describe('VeleroPhaseValue', () => {
  it('does not repeat the phase when the label already is the phase', () => {
    const html = renderToString(
      <VeleroPhaseValue status={{ text: 'Completed', color: 'x' }} phase="Completed" />
    )
    expect(html.match(/Completed/g)).toHaveLength(1)
  })

  it('shows the phase alongside the label when they differ', () => {
    const html = renderToString(
      <VeleroPhaseValue status={{ text: 'Partially failed', color: 'x' }} phase="FinalizingPartiallyFailed" />
    )
    expect(html).toContain('Partially failed')
    expect(html).toContain('FinalizingPartiallyFailed')
  })

  it('renders nothing extra when the resource has no phase', () => {
    const html = renderToString(
      <VeleroPhaseValue status={{ text: 'Unknown', color: 'x' }} phase="" />
    )
    expect(html.match(/Unknown/g)).toHaveLength(1)
  })
})
