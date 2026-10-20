import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, expect, it, vi } from 'vitest'
import { cnpgPortForwardCommand } from '@skyhook-io/k8s-ui'
import { useCNPGKubectlContext } from './useCNPGKubectlContext'
const state = vi.hoisted(() => ({ context: 'kind-orders', mode: 'local' as string | undefined, embedded: false, contexts: [{ name: 'kind-orders', source: 'orders' }] as { name: string; originalName?: string; source?: string }[] | undefined }))
vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: state.mode ? { deployment: { mode: state.mode } } : undefined }),
  useContexts: () => ({ data: state.contexts }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: state.embedded }) }))
function Context() {
  const context = useCNPGKubectlContext()
  return <span>{cnpgPortForwardCommand({ name: 'orders-rw', port: 5432 } as any, 'db', 5432, context?.name)} · {context?.source ?? 'current kubectl context'}</span>
}
const render = () => renderToStaticMarkup(<Context />)
afterEach(() => {
  state.context = 'kind-orders'; state.mode = 'local'; state.embedded = false; state.contexts = [{ name: 'kind-orders', source: 'orders' }]
})
it('only supplies a known local kubeconfig context, never a Hub or in-cluster identity', () => {
  expect(render()).toContain('kind-orders')
  state.embedded = true; expect(render()).toContain('current kubectl context'); expect(render()).not.toContain('--context')
  state.embedded = false; state.mode = 'in-cluster'; expect(render()).toContain('current kubectl context'); expect(render()).not.toContain('--context')
  state.mode = undefined; expect(render()).toContain('current kubectl context')
  state.mode = 'local'; state.context = ''; expect(render()).toContain('current kubectl context')
})
it('uses the in-file name and source for a collision-qualified context', () => {
  state.context = 'prod (staging)'
  state.contexts = [{ name: 'prod', source: 'production' }, { name: 'prod (staging)', originalName: 'prod', source: 'staging' }]
  expect(render()).toContain('kubectl --context prod -n db')
  expect(render()).toContain('staging')
  expect(render()).not.toContain('prod (staging)')
})
it('uses a plain context name when it has no originalName', () => {
  expect(render()).toContain('kubectl --context kind-orders -n db')
  expect(render()).toContain('orders')
})
it('leaves kubectl context selection to the shell when the context cannot be resolved', () => {
  state.context = 'prod (staging)'
  expect(render()).not.toContain('--context')
  expect(render()).toContain('current kubectl context')
  state.contexts = undefined
  expect(render()).not.toContain('--context')
  expect(render()).not.toContain('prod (staging)')
})
