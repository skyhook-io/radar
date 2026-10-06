// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ClusterActionDialog } from './CNPGClusterActions'
const state = vi.hoisted(() => ({ dialog: {} as any, mutate: vi.fn() }))
vi.mock('../../../api/cnpg', () => ({ useCNPGAction: () => ({ mutate: state.mutate }), useCNPGRuntime: () => ({}), useCNPGClusterCapabilities: () => ({}) }))
vi.mock('../../../api/cnpg-ha', () => ({ useCNPGClusterHA: () => ({}) }))
vi.mock('../useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({}) }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: vi.fn() }) }))
vi.mock('./useCNPGWriteGuard', () => ({ useCNPGWriteGuard: () => ({ node: null, satisfied: true }) }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...(await original<typeof import('@skyhook-io/k8s-ui')>()), ActionConfirmDialog: (props: any) => { state.dialog = props; return <div>{props.children}<button disabled={!!props.disabledReason} onClick={props.onConfirm}>Confirm</button></div> } }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let host: HTMLDivElement, root: ReturnType<typeof createRoot>
afterEach(() => { act(() => root.unmount()); host.remove(); state.mutate.mockClear() })
const facts = { maintenance: {}, backupMethods: [], currentPrimary: 'orders-1', fencedInstances: { all: false, instances: [] }, instances: [{ pod: 'orders-1', podExists: true, ready: true }, { pod: 'orders-2', podExists: false, ready: false }] }
function render(kind: 'fence' | 'unfence' | 'hibernate', extra = {}, initialPod?: string) {
  host = document.createElement('div'); root = createRoot(host)
  act(() => root.render(<ClusterActionDialog kind={kind} caps={{ facts, actions: { [kind]: { allowed: true } }, context: 'kind-orders', uid: 'uid', ...extra } as any} namespace="db" name="orders" initialPod={initialPod} onClose={() => {}} />))
}
it('requires an explicit fencing choice, puts All last and keeps primary typed confirmation', () => {
  render('fence', {}, 'orders-2')
  const select = host.querySelector('select')!
  expect(select.value).toBe('')
  expect(host.querySelector('button')!.disabled).toBe(true)
  expect([...select.options].map((o) => o.textContent)).toEqual(['Choose an instance', 'orders-1 (primary)', 'orders-2', 'All instances — stops service'])
  act(() => { select.value = 'orders-2'; select.dispatchEvent(new Event('change', { bubbles: true })) })
  expect(host.querySelector('button')!.disabled).toBe(false)
  expect(state.dialog.typedConfirmation).toBeUndefined()
  act(() => host.querySelector('button')!.click())
  expect(state.mutate.mock.calls[0][0].request.params.instances).toEqual(['orders-2'])
  act(() => { select.value = '*'; select.dispatchEvent(new Event('change', { bubbles: true })) })
  expect(state.dialog.typedConfirmation).toBe('orders')
  expect(state.dialog.effect).toContain('Writes stop')
})
it('preserves the lift-all fencing default', () => {
  render('unfence', { facts: { ...facts, fencedInstances: { all: true, instances: [] } } })
  expect(host.querySelector('select')!.value).toBe('*')
  expect(host.querySelector('button')!.disabled).toBe(false)
})
it('labels kept PVCs and separates capacity from requested size', () => {
  const list = { available: true, names: [] }
  render('hibernate', { hibernateEffects: { volumes: { available: true, items: [{ name: 'orders-1', capacity: '1Gi', requested: '2Gi' }, { name: 'orders-2', requested: '2Gi' }, { name: 'orders-3' }] }, poolers: list, unsuspendedScheduledBackups: list, databases: list, publications: list, subscriptions: list } })
  expect(host.textContent).toContain('Kept PVCs: orders-1 (capacity 1Gi; requested 2Gi), orders-2 (capacity not reported; requested 2Gi), orders-3 (capacity not reported)')
  expect(state.dialog.typedConfirmation).toBe('orders')
})
