// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ParentRulesList, SchemaFacts, SchemaPathText } from './crd-schema-parts'
import { indexCRDSchema } from '../../../utils/crd-schema'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove() })
function mount(content: React.ReactNode) {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(content))
}
it('renders messages before their expressions, including parent and root rules, and discloses grouped rules', () => {
  const schema = { 'x-kubernetes-validations': [{ rule: 'has(self.spec)', message: 'Spec must exist' }], properties: { spec: {
    'x-kubernetes-validations': Array.from({ length: 5 }, (_, i) => ({ rule: `self.x > ${i}`, message: `Message ${i}` })), properties: { x: { type: 'integer' } },
  } } }
  const node = indexCRDSchema(schema).find(node => node.path.length === 2)!
  mount(<><SchemaFacts schema={{ 'x-kubernetes-validations': [{ rule: 'self > 0', message: 'Positive value' }, { rule: 'self < 10' }] }} /><ParentRulesList node={node} root={schema} /></>)
  const own = host.querySelector('.card-inner')!
  expect(own.firstElementChild?.textContent).toBe('Positive value')
  expect(own.lastElementChild?.textContent).toBe('self > 0')
  expect(host.textContent).toContain('5 rules on spec')
  expect(host.textContent).toContain('1 rule on Document root')
  const rootGroup = Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('1 rule on Document root'))!
  expect(rootGroup.getAttribute('aria-expanded')).toBe('false')
  expect(host.textContent).not.toContain('Spec must exist')
  act(() => rootGroup.click())
  expect(host.textContent).toContain('Spec must exist')
  expect(host.textContent).not.toContain('Message 3')
  const button = Array.from(host.querySelectorAll('button')).find(b => b.textContent === 'Show 2 more')!
  expect(button.getAttribute('aria-expanded')).toBe('false')
  act(() => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  expect(host.textContent).toContain('Message 4')
  for (const card of host.querySelectorAll('.card-inner')) {
    if (card.children.length === 2) expect(card.firstElementChild?.tagName).toBe('P')
  }
})
it('uses labeled code blocks for long constraints and structured defaults while short scalars remain badges', () => {
  mount(<SchemaFacts schema={{ default: { x: 1 }, pattern: 'x'.repeat(41), minimum: 0 }} />)
  expect(Array.from(host.querySelectorAll('pre')).map(p => p.textContent)).toEqual(['{"x":1}', 'x'.repeat(41)])
  expect(Array.from(host.querySelectorAll('pre')).map(p => p.previousElementSibling?.textContent)).toEqual(['default', 'pattern'])
  expect(host.querySelector('.badge-sm')?.textContent).toContain('minimum: 0')
})
it('highlights only direct case-insensitive substrings of the displayed path', () => {
  const node = indexCRDSchema({ properties: { ports: { items: { properties: { name: { type: 'string' } } } } } })[1]
  const both = (query: string) => <><SchemaPathText path={node.path} query={query} part="parent" /><SchemaPathText path={node.path} query={query} part="leaf" /></>
  mount(both('PORTS[]'))
  expect(host.querySelector('mark')?.textContent).toBe('ports[]')
  act(() => root.render(both('nam')))
  expect(host.querySelector('mark')?.textContent).toBe('nam')
  act(() => root.render(both('ports[0].name')))
  expect(host.querySelector('mark')).toBeNull()
  expect(host.textContent).toBe('ports[].name')
})
