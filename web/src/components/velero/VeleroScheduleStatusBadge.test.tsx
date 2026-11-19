import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { schedule } from './velero.fixtures'

const state = vi.hoisted(() => ({ support: 'supported' as string, data: undefined as unknown }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../api/velero', () => ({ useVeleroScheduleActivity: () => ({ data: state.data }) }))

const { VeleroScheduleStatusBadge } = await import('./VeleroScheduleStatusBadge')

const enabled = { apiVersion: 'velero.io/v1', kind: 'Schedule', metadata: { namespace: 'velero', name: 'nightly' }, spec: { schedule: '0 1 * * *' }, status: { phase: 'Enabled' } }

describe('Velero Schedule header chip', () => {
  it('says what the schedule is doing, not that Velero accepted it', () => {
    state.support = 'supported'
    state.data = { reading: schedule({ state: 'late', attention: true }) }
    const html = renderToStaticMarkup(<VeleroScheduleStatusBadge resource={enabled} />)
    expect(html).toContain('Late')
    expect(html).not.toContain('Enabled')
  })

  it('falls back to the phase until the reading arrives, and on an older Radar', () => {
    state.data = undefined
    expect(renderToStaticMarkup(<VeleroScheduleStatusBadge resource={enabled} />)).toContain('Enabled')
    state.support = 'unsupported'
    expect(renderToStaticMarkup(<VeleroScheduleStatusBadge resource={enabled} />)).toContain('Enabled')
  })
})
