// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { CustomResourceDefinitionRenderer } from './CustomResourceDefinitionRenderer'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
const schema = { type: 'object', properties: { spec: { type: 'object', description: 'Specification', required: ['field0'], properties: Object.fromEntries(Array.from({ length: 205 }, (_, i) => [`field${i}`, { type: 'string', description: `Description ${i}`, format: 'uri', 'x-kubernetes-validations': [{ rule: 'self.size() > 0', message: 'Must be set' }], oneOf: [{ required: ['a'] }, { required: ['b'] }] }])) } } }
const data = { apiVersion: 'apiextensions.k8s.io/v1', metadata: { uid: 'a', resourceVersion: '1' }, spec: { group: 'example.io', scope: 'Namespaced', names: { kind: 'Widget', plural: 'widgets', singular: 'widget' }, versions: [{ name: 'v1beta1', served: true, storage: true, schema: { openAPIV3Schema: { properties: { old: { type: 'string' } } } } }, { name: 'v1', served: true, schema: { openAPIV3Schema: schema } }, { name: 'v2', served: false }] } }
afterEach(() => { act(() => root?.unmount()); host?.remove() })
function mount(resource: unknown = data, onViewInstances?: () => void) {
  if (!host) { host = document.createElement('div'); document.body.append(host); root = createRoot(host) }
  act(() => root.render(<CustomResourceDefinitionRenderer data={resource} onViewInstances={onViewInstances} />))
}
function start(onViewInstances?: () => void) { host = document.createElement('div'); document.body.append(host); root = createRoot(host); mount(data, onViewInstances) }
function click(text: string) { const button = Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes(text)); expect(button).toBeDefined(); act(() => button!.click()) }
function filter(value: string) { const input = host.querySelector('input')!; act(() => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })) }) }
const versionButton = () => host.querySelector<HTMLButtonElement>('button[aria-haspopup="listbox"]')
function openVersions() { act(() => versionButton()!.click()); return Array.from(document.querySelectorAll<HTMLButtonElement>('[role="option"]')) }
function version(value: string) { const option = openVersions().find(o => o.textContent === value || o.textContent?.match(new RegExp(`^${value}(storage|not served|schema)`))); expect(option).toBeDefined(); act(() => option!.click()) }

it('shows definition facts, highest served version and optional Instances action', () => {
  const view = vi.fn(); start(view)
  expect(host.textContent).toContain('Widget')
  expect(host.textContent).toContain('example.io')
  expect(host.textContent).toContain('Namespaced')
  expect(versionButton()?.getAttribute('aria-label')).toBe('Schema version: v1')
  const options = openVersions()
  expect(options.map(o => o.textContent)).toEqual(['v1beta1storage', 'v1', 'v2not served · schema not received'])
  act(() => options[1].click())
  click('View Widget resources'); expect(view).toHaveBeenCalledOnce()
  mount(data); expect(host.textContent).not.toContain('View Widget resources')
})
it('lazily expands at most 100 children, reveals details and pages siblings', () => {
  start(); expect(host.textContent).not.toContain('field0')
  click('spec'); expect(host.textContent).toContain('field99'); expect(host.textContent).not.toContain('field100')
  click('field0'); expect(host.textContent).toContain('format: uri'); expect(host.textContent).toContain('self.size() > 0'); expect(host.textContent).toContain('Must be set'); expect(host.textContent).toContain('One of: a · b')
  click('Show 100 more'); expect(host.textContent).toContain('field199'); expect(host.textContent).not.toContain('field200')
  click('Show 5 more'); expect(host.textContent).toContain('field204')
})
it('caps filter results and resets filter and expansion on version and CRD changes', () => {
  start(); filter('SPEC.FIELD')
  expect(host.textContent).toContain('205 matching fields — showing 200')
  expect(host.textContent).toContain('spec.field0')
  expect(host.textContent).not.toContain('spec.field200')
  version('v1beta1'); expect(host.querySelector('input')?.value).toBe(''); expect(host.textContent).toContain('old')
  version('v1'); click('spec'); expect(host.textContent).toContain('field0')
  version('v1beta1'); version('v1'); expect(host.textContent).not.toContain('field0')
  filter('field1'); mount({ ...data, metadata: { uid: 'b', resourceVersion: '1' } }); expect(host.querySelector('input')?.value).toBe('')
  filter('field1'); mount({ ...data, metadata: { uid: 'b', resourceVersion: '2' } }); expect(host.querySelector('input')?.value).toBe('')
})
it('reports missing schemas honestly per version and for a stripped object', () => {
  start(); version('v2'); expect(host.textContent).toContain("Radar didn't receive a schema for this version")
  mount({ ...data, spec: { ...data.spec, versions: [{ name: 'v1', served: false }] } })
  expect(host.textContent).toContain("Radar didn't receive this definition's schema. The YAML tab shows the object as returned.")
  expect(host.textContent).not.toContain('View Widget resources')
})

it('shows preserve-unknown and int-or-string notes without replacing the declared type', () => {
  start()
  mount({ ...data, spec: { ...data.spec, versions: [{ name: 'v1', served: true, schema: { openAPIV3Schema: { properties: { extra: { type: 'object', 'x-kubernetes-preserve-unknown-fields': true }, port: { 'x-kubernetes-int-or-string': true } } } } }] } })
  expect(host.textContent).toContain('extraobject')
  click('extra'); expect(host.textContent).toContain('Accepts fields not declared in this schema.')
  click('port'); expect(host.textContent).toContain('Accepts an integer or a string.')
})
