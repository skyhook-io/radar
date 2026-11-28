// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NetworkPolicyGroupList } from './NetworkPolicyGroupList'
import { summarizeNetworkPolicy, type PolicyGroup } from '../../../utils/network-policy-summary'
import { largePolicySpec } from '../../../utils/network-policy-fixtures'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | undefined
let element: HTMLDivElement

async function mount(group: PolicyGroup) {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  await act(async () => root!.render(<NetworkPolicyGroupList group={group} />))
}

afterEach(async () => {
  await act(async () => root?.unmount())
  element?.remove()
  root = undefined
})

const chips = () => element.querySelectorAll('[data-chip]')
const button = (text: string) => [...element.querySelectorAll('button')].find((b) => b.textContent === text)

function cidrGroup(count: number): PolicyGroup {
  // Step by 2 so nothing collapses into a run.
  const raw = Array.from({ length: count }, (_, i) => `10.${Math.floor(i / 120)}.${(i % 120) * 2}.0/32`)
  return { kind: 'ipBlocks', title: `${count} IP ranges`, peerType: 'cidr', raw, items: raw.map((r) => ({ label: r, members: [r] })) }
}

const largeIpGroup = () => summarizeNetworkPolicy(largePolicySpec, 'payments').ingress.rules[2].groups[0]

async function typeFilter(text: string) {
  const input = element.querySelector('input[type=search]') as HTMLInputElement
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('NetworkPolicyGroupList', () => {
  it('shows at most 6 chips and a "+N more" button', async () => {
    await mount(cidrGroup(20))
    expect(chips()).toHaveLength(6)
    await act(async () => button('+14 more')!.click())
    expect(chips()).toHaveLength(20)
    expect(button('Show less')).toBeDefined()
  })

  it('renders a 5000-member group with 6 chips', async () => {
    await mount(cidrGroup(5000))
    expect(chips()).toHaveLength(6)
  })

  it('shows the filter only above 12 raw entries', async () => {
    await mount(cidrGroup(12))
    expect(element.querySelector('input[type=search]')).toBeNull()
    await act(async () => root!.unmount())
    element.remove()
    await mount(cidrGroup(13))
    expect(element.querySelector('input[type=search]')).not.toBeNull()
  })

  it('filters raw members, not the collapsed label', async () => {
    await mount(largeIpGroup())
    await typeFilter('10.42.250.')
    expect(chips()).toHaveLength(1)
    expect(chips()[0].textContent).toBe('10.42.250.0/32')
    await typeFilter('nomatch')
    expect(element.textContent).toContain('No matches')
  })

  it('copies the raw values one per line', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const group = largeIpGroup()
    await mount(group)
    await act(async () => (element.querySelector('button[aria-label="Copy list"]') as HTMLButtonElement).click())
    expect(writeText).toHaveBeenCalledWith(group.raw.join('\n'))
    expect(writeText.mock.calls[0][0].split('\n')).toHaveLength(261)
    vi.unstubAllGlobals()
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  })

  it('offers no copy for a single value', async () => {
    await mount({ kind: 'namespaces', title: 'Any pod in analytics', peerType: 'namespace', raw: ['analytics'], items: [{ label: 'analytics', members: ['analytics'] }] })
    expect(element.querySelector('button[aria-label="Copy list"]')).toBeNull()
  })

  it('lets chip labels wrap instead of widening the drawer', async () => {
    await mount(largeIpGroup())
    for (const chip of chips()) {
      expect(chip.querySelector('[data-chip-label]')!.classList.contains('break-all')).toBe(true)
    }
  })

  it('names the whole range of a collapsed run for screen readers', async () => {
    await mount(largeIpGroup())
    const run = [...chips()].find((c) => c.textContent?.includes('10.42.0–255.0/32'))
    expect(run!.querySelector('.sr-only')!.textContent).toBe('10.42.0.0/32 … 10.42.255.0/32')
  })

  it('lets a long chip detail wrap too', async () => {
    await mount({ kind: 'podsInNamespaces', title: 'Pods in selected namespaces', peerType: 'combined', raw: ['x'], items: [{ label: 'app=x', detail: `in name In (${'ns,'.repeat(40)})`, members: ['x'] }] })
    const detail = element.querySelector('[data-chip-detail]')!
    expect(detail.classList.contains('break-all')).toBe(true)
    expect(detail.classList.contains('shrink-0')).toBe(false)
  })

  it('keeps an except list when filtering and copying', async () => {
    const group = summarizeNetworkPolicy({
      ingress: [{ from: [{ ipBlock: { cidr: '10.0.0.0/8', except: ['10.1.0.0/16'] } }, ...Array.from({ length: 12 }, (_, i) => ({ ipBlock: { cidr: `172.16.${i * 2}.0/24` } }))] }],
    }).ingress.rules[0].groups[0]
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    await mount(group)
    await typeFilter('10.0.0.0')
    expect(chips()).toHaveLength(1)
    expect(chips()[0].textContent).toContain('except 10.1.0.0/16')
    await act(async () => (element.querySelector('button[aria-label="Copy list"]') as HTMLButtonElement).click())
    expect(writeText.mock.calls[0][0]).toContain('10.0.0.0/8 except 10.1.0.0/16')
    vi.unstubAllGlobals()
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  })

  it('copies through the fallback when the Clipboard API is missing (plain HTTP)', async () => {
    vi.stubGlobal('navigator', {})
    const execCommand = vi.fn().mockReturnValue(true)
    Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true })
    await mount(largeIpGroup())
    await act(async () => (element.querySelector('button[aria-label="Copy list"]') as HTMLButtonElement).click())
    expect(execCommand).toHaveBeenCalledWith('copy')
    vi.unstubAllGlobals()
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  })
})
