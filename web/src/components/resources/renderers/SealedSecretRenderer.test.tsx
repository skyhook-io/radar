import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ query: {} as any }))
vi.mock('../../../api/sealedSecrets', () => ({ useSealedSecretsController: () => state.query }))

import { SealedSecretRenderer } from './SealedSecretRenderer'

const data = { apiVersion: 'bitnami.com/v1alpha1', kind: 'SealedSecret', metadata: { name: 'db', namespace: 'team' }, status: { conditions: [{ type: 'Synced', status: 'False' }] } }
const draw = () => renderToStaticMarkup(<SealedSecretRenderer data={data} />).replace(/<!-- -->/g, '')

it('passes the controller answer to the shared renderer', () => {
  state.query = { support: 'supported', data: { installed: true, controllerState: 'notFound', keysState: 'unknown' } }
  expect(draw()).toContain('Controller and sealing keys')
})

it('renders no section on a Radar without the endpoint', () => {
  state.query = { support: 'unsupported', isError: true }
  const html = draw()
  expect(html).not.toContain('Controller and sealing keys')
  expect(html).not.toContain('could not be loaded')
})

it('says the status could not be loaded when the request fails', () => {
  state.query = { support: 'supported', isError: true }
  expect(draw()).toContain('could not be loaded')
})
