// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DashboardTrafficSummary } from '../../api/client'
import { TrafficSummary, trafficSourceName } from './TrafficSummary'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function render(data: DashboardTrafficSummary | null, onNavigate = vi.fn()) {
  act(() => root.render(<TrafficSummary data={data} onNavigate={onNavigate} />))
  return onNavigate
}

const header = () => container.querySelector('button > div > div')!.textContent
const drawing = () => container.querySelector('svg')!.outerHTML

const observed: DashboardTrafficSummary = { source: 'hubble' }

describe('TrafficSummary', () => {
  it('names no source until the server has detected one', () => {
    render(null)
    expect(header()).toBe('Live Traffic')
    expect(container.textContent).not.toMatch(/Needs a traffic source|Nothing to show/)
  })

  it('names the detected source in the header', () => {
    render(observed)
    expect(header()).toBe('Live TrafficHubble')
  })

  it('draws the same picture with or without a detected source', () => {
    render(null)
    const empty = drawing()
    render(observed)
    expect(drawing()).toBe(empty)
  })

  it('opens Live Traffic from anywhere on the card', () => {
    const onNavigate = render(null)
    act(() => container.querySelector('button')!.click())
    expect(onNavigate).toHaveBeenCalledOnce()
  })

  it('capitalises an unknown source name', () => {
    expect(trafficSourceName('istio')).toBe('Istio')
    expect(trafficSourceName('cilium')).toBe('Cilium')
  })
})
