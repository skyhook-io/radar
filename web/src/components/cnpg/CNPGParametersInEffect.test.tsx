import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { toneTextClass } from '@skyhook-io/k8s-ui'
import { ParameterRow } from './CNPGParametersInEffect'
import { cnpgParametersView } from './inspectModel'
import type { CNPGParametersResponse } from '../../api/cnpg-inspect'

function render(values: string[]) {
  const reports = values.map((value, i) => ({ pod: `orders-${i + 1}`, value }))
  const response: CNPGParametersResponse = { cluster: { name: 'orders', namespace: 'db', uid: 'u' }, sampledAt: '', permission: { exec: 'allowed' }, state: 'ok', declared: [{ name: 'max_connections', value: '100' }], instances: reports.map(({ pod, value }) => ({ pod, role: 'replica', state: 'ok', settings: [{ name: 'max_connections', value, source: 'configuration file', context: 'postmaster', pendingRestart: false, setByClient: false }] })) }
  const row = cnpgParametersView(response).rows[0]
  return renderToStaticMarkup(<table><tbody><ParameterRow row={row} reports={reports} unreadText="Not reported" /></tbody></table>)
}
it('renders an agreed value once and names its reporting instances quietly', () => {
  const html = render(['100', '100', '100'])
  expect(html).toContain('font-mono text-theme-text-primary">100</span>')
  expect(html).toContain('on orders-1, orders-2, orders-3')
  expect(html).not.toContain('orders-1: 100')
})
it('retains the per-instance list when reported values differ', () => {
  const html = render(['100', '200'])
  expect(html).toContain('orders-1: 100 · orders-2: 200')
  expect(html).toContain(toneTextClass('degraded'))
})
