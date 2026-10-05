import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGScheduleActions } from './CNPGScheduleActions'
vi.mock('../../../api/cnpg', () => ({ useCNPGScheduleCapabilities: () => ({ data: { actions: { run: { allowed: false, reason: 'Configure a backup destination on payments first' }, suspend: { allowed: true }, setSchedule: { allowed: true } }, facts: { suspended: false } } }) }))
it('shows the server destination blocker beside the disabled Run now control', () => {
  const html = renderToStaticMarkup(<CNPGScheduleActions namespace="prod" name="payments-nightly" />)
  expect(html).toMatch(/disabled=""[^>]*>Run now/)
  expect(html).toContain('Configure a backup destination on payments first')
  expect(html).toContain('Suspend')
})
