import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { VeleroWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { VeleroLocations } from './VeleroLocations'
import { VeleroOverview } from './VeleroOverview'
import { hoursAgo, location, workspace } from './velero.fixtures'

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

function render(data: VeleroWorkspaceResponse) {
  return text(renderToStaticMarkup(
    <VeleroLocations data={data} installation={null} onInstallation={() => {}} onInspect={() => {}} inspected={null} />,
  ))
}

describe('Velero storage locations', () => {
  it('shows unread backups and schedules as unknown, never as zero or none', () => {
    const html = render(workspace({
      coverage: { schedules: { state: 'denied' }, backups: { state: 'denied' }, backupStorageLocations: { state: 'full' }, restores: { state: 'full' }, backupRepositories: { state: 'full' } },
      installations: [{ namespace: 'velero', server: { source: 'unread' }, defaultSource: 'unknown' }],
      locations: [location({ backups: null, schedules: null, default: false, defaultSource: 'unknown', frequency: undefined })],
    }))
    expect(html).toContain('Unknown — no access to backups')
    expect(html).toContain('Unknown — no access to schedules')
    expect(html).toContain('frequency unknown')
    expect(html).toContain('default unknown')
    expect(html).not.toMatch(/tabular-nums">0</)
    expect(html).toContain('Radar couldn’t read some of the Velero data on this screen')
  })

  it('counts the backups each location is named by and flags overdue validation', () => {
    const data = workspace({
      installations: [{ namespace: 'velero', server: { source: 'deployment' }, defaultLocation: 'default', defaultSource: 'spec', unassignedBackups: 2 }],
      locations: [location({ validationOverdue: true, attention: true, lastValidation: hoursAgo(2), schedules: [{ name: 'nightly' }, { name: 'weekly', viaDefault: true }] })],
    })
    const html = render(data)
    expect(html).toContain('overdue')
    expect(html).toContain('(as the default)')
    // The usual frequency lives in the overdue badge's tooltip, not on every row.
    expect(html).not.toContain('server flag')

    const overview = text(renderToStaticMarkup(
      <MemoryRouter>
        <VeleroOverview data={data} installation={null} onInstallation={() => {}} onInspect={() => {}} inspected={null} />
      </MemoryRouter>,
    ))
    expect(overview).toContain('Velero last validated this location 2h ago; it validates every 1m.')
  })

  it('says validation is disabled when its frequency is zero', () => {
    const html = render(workspace({ locations: [location({ frequency: { seconds: 0, source: 'spec', disabled: true } })] }))
    expect(html).toContain('validation disabled')
  })
  it('says when Velero never validated a location', () => {
    const html = text(render(workspace({ locations: [location({ neverValidated: true, attention: true, lastValidation: undefined })] })))
    expect(html).toContain('Not validated yet')
  })

  it('says why a location is unavailable in plain words, and marks the default once, under its name', () => {
    const html = render(workspace({
      locations: [
        location(),
        location({ name: 'dr', phase: 'Unavailable', default: false, message: 'rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist' }),
      ],
    }))
    expect(html).toContain('The bucket doesn’t exist')
    // The default location named "default" reads once as its name and once as a note on a line of its own, not as a badge beside it.
    expect(html).not.toMatch(/badge[^"]*">default</)
    expect(html.match(/>default</g)?.length).toBe(1)
    expect(html).toMatch(/<div class="mt-0\.5 text-xs[^"]*"><span[^>]*><span>the default location<\/span>/)
    // On one line, the reason on hover; jsdom has no matchMedia, so this is the narrow table, whose counts share a column.
    expect(html).toMatch(/whitespace-nowrap[^"]*">not restorable</)
    expect(html).toContain('Last checked')
    expect(html).toMatch(/— <span class="text-red[^"]*">failed</)
    expect(html).toContain('completed · ')
  })

  it('marks the default location and folds a long schedule list into a count', () => {
    const schedules = ['a', 'b', 'c', 'd', 'e'].map((name) => ({ name }))
    const html = render(workspace({ locations: [location({ schedules })] }))
    expect(html).toContain('>the default location<')
    expect(html).toContain('and 2 more')
    expect(html).not.toContain('>d<')
  })
})
