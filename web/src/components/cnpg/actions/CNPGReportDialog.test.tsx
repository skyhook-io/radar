import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { CNPGReportDialog } from './CNPGReportDialog'
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), ActionConfirmDialog: ({ notes, children }: any) => <div>{notes.map((note: string) => <p key={note}>{note}</p>)}{children}</div> }))
vi.mock('../../../api/client', () => ({ useRadarFeature: () => ({ guard: vi.fn() }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('separates excluded Secret contents from best-effort log redaction and review', () => {
  const html = renderToStaticMarkup(<CNPGReportDialog namespace="db" name="orders" onClose={() => {}} />)
  expect(html).toContain('Secret contents are never included.')
  expect(html).toContain('Included logs are redacted for recognisable secrets on a best-effort basis; review them before sharing.')
  expect(html).not.toContain('Secret values are never included')
})
