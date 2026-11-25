import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { LonghornWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { LONGHORN_NOTHING_IN_WHAT_WAS_READ, LONGHORN_NOTHING_NEEDS_ATTENTION, LonghornOverview } from './LonghornOverview'
import { LonghornVolumes } from './LonghornVolumes'
import { volume, workspace } from './longhorn.fixtures'

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')
const props = { onInspect: () => {}, inspected: null }
const overview = (data: LonghornWorkspaceResponse) => text(renderToStaticMarkup(<MemoryRouter><LonghornOverview data={data} {...props} /></MemoryRouter>))

describe('Longhorn overview', () => {
  it('marks every count a lower bound when the read was incomplete, and says nothing found proves less', () => {
    const data = workspace({ coverage: { volumes: { state: 'partial', deniedNamespaces: ['storage'] } } })
    const html = overview({ ...data, counts: { ...data.counts!, complete: false } })
    expect(html.match(/≥/g)).toHaveLength(5)
    expect(html).toContain(LONGHORN_NOTHING_IN_WHAT_WAS_READ)
    expect(html).toContain('no access in storage')
  })

  it('reads unknown, never 0, when no volume was read, and plainly empty when complete', () => {
    const html = overview(workspace({ volumes: [], counts: null, coverage: { volumes: { state: 'denied' } } }))
    expect(html).toContain('unknown — no volume read')
    expect(html).not.toMatch(/>0</)
    expect(overview(workspace())).toContain(LONGHORN_NOTHING_NEEDS_ATTENTION)
  })
})

describe('Longhorn volumes', () => {
  it('shows ? for healthy replicas Radar couldn’t read, never 0', () => {
    const html = text(renderToStaticMarkup(
      <MemoryRouter><LonghornVolumes data={workspace({ volumes: [volume({ healthyReplicas: null, healthyReplicasUnknown: 'engineUnread', use: { state: 'bound', claimWithheld: true } })] })} {...props} /></MemoryRouter>,
    ))
    expect(html).toMatch(/>\?<\/span> \/ 3/)
    expect(html).not.toMatch(/>0 \/ 3/)
    expect(html).toContain('a claim you can’t read')
  })

  it('keeps a reported actual size when the requested size is not reported', () => {
    const html = text(renderToStaticMarkup(
      <MemoryRouter><LonghornVolumes data={workspace({ volumes: [volume({ size: undefined, actualSize: 2 * 1024 ** 3 })] })} {...props} /></MemoryRouter>,
    ))
    expect(html).toContain('not reported')
    expect(html).toContain('2 GB used')
  })
})
