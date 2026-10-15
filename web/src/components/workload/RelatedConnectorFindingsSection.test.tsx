import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { RelatedApplicationFindings } from '@skyhook-io/k8s-ui'
import { RelatedConnectorFindingsSection } from './RelatedConnectorFindingsSection'

const cached: RelatedApplicationFindings = {
  source: { group: 'kafka.strimzi.io', kind: 'KafkaConnect', namespace: 'app', name: 'connect' },
  checked: 1, unknown: 0, coverageState: 'cached',
  coverage: 'Checked 1 authorized cached connector snapshots; missing findings do not establish application health.',
}

describe('Related connector findings coverage', () => {
  it('hides missing data and a fully inspected empty cached result', () => {
    expect(renderToString(<RelatedConnectorFindingsSection data={undefined} />)).toBe('')
    expect(renderToString(<RelatedConnectorFindingsSection data={cached} />)).toBe('')
  })
  it('shows unknown snapshots even without a canonical finding', () => {
    const html = renderToString(<RelatedConnectorFindingsSection data={{ ...cached, unknown: 1, coverageState: 'limited', coverage: 'One cached snapshot was unverifiable.' }} />)
    expect(html).toContain('Related connector findings')
    expect(html).toContain('unverifiable')
    expect(html).toContain('status is reported separately')
    expect(html).not.toContain('Operational issues')
  })
  it('keeps unavailable and over-budget coverage visible without inventing issues', () => {
    for (const data of [{ ...cached, coverageState: 'unavailable' as const }, { ...cached, coverageState: 'limited' as const, truncated: true }]) {
      const html = renderToString(<RelatedConnectorFindingsSection data={data} />)
      expect(html).toContain('Related connector findings')
      expect(html).not.toContain('Operational issues')
    }
  })
})
