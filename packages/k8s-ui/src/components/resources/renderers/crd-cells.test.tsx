// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { CRDVersionsCell } from './crd-cells'

it('sorts served versions, shows two complete tokens and summarizes the rest', () => {
  const container = document.createElement('div')
  container.innerHTML = renderToStaticMarkup(<CRDVersionsCell versions={[
    { name: 'v2alpha1', served: true, deprecated: true }, { name: 'v1beta1', served: true },
    { name: 'v1', served: true, storage: true }, { name: 'v2', served: true }, { name: 'v3', served: false },
  ]} />)
  expect(Array.from(container.querySelectorAll('.font-mono')).map(el => el.textContent)).toEqual(['v2', 'v1'])
  expect(container.textContent).toBe('v2v1+2')
  expect(container.querySelector('[class*="overflow-hidden"]')).toBeNull()
  for (const token of container.querySelectorAll('.font-mono')) {
    expect(token.parentElement?.classList.contains('shrink-0')).toBe(true)
    expect(token.parentElement?.classList.contains('whitespace-nowrap')).toBe(true)
  }
  container.innerHTML = renderToStaticMarkup(<CRDVersionsCell versions={[
    { name: 'v1beta2', served: true, deprecated: true }, { name: 'v1beta1', served: true, deprecated: true },
  ]} />)
  expect(container.textContent).toBe('v1beta2deprecated+1')
  container.innerHTML = renderToStaticMarkup(<CRDVersionsCell versions={[]} />)
  expect(container.textContent).toBe('None served')
})
