import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { useCNPGKubectlContext } from './useCNPGKubectlContext'
const state = vi.hoisted(() => ({ context: 'kind-orders', mode: 'local' as string | undefined, embedded: false }))
vi.mock('../../api/client', () => ({ useCapabilities: () => ({ data: state.mode ? { deployment: { mode: state.mode } } : undefined }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: state.embedded }) }))
function Context() { return <span>{useCNPGKubectlContext() ?? 'current kubectl context'}</span> }
it('only supplies a known local kubeconfig context, never a Hub or in-cluster identity', () => {
  const render = () => renderToStaticMarkup(<Context />)
  expect(render()).toContain('kind-orders')
  state.embedded = true; expect(render()).toContain('current kubectl context')
  state.embedded = false; state.mode = 'in-cluster'; expect(render()).toContain('current kubectl context')
  state.mode = undefined; expect(render()).toContain('current kubectl context')
  state.mode = 'local'; state.context = ''; expect(render()).toContain('current kubectl context')
})
