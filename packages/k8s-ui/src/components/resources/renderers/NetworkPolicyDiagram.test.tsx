// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NetworkPolicyDiagram } from './NetworkPolicyDiagram'
import { largePolicySpec } from '../../../utils/network-policy-fixtures'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | undefined
let element: HTMLDivElement

async function mount() {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  await act(async () => root!.render(<NetworkPolicyDiagram spec={largePolicySpec} namespace="payments" />))
}

async function unmount() {
  await act(async () => root?.unmount())
  element?.remove()
  root = undefined
}

const button = (text: string) => [...element.querySelectorAll('button')].find((b) => b.textContent === text) as HTMLButtonElement
const graphNodes = () => element.querySelectorAll('[data-graph-node]')

beforeEach(() => localStorage.clear())
afterEach(async () => {
  await unmount()
  vi.restoreAllMocks()
})

describe('NetworkPolicyDiagram', () => {
  it('opens in Rules, switches to Graph and remembers the choice', async () => {
    await mount()
    expect(element.textContent).toContain('Rule 1')
    expect(element.textContent).toContain('All pods in payments')
    expect(graphNodes()).toHaveLength(0)
    expect(button('Rules').getAttribute('aria-pressed')).toBe('true')

    await act(async () => button('Graph').click())
    expect(graphNodes().length).toBeGreaterThan(0)
    expect(localStorage.getItem('radar.networkPolicy.view')).toBe('graph')

    await unmount()
    await mount()
    expect(graphNodes().length).toBeGreaterThan(0)
    expect(button('Graph').getAttribute('aria-pressed')).toBe('true')
  })

  it('works when localStorage throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
    await mount()
    expect(element.textContent).toContain('Rule 1')
    await act(async () => button('Graph').click())
    expect(graphNodes().length).toBeGreaterThan(0)
  })
})
