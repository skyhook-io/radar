// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import yaml from 'yaml'
import { CNPGRestoreDialog } from './CNPGRestoreDialog'

const state = vi.hoisted(() => ({ context: 'kind-demo', editor: {} as any }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGWorkspace: () => ({ isLoading: false, data: { coverage: { backups: { state: 'full' }, objectStores: { state: 'full' } }, objects: { clusters: [{ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: 'pg', namespace: 'db' }, spec: { imageName: 'postgres:17', storage: { size: '20Gi' }, backup: { barmanObjectStore: { destinationPath: 's3://archive' } } } }], backups: [] } } }),
  useCNPGRuntime: () => ({}),
}))
vi.mock('../../../api/cnpg-recovery', () => ({ useCNPGRestoreCapability: () => ({ data: { allowed: true } }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: vi.fn() }) }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('../../shared/CreateResourceDialog', () => ({ CreateResourceDialog: (props: any) => { state.editor = props; return <pre>{props.initialYaml}</pre> } }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({
  ...(await original<typeof import('@skyhook-io/k8s-ui')>()),
  ActionConfirmDialog: ({ onConfirm, confirmLabel, disabledReason, incompleteReason, notes, children }: { onConfirm: () => void; confirmLabel: string; disabledReason?: string; incompleteReason?: string; notes?: string[]; children: ReactNode }) => <div>{notes?.join(' ')}{disabledReason}{children}<button disabled={!!disabledReason || !!incompleteReason} onClick={onConfirm}>{confirmLabel}</button></div>,
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: ReturnType<typeof createRoot>, host: HTMLDivElement
const onClose = vi.fn()
const button = (text: string) => [...document.querySelectorAll('button')].find((item) => item.textContent?.trim() === text)!
const render = () => root.render(<CNPGRestoreDialog namespace="db" entry={{ kind: 'cluster', name: 'pg' }} onClose={onClose} />)
const changeName = (value: string) => act(() => {
  const input = host.querySelector('#cnpg-restore-name')!
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
})
beforeEach(async () => {
  state.context = 'kind-demo'
  onClose.mockClear()
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  await act(async () => render())
})
afterEach(() => { act(() => root.unmount()); host.remove() })

it('retains setup choices and arbitrary YAML edits through Back', () => {
  changeName('custom-restore')
  act(() => button('Review manifest').click())
  const draft = `${state.editor.initialYaml}\n# retained advanced settings\n`
  act(() => state.editor.onBack(draft))
  expect((host.querySelector('#cnpg-restore-name') as HTMLInputElement).value).toBe('custom-restore')
  expect(host.textContent).toContain('Your YAML edits are retained')
  act(() => button('Review manifest').click())
  expect(state.editor.initialYaml).toBe(draft)
  expect(state.editor.initialMode).toBe('create')
  expect(state.editor.lockMode).toBe(true)
  expect(onClose).not.toHaveBeenCalled()
})

it('requires an explicit replacement when changed setup would discard edited YAML', () => {
  act(() => button('Review manifest').click())
  const draft = `${state.editor.initialYaml}\n# keep this edit\n`
  act(() => state.editor.onBack(draft))
  changeName('other-target')
  act(() => button('Review manifest').click())
  expect(document.body.textContent).toContain('Replace edited restore manifest?')
  act(() => button('Keep editing setup').click())
  changeName('pg-restore')
  act(() => button('Review manifest').click())
  expect(state.editor.initialYaml).toBe(draft)
  act(() => state.editor.onBack(draft))
  changeName('other-target')
  act(() => button('Review manifest').click())
  act(() => button('Replace manifest').click())
  expect(yaml.parse(state.editor.initialYaml).metadata.name).toBe('other-target')
  expect(state.editor.initialYaml).not.toContain('keep this edit')
})

it('rebuilds an unedited draft after setup changes and preserves empty edited drafts', () => {
  act(() => button('Review manifest').click())
  act(() => state.editor.onBack(state.editor.initialYaml))
  changeName('new-target')
  act(() => button('Review manifest').click())
  expect(yaml.parse(state.editor.initialYaml).metadata.name).toBe('new-target')
  act(() => state.editor.onBack(''))
  act(() => button('Review manifest').click())
  expect(state.editor.initialYaml).toBe('')
})

it('refuses to reuse the restore draft after a context switch', async () => {
  act(() => button('Review manifest').click())
  state.context = 'kind-other'
  await act(async () => render())
  expect(host.querySelector('pre')).toBeNull()
  expect(host.textContent).toContain('start again in the current context')
  expect(button('Review manifest').disabled).toBe(true)
})
