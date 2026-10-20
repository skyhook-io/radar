import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { toneTextClass } from '@skyhook-io/k8s-ui'
import { ParameterRow, cnpgParameterBoolean } from './CNPGParametersInEffect'
import { cnpgParametersView } from './inspectModel'
import type { CNPGParametersResponse } from '../../api/cnpg-inspect'

function render(values: string[]) {
  const reports = values.map((value, i) => ({ pod: `orders-${i + 1}`, value }))
  const response: CNPGParametersResponse = { cluster: { name: 'orders', namespace: 'db', uid: 'u' }, sampledAt: '', permission: { exec: 'allowed' }, state: 'ok', declared: [{ name: 'max_connections', value: '100' }], instances: reports.map(({ pod, value }) => ({ pod, role: 'replica', state: 'ok', settings: [{ name: 'max_connections', value, source: 'configuration file', context: 'postmaster', pendingRestart: false, setByClient: false }] })) }
  const row = cnpgParametersView(response).rows[0]
  return renderToStaticMarkup(<table><tbody><ParameterRow row={row} reports={reports} readPods={reports.map((p) => p.pod)} unreadText="Not reported" /></tbody></table>)
}
it('renders an agreed value once without repeating the summary read set', () => {
  const html = render(['100', '100', '100'])
  expect(html).toContain('font-mono text-theme-text-primary">100</span>')
  expect(html).not.toContain('on orders-1, orders-2, orders-3')
  expect(html).not.toContain('orders-1: 100')
})
it('retains the per-instance list when reported values differ', () => {
  const html = render(['100', '200'])
  expect(html).toContain('orders-1: 100 · orders-2: 200')
  expect(html).toContain(toneTextClass('degraded'))
})

it.each(['on', 'ON', 'true', 't', 'yes', 'y', '1'])('accepts PostgreSQL true spelling %s', (value) => expect(cnpgParameterBoolean(value)).toBe(true))
it.each(['off', 'false', 'f', 'no', 'n', '0'])('accepts PostgreSQL false spelling %s', (value) => expect(cnpgParameterBoolean(value)).toBe(false))
it.each(['o', '', 'truth', '10', 'ofx'])('does not treat %s as a boolean', (value) => expect(cnpgParameterBoolean(value)).toBeUndefined())
it('hints equivalent booleans without inferring drift from different text', () => {
  const row = { name: 'log_truncate_on_rotation', declared: 'false', value: 'off', takesEffect: 'reload', setByClient: false, pendingRestart: [], unreported: [] }
  const renderRow = () => renderToStaticMarkup(<table><tbody><ParameterRow row={row} reports={[{ pod: 'orders-1', value: row.value }]} readPods={['orders-1']} unreadText="Not reported" /></tbody></table>)
  expect(renderRow()).toContain('same as declared')
  row.declared = 'off'
  expect(renderRow()).not.toContain('same as declared')
  row.declared = 'false'
  row.value = 'on'
  expect(renderRow()).not.toContain('same as declared')
  expect(renderRow()).not.toContain('mismatch')
  row.declared = '0'; row.value = '0'
  expect(renderRow()).not.toContain('same as declared')
  row.declared = '1024MB'; row.value = '1GB'
  expect(renderRow()).not.toContain('mismatch')
})
it('names reporting subsets and restart exceptions in the row', () => {
  const row = { name: 'work_mem', declared: '8MB', value: '8MB', takesEffect: 'reload', setByClient: false, pendingRestart: [], unreported: ['orders-2'] }
  const html = renderToStaticMarkup(<table><tbody><ParameterRow row={row} reports={[{ pod: 'orders-1', value: '8MB' }]} readPods={['orders-1', 'orders-2']} unreadText="Not reported" /></tbody></table>)
  expect(html).toContain('on orders-1')
  expect(html).toContain('Not reported by orders-2')
  row.unreported = []; row.pendingRestart = ['orders-1'] as never[]
  expect(renderToStaticMarkup(<table><tbody><ParameterRow row={row} reports={[{ pod: 'orders-1', value: '8MB' }]} readPods={['orders-1']} unreadText="Not reported" /></tbody></table>)).toContain('Restart pending on orders-1')
})
