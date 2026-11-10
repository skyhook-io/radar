// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import yaml from 'yaml'
import { CNPGRestoreDialog } from './CNPGRestoreDialog'

const state = vi.hoisted(() => ({ context: 'kind-demo', editor: {} as any, toast: vi.fn() }))
vi.mock('../../../api/cnpg', () => ({
  useCNPGWorkspace: () => ({ isLoading: false, data: { coverage: { backups: { state: 'full' }, objectStores: { state: 'full' } }, objects: { clusters: [{ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: 'pg', namespace: 'db' }, spec: { imageName: 'postgres:17', storage: { size: '20Gi' }, backup: { barmanObjectStore: { destinationPath: 's3://archive' } } } }], backups: [] } } }),
  useCNPGRuntime: () => ({}),
}))
vi.mock('../../../api/client', () => ({ useResources: () => ({ data: [] }) }))
vi.mock('../../../api/cnpg-recovery', () => ({ useCNPGRestoreCapability: () => ({ data: { allowed: true } }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: state.toast }) }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('../../shared/CreateResourceDialog', () => ({ CreateResourceDialog: (props: any) => { state.editor = props; return <pre>{props.initialYaml}</pre> } }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({
  ...(await original<typeof import('@skyhook-io/k8s-ui')>()),
  ActionConfirmDialog: ({ onConfirm, onBack, backLabel, confirmLabel, disabledReason, incompleteReason, notes, children }: { onConfirm: () => void; onBack?: () => void; backLabel?: string; confirmLabel: string; disabledReason?: string; incompleteReason?: string; notes?: string[]; children: ReactNode }) => <div>{notes?.join(' ')}{disabledReason}{children}{onBack && <button onClick={onBack}>{backLabel}</button>}<button disabled={!!disabledReason || !!incompleteReason} onClick={onConfirm}>{confirmLabel}</button></div>,
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: ReturnType<typeof createRoot>, host: HTMLDivElement
const onClose = vi.fn()
const button = (text: string) => [...document.querySelectorAll('button')].find((item) => item.textContent?.trim() === text)!
const render = () => root.render(<CNPGRestoreDialog namespace="db" entry={{ kind: 'cluster', name: 'pg' }} onClose={onClose} />)
const changeField = (field: string, value: string) => act(() => {
  const input = host.querySelector(`#cnpg-restore-${field}`)!
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
})
const changeName = (value: string) => changeField('name', value)
const selectTarget = (label: string) => act(() => [...host.querySelectorAll('label')].find((item) => item.textContent?.trim() === label)!.querySelector<HTMLInputElement>('input')!.click())
beforeEach(async () => {
  state.context = 'kind-demo'
  onClose.mockClear()
  state.toast.mockClear()
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  await act(async () => render())
  act(() => button('Continue to new Cluster').click())
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
  act(() => button('Back to recovery point').click())
  selectTarget('A point in time')
  changeField('time', '2026-10-01T12:00')
  act(() => button('Continue to new Cluster').click())
  act(() => button('Review manifest').click())
  expect(document.body.textContent).toContain('Replace edited restore manifest?')
  act(() => button('Keep editing setup').click())
  act(() => button('Back to recovery point').click())
  selectTarget('The latest archived WAL')
  act(() => button('Continue to new Cluster').click())
  act(() => button('Review manifest').click())
  expect(state.editor.initialYaml).toBe(draft)
  act(() => state.editor.onBack(draft))
  act(() => button('Back to recovery point').click())
  selectTarget('A point in time')
  act(() => button('Continue to new Cluster').click())
  act(() => button('Review manifest').click())
  act(() => button('Replace manifest').click())
  expect(yaml.parse(state.editor.initialYaml).spec.bootstrap.recovery.recoveryTarget.targetTime).toContain('2026-10-01T12:00:00')
  expect(state.editor.initialYaml).not.toContain('keep this edit')
})

it('rebuilds an unedited draft after setup changes and preserves empty edited drafts', () => {
  act(() => button('Review manifest').click())
  act(() => state.editor.onBack(state.editor.initialYaml))
  changeName('new-target')
  act(() => button('Review manifest').click())
  expect(yaml.parse(state.editor.initialYaml).metadata.name).toBe('new-target')
  act(() => state.editor.onBack(''))
  act(() => button('Continue editing current YAML').click())
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


it('projects supported YAML inputs and preserves other edits when target fields change', () => {
  act(() => button('Review manifest').click())
  const doc = yaml.parseDocument(state.editor.initialYaml, { version: '1.1' })
  doc.setIn(['spec', 'storage', 'size'], '40Gi')
  doc.setIn(['spec', 'postgresql'], { parameters: { max_connections: '250' } })
  const draft = `${doc.toString()}\n# preserve this note\n`
  expect(yaml.parse(draft).spec.postgresql).toEqual({ parameters: { max_connections: '250' } })
  act(() => state.editor.onBack(draft))
  expect((host.querySelector('#cnpg-restore-size') as HTMLInputElement).value).toBe('40Gi')
  changeName('different-target')
  changeField('instances', '2')
  act(() => button('Review manifest').click())
  const manifest = yaml.parse(state.editor.initialYaml)
  expect(manifest.metadata.name).toBe('different-target')
  expect(manifest.spec.instances).toBe(2)
  expect(manifest.spec.storage.size).toBe('40Gi')
  expect(manifest.spec.postgresql.parameters.max_connections).toBe('250')
  expect(state.editor.initialYaml).toContain('preserve this note')
})

it('does not claim an edited initdb manifest started a restore', () => {
  act(() => button('Review manifest').click())
  const doc = yaml.parseDocument(state.editor.initialYaml)
  doc.setIn(['spec', 'bootstrap'], { initdb: {} })
  const created = { kind: 'Cluster', apiVersion: 'postgresql.cnpg.io/v1', namespace: 'db', name: 'pg-restore' }
  act(() => state.editor.onCreated(created, doc.toString()))
  expect(state.toast).toHaveBeenCalledWith('Cluster pg-restore created', 'Review its current state on the Cluster page.', expect.objectContaining({ label: 'Open Cluster' }))
})


it('puts recovery decisions first and keeps target inputs when stepping back', () => {
  changeName('chosen-target')
  act(() => button('Back to recovery point').click())
  expect(host.querySelector('#cnpg-restore-name')).toBeNull()
  expect(host.textContent).toContain('What the source holds')
  selectTarget('A point in time')
  expect(button('Continue to new Cluster').disabled).toBe(true)
  changeField('time', '2026-10-01T12:00')
  act(() => button('Continue to new Cluster').click())
  expect((host.querySelector('#cnpg-restore-name') as HTMLInputElement).value).toBe('chosen-target')
  expect(host.textContent).toContain('Recover to 2026-10-01 12:00:00 UTC')
})
