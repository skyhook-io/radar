// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CRDSchemaBrowser } from './CRDSchemaBrowser'
import { resourceToYaml } from '../../../utils/yaml'

vi.mock('../../ui/CodeViewer', () => ({ CodeViewer: (props: any) => <pre data-theme={props.theme} data-language={props.language} data-copy={props.showCopyButton}>{props.code}</pre> }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
const schema = { type: 'object', 'x-kubernetes-validations': [{ rule: 'has(self.spec)', message: 'Spec is required' }], properties: { spec: {
  type: 'object', description: 'Settings', required: ['name'], 'x-kubernetes-validations': [{ rule: 'self.name != ""', message: 'Choose a name' }], properties: {
    name: { type: 'string', description: 'A display name', minLength: 1, 'x-kubernetes-validations': [{ rule: 'self != "bad"', message: 'Use a good name' }] },
    ports: { type: 'array', items: { properties: { port: { type: 'integer', minimum: 1 } } } },
  },
} } }
const data = { metadata: { uid: 'a', name: 'widgets.example.io' }, spec: { names: { kind: 'Widget' }, versions: [
  { name: 'v1', served: true, schema: { openAPIV3Schema: schema } },
  { name: 'v1beta1', served: true, schema: { openAPIV3Schema: { properties: { spec: { properties: { name: { type: 'integer' } } } } } } },
  { name: 'v1alpha1', served: true, schema: { openAPIV3Schema: { properties: { other: { type: 'boolean' } } } } },
  { name: 'v2', served: false },
] } }
afterEach(() => { act(() => root?.unmount()); host?.remove() })
function mount(resource = data, onCopy?: (text: string) => void) {
  if (!host?.isConnected) { host = document.createElement('div'); document.body.append(host); root = createRoot(host) }
  act(() => root.render(<CRDSchemaBrowser data={resource} theme="light" onCopy={onCopy} />))
}
function click(text: string, within: Element = host) {
  const button = Array.from(within.querySelectorAll('button')).find(b => b.textContent === text || b.getAttribute('aria-label') === text)
  expect(button, text).toBeDefined(); act(() => button!.click())
}
function filter(value: string) {
  const input = host.querySelector('input')!
  act(() => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })) })
}
function version(name: string) {
  act(() => host.querySelector<HTMLButtonElement>('button[aria-haspopup="listbox"]')!.click())
  const option = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="option"]')).find(b => b.textContent?.startsWith(name))!
  act(() => option.click())
}
const detail = () => host.querySelector('[aria-label="Schema field detail"]')!
const outline = () => host.querySelector('[aria-label="Schema outline"]')!

it('selects independently of expansion, shows facts and parent rules, and follows child fields', () => {
  const copy = vi.fn(); mount(data, copy)
  expect(detail().textContent).toContain('Select a field')
  expect(outline().textContent).not.toContain('ports')
  click('specobject', outline())
  expect(detail().textContent).toContain('Settings')
  expect(detail().textContent).toContain('Choose a name')
  expect(detail().textContent).toContain('Spec is required')
  expect(outline().textContent).not.toContain('ports')
  click('namestringrequired', detail())
  expect(detail().textContent).toContain('spec.name')
  expect(detail().textContent).toContain('minLength: 1')
  expect(detail().textContent).toContain('Use a good name')
  expect(detail().textContent).toContain('Rules on parent fields')
  expect(detail().textContent).toContain('Choose a name')
  click('Copy field path', detail()); expect(copy).toHaveBeenCalledWith('spec.name')
  click('Expand spec', outline())
  expect(outline().querySelector('[aria-current="true"]')?.textContent).toContain('name')
})
it('filters pasted paths, selects results and highlights only direct matches', () => {
  mount(); filter('spec.ports[0].port')
  expect(host.textContent).toContain('1 matching field')
  expect(outline().querySelector('mark')).toBeNull()
  click('spec.ports[].portinteger', outline())
  expect(detail().textContent).toContain('spec.ports[].port')
  expect(detail().textContent).toContain('minimum: 1')
  filter('PORTS[]')
  expect(outline().querySelector('mark')?.textContent).toBe('ports[]')
  click('Clear field filter'); expect(host.querySelector('input')?.value).toBe('')
})
it('retains selection across versions only while the path exists, and resets on another CRD', () => {
  mount(); filter('spec.name'); click('spec.namestringrequired', outline())
  version('v1beta1')
  expect(detail().textContent).toContain('spec.name')
  expect(detail().textContent).toContain('integer')
  expect(host.querySelector('input')?.value).toBe('spec.name')
  version('v1alpha1'); expect(detail().textContent).toContain('Select a field')
  version('v1'); expect(detail().textContent).toContain('Select a field')
  click('Raw'); mount({ ...data, metadata: { ...data.metadata, uid: 'b' } })
  expect(host.querySelector('input')?.value).toBe('')
  expect(host.querySelector('button[aria-pressed="true"]')?.textContent).toBe('Fields')
  expect(detail().textContent).toContain('Select a field')
})
it('shows the version schema as YAML with the existing raw viewer and honest missing-schema copy', () => {
  mount(); click('Raw')
  expect(host.querySelector('input[aria-label="Filter fields"]')).toBeNull()
  const raw = host.querySelector('pre')!
  expect(raw.textContent).toBe(resourceToYaml(schema))
  expect(raw.dataset).toMatchObject({ theme: 'light', language: 'yaml', copy: 'true' })
  version('v2')
  expect(host.textContent).toContain("Radar didn't receive a schema for this version")
  expect(host.querySelector('pre')).toBeNull()
})
it('copies a path through the clipboard and handles clipboard rejection', async () => {
  const writeText = vi.fn().mockRejectedValue(new Error('denied'))
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
  mount(); click('specobject', outline())
  await act(async () => click('Copy field path', detail()))
  expect(writeText).toHaveBeenCalledWith('spec')
})
