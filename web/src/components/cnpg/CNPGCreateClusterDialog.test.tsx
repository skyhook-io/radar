// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import yaml from 'yaml'
import { CNPGCreateClusterDialog } from './CNPGCreateClusterDialog'

const state = vi.hoisted(() => ({ context: 'kind-demo', namespaces: { data: [{ name: 'db' }, { name: 'prod' }], isError: false }, editor: {} as any }))
vi.mock('../../api/client', () => ({ useNamespaces: () => state.namespaces }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: state.context } }) }))
vi.mock('../shared/CreateResourceDialog', () => ({ CreateResourceDialog: (props: any) => { state.editor = props; return <pre>{props.initialYaml}</pre> } }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: ReturnType<typeof createRoot>, host: HTMLDivElement
const button = (text: string) => [...document.querySelectorAll('button')].find((item) => item.textContent?.trim() === text)!

beforeEach(() => {
  state.context = 'kind-demo'
  state.namespaces = { data: [{ name: 'db' }, { name: 'prod' }], isError: false }
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(() => { act(() => root.unmount()); host.remove() })

it.each([{ namespaces: [] }, { namespaces: ['db', 'prod'] }])('requires a target namespace for scope $namespaces', async ({ namespaces }) => {
  await act(async () => root.render(<CNPGCreateClusterDialog namespaces={namespaces} onClose={vi.fn()} onCreated={vi.fn()} />))
  expect(document.querySelector('input')!.value).toBe('')
  expect(button('Edit manifest').disabled).toBe(true)
})

it('seeds the selected namespace and opens strict create for the exact CNPG identity', async () => {
  const onCreated = vi.fn()
  await act(async () => root.render(<CNPGCreateClusterDialog namespaces={['prod']} onClose={vi.fn()} onCreated={onCreated} />))
  expect(document.querySelector('input')!.value).toBe('prod')
  act(() => button('Edit manifest').click())
  expect(yaml.parse(state.editor.initialYaml)).toMatchObject({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { namespace: 'prod' } })
  expect(state.editor.initialMode).toBe('create')
  expect(state.editor.lockMode).toBe(true)
  state.editor.onCreated({ apiVersion: 'cluster.x-k8s.io/v1beta1', kind: 'Cluster', namespace: 'prod', name: 'capi' })
  expect(onCreated).not.toHaveBeenCalled()
  state.editor.onCreated({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', namespace: 'prod', name: 'pg' })
  expect(onCreated).toHaveBeenCalledWith({ kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'prod', name: 'pg' })
})

it('allows an explicit namespace when suggestions are unavailable', async () => {
  state.namespaces = { data: [], isError: true }
  await act(async () => root.render(<CNPGCreateClusterDialog namespaces={[]} onClose={vi.fn()} onCreated={vi.fn()} />))
  expect(document.body.textContent).toContain('Namespace suggestions could not be read')
  act(() => {
    const input = document.querySelector('input')!
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'restricted')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  act(() => button('Edit manifest').click())
  expect(yaml.parse(state.editor.initialYaml).metadata.namespace).toBe('restricted')
})

it('does not carry a prepared manifest into a different context', async () => {
  const render = () => root.render(<CNPGCreateClusterDialog namespaces={['db']} onClose={vi.fn()} onCreated={vi.fn()} />)
  await act(async () => render())
  act(() => button('Edit manifest').click())
  state.context = 'kind-other'
  await act(async () => render())
  expect(host.querySelector('pre')).toBeNull()
  expect(document.body.textContent).toContain('The Kubernetes context changed')
  expect(button('Edit manifest').disabled).toBe(true)
})
