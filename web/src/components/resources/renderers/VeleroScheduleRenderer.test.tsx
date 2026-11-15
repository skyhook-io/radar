import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { schedule } from '../../velero/velero.fixtures'

const activity = vi.hoisted(() => ({ current: {} as Record<string, unknown> }))

vi.mock('../../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('../../../api/velero', () => ({ useVeleroScheduleActivity: () => activity.current }))
vi.mock('@skyhook-io/k8s-ui/components/resources/renderers/VeleroScheduleRenderer', () => ({
  VeleroScheduleRenderer: ({ activity }: { activity?: unknown }) => <>{activity as never}<p>schedule spec</p></>,
}))

const { VeleroScheduleRenderer } = await import('./VeleroScheduleRenderer')

const data = { metadata: { namespace: 'velero', name: 'nightly', creationTimestamp: new Date().toISOString() } }

describe('Velero Schedule drawer', () => {
  it('says the reading is stale when a refresh fails after a success', () => {
    activity.current = {
      data: { observedAt: new Date().toISOString(), reading: schedule(), coverage: {} },
      isRefetchError: true,
      error: new Error('Service Unavailable'),
      dataUpdatedAt: Date.now() - 10 * 60_000,
    }
    const html = renderToStaticMarkup(<VeleroScheduleRenderer data={data} />)
    expect(html).toContain('Last refresh failed: Service Unavailable')
    expect(html).toContain('showing data from 10m ago')
    expect(html).toContain('Schedule activity')
    // What the schedule is doing comes before what it is configured to do.
    expect(html.indexOf('Schedule activity')).toBeLessThan(html.indexOf('schedule spec'))
  })

  it('shows no stale-data notice while refreshes succeed', () => {
    activity.current = {
      data: { observedAt: new Date().toISOString(), reading: schedule(), coverage: {} },
      isRefetchError: false,
      error: null,
      dataUpdatedAt: Date.now(),
    }
    expect(renderToStaticMarkup(<VeleroScheduleRenderer data={data} />)).not.toContain('Last refresh failed')
  })
})
