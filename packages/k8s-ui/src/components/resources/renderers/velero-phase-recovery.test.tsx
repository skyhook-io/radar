import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroPhaseValue } from './velero-cells'
import { VeleroBackupRenderer } from './VeleroBackupRenderer'
import { VeleroRestoreRenderer } from './VeleroRestoreRenderer'
import { VeleroScheduleRenderer } from './VeleroScheduleRenderer'
import { VeleroBSLRenderer } from './VeleroBSLRenderer'

// The table deliberately shows a shortened label, and three distinct phases
// collapse onto one of them. That is only defensible while the exact phase
// stays reachable without reading YAML, so these tests guard the drawer. A
// phase that is the label without its spaces adds nothing and is left out.

const backup = (phase: string) => ({
  metadata: { name: 'b', namespace: 'velero' },
  spec: {},
  status: { phase },
})

describe('exact Velero phase stays reachable in the drawer', () => {
  it.each([
    'WaitingForPluginOperationsPartiallyFailed',
    'FinalizingPartiallyFailed',
    'WaitingForPluginOperations',
    'FailedValidation',
  ])('Backup detail prints the raw %s', (phase) => {
    expect(renderToString(<VeleroBackupRenderer data={backup(phase)} />)).toContain(phase)
  })

  it.each([
    ['PartiallyFailed', 'Partially failed'],
    ['InProgress', 'In progress'],
  ])('Backup detail leaves out %s under its own label', (phase, label) => {
    const html = renderToString(<VeleroBackupRenderer data={backup(phase)} />)
    expect(html).toContain(label)
    expect(html).not.toContain(`>${phase}<`)
  })

  it('Restore detail prints the raw phase', () => {
    const html = renderToString(
      <VeleroRestoreRenderer data={{ metadata: { name: 'r' }, spec: {}, status: { phase: 'WaitingForPluginOperationsPartiallyFailed' } }} />
    )
    expect(html).toContain('WaitingForPluginOperationsPartiallyFailed')
  })

  it('Schedule detail shows the phase only when it says more than "accepted"', () => {
    // Velero leaves .status.phase at Enabled while a schedule is paused. Enabled
    // only means Velero accepted the schedule, so it is left out; a paused
    // schedule says Paused, and a rejected one shows its phase.
    const paused = renderToString(
      <VeleroScheduleRenderer data={{ metadata: { name: 's' }, spec: { schedule: '0 1 * * *', paused: true }, status: { phase: 'Enabled' } }} />
    )
    expect(paused).toContain('Paused')
    expect(paused).not.toContain('Enabled')
    const rejected = renderToString(
      <VeleroScheduleRenderer data={{ metadata: { name: 's' }, spec: { schedule: 'bad' }, status: { phase: 'FailedValidation' } }} />
    )
    expect(rejected).toContain('FailedValidation')
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
