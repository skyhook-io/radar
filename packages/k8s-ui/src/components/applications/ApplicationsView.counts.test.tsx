// @vitest-environment jsdom
//
// A host scoped to one source can have the tiles and facet counts count only
// what the list shows; by default they count every app.
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import { FilterLocationProvider } from '../../filter-state'
import { buildSingleAppEntry, isHostManagedSource, SOURCE_META, SOURCE_ORDER, type FleetAppEntry, type AppHealth, type AppRow } from '../../utils/applications'
import { ApplicationsView, type ApplicationsViewProps } from './ApplicationsView'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const app = (name: string, health: AppHealth): AppRow => ({
  key: `shop/${name}`,
  name,
  namespace: 'shop',
  health,
  workloads: [{ kind: 'Deployment', namespace: 'shop', name, workload_class: 'service', health, ready: 1, desired: 1, restarts: 0 }],
})
const managedSource = SOURCE_ORDER.find(isHostManagedSource)!
const entries = () => [
  { ...buildSingleAppEntry(app('demo-app', 'healthy')), source: managedSource },
  { ...buildSingleAppEntry(app('demo-jobs', 'healthy')), source: managedSource },
  buildSingleAppEntry(app('other', 'unhealthy')),
]

let root: Root | undefined
function mountList(search = '') {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  type ListProps = Omit<ApplicationsViewProps, 'onSelect'>
  function List(props: ListProps) {
    const [searchParams, update] = useState(() => new URLSearchParams(search))
    return (
      <FilterLocationProvider value={{ searchParams, update }}>
        <KeyboardShortcutProvider>
          <ApplicationsView {...props} onSelect={() => {}} />
        </KeyboardShortcutProvider>
      </FilterLocationProvider>
    )
  }
  return { host, render: (props: ListProps) => act(() => root!.render(<List {...props} />)) }
}
function show(countsFrom?: 'all' | 'searchAndSource', apps = entries()) {
  const { host, render } = mountList()
  render({ variant: 'single', entries: apps, countsFrom })
  return host
}
const pickManagedSource = (host: Element) =>
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith(SOURCE_META[managedSource].label))!.click())
const unhealthyFacet = (host: Element) => [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Unhealthy'))?.textContent
function setSearch(host: Element, value: string) {
  const input = host.querySelector<HTMLInputElement>('input[type="text"]')!
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
function headerTotal(host: Element) {
  const label = [...host.querySelectorAll('span')].find((el) => /^applications?$/.test(el.textContent ?? ''))
  return label?.parentElement?.previousElementSibling?.textContent
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }))
  Element.prototype.scrollIntoView = () => {}
})
afterEach(() => {
  act(() => root?.unmount())
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('ApplicationsView countsFrom', () => {
  it.each([['unknown', 'Unknown'], ['neutral', 'Idle']])('loads a ?health=%s tile before showing its clearable zero', (health, label) => {
    const { host, render } = mountList(`?health=${health}`)
    render({ variant: 'single', entries: [], loading: true })
    const skeleton = [...host.querySelectorAll('.animate-pulse')].find((el) => el.parentElement?.textContent === label)
    expect(skeleton).toBeDefined()
    expect(skeleton!.closest('button')).toBeNull()

    render({ variant: 'single', entries: entries() })
    const tile = () => [...host.querySelectorAll('button')].find((button) => button.textContent === `0${label}`)
    expect(tile()?.getAttribute('aria-pressed')).toBe('true')
    act(() => tile()!.click())
    expect(tile()).toBeUndefined()
    expect(host.querySelectorAll('tbody tr')).toHaveLength(3)
  })

  it('counts every app by default', () => {
    const host = show()
    pickManagedSource(host)
    expect([...host.querySelectorAll('h3, [role="heading"], span')].some((el) => el.textContent === 'Type')).toBe(true)
    expect(unhealthyFacet(host)).toBe('Unhealthy1')
  })

  it('reuses inventory counts when a default-mode search excludes a row', () => {
    const apps = entries()
    const excluded = apps[2]
    const readHealth = vi.fn(() => excluded.health)
    apps[2] = { ...excluded, get health() { return readHealth() } }
    const host = show('all', apps)
    expect(readHealth).toHaveBeenCalled()
    readHealth.mockClear()
    setSearch(host, 'demo')
    expect(host.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(readHealth).not.toHaveBeenCalled()
    expect(headerTotal(host)).toBe('3')
    expect(unhealthyFacet(host)).toBe('Unhealthy1')
  })

  it.each(['all', 'searchAndSource'] as const)('keeps a selected health tile clickable at zero in %s mode', (mode) => {
    const apps = entries()
    apps[2] = { ...apps[2], namespaces: ['kube-system'] }
    const host = show(mode, apps)
    const system = host.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    const tile = (count: number) => [...host.querySelectorAll('button')].find((button) => button.textContent === `${count}Unhealthy`)

    act(() => system.click())
    act(() => tile(1)!.click())
    act(() => system.click())
    expect(tile(0)?.getAttribute('aria-pressed')).toBe('true')

    act(() => tile(0)!.click())
    expect(tile(0)).toBeUndefined()
  })

  it('keeps a selected environment removable when its last app is hidden in the default count mode', () => {
    const apps = entries()
    apps[2] = { ...apps[2], namespaces: ['kube-system'], env: 'staging' }
    const host = show(undefined, apps)
    const system = host.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    const staging = () => [...host.querySelectorAll<HTMLButtonElement>('aside button')].find((button) => button.textContent?.startsWith('staging'))
    act(() => system.click())
    act(() => staging()!.click())
    act(() => system.click())
    expect(staging()?.getAttribute('aria-pressed')).toBe('true')
    act(() => staging()!.click())
    expect(staging()).toBeUndefined()
  })

  it('counts what the Source filter leaves when asked', () => {
    const host = show('searchAndSource')
    pickManagedSource(host)
    // No unhealthy app is left: the facet counts none (or hides the empty option).
    expect(unhealthyFacet(host) ?? 'Unhealthy0').toBe('Unhealthy0')
    // The Source facet still counts across every app.
    expect([...host.querySelectorAll('button')].some((b) => b.textContent === `${SOURCE_META[managedSource].label}2`)).toBe(true)
    // Every app left is the same type: the Type facet isn't offered.
    expect([...host.querySelectorAll('h3, [role="heading"], span')].some((el) => el.textContent === 'Type')).toBe(false)
  })

  it('dims only the alternate source dot and lets both sources be selected', () => {
    const host = show('searchAndSource')
    pickManagedSource(host)
    const source = (label: string) => [...host.querySelectorAll('aside button')].find((button) => button.textContent?.startsWith(label))!
    const other = source(SOURCE_META.ungrouped.label)
    expect(other.getAttribute('aria-pressed')).toBe('false')
    expect(other.matches('.opacity-60')).toBe(false)
    expect([...other.querySelectorAll('.opacity-60')]).toEqual([other.querySelector('[aria-hidden="true"]')])
    act(() => other.click())
    expect(other.getAttribute('aria-pressed')).toBe('true')
    expect(other.querySelector('.opacity-60')).toBeNull()
    expect(source(SOURCE_META[managedSource].label).getAttribute('aria-pressed')).toBe('true')
  })

  it('keeps selected environment and health controls when a source has no matches', () => {
    const apps = entries()
    apps[2] = { ...apps[2], env: 'staging' }
    const host = show('searchAndSource', apps)
    const option = (label: string) => [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith(label))!
    act(() => option('staging').click())
    act(() => option('Unhealthy').click())
    pickManagedSource(host)
    expect(option('staging').getAttribute('aria-pressed')).toBe('true')
    expect(option('Unhealthy').getAttribute('aria-pressed')).toBe('true')
    expect(option('0Unhealthy').getAttribute('aria-pressed')).toBe('true')
    act(() => option('staging').click())
    expect(option('staging')).toBeUndefined()
  })
})

describe('ApplicationsView fleet rows', () => {
  const entry: FleetAppEntry = {
    ...buildSingleAppEntry(app('demo-app', 'healthy')),
    variant: 'fleet',
    envs: [{ env: 'prod', health: 'healthy', inferred: false }, { env: 'staging', health: 'healthy', inferred: false }],
    clusters: [{ id: 'east', name: 'east', health: 'healthy' }],
    versionSkew: false,
    envVersions: [{ env: 'prod', version: '6.7.0', source: 'observed' }, { env: 'staging', version: '6.8.0', source: 'configured' }],
  }

  it('labels the observed tags when one cluster reports version skew', () => {
    vi.useFakeTimers()
    const { host, render } = mountList()
    const skewed = { ...entry, versionSkew: true, versions: ['6.7.0', '6.7.1'] }
    render({ variant: 'fleet', entries: [skewed] })
    const chip = [...host.querySelectorAll('td span')].find((el) => el.textContent === 'version skew' && el.childElementCount === 0)!
    act(() => { chip.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
    act(() => { vi.advanceTimersByTime(150) })
    expect(document.querySelector('[role="tooltip"]')?.textContent).toBe('Version skew detected. Observed tags: 6.7.0, 6.7.1')
    expect(host.textContent).toContain('staging 6.8.0 (configured)')
  })

  it("shows what each environment runs when the host knows it", () => {
    const { host, render } = mountList()
    render({ variant: 'fleet', entries: [entry] })
    expect(host.textContent).toContain('prod 6.7.0·staging 6.8.0 (configured)')
    vi.useFakeTimers()
    const shortTag = [...host.querySelectorAll<HTMLElement>('td .font-mono')].find((el) => el.textContent === '6.7.0')!
    expect(shortTag.tabIndex).toBe(-1)
    act(() => { shortTag.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
    act(() => { vi.advanceTimersByTime(150) })
    expect(shortTag.hasAttribute('title')).toBe(false)
    expect(document.body.lastElementChild).toBe(host)
    const configuredOnly = { ...entry, versions: ['6.7.0'], envVersions: entry.envVersions?.slice(1) }
    render({ variant: 'fleet', entries: [configuredOnly] })
    expect(host.textContent).toContain('6.7.0')
    expect(host.textContent).toContain('staging 6.8.0 (configured)')

  })

  it('exposes the full abbreviated tag to screen readers and on focus or hover', () => {
    vi.useFakeTimers()
    const { host, render } = mountList()
    const longVersion = 'release-2026-11-25-123456789'
    render({ variant: 'fleet', entries: [{ ...entry, envVersions: [{ env: 'prod', version: longVersion, source: 'observed' }] }] })
    const truncatedTag = host.querySelector<HTMLElement>('td [tabindex="0"].font-mono')!
    expect(truncatedTag.querySelector('[aria-hidden="true"]')?.textContent).toContain('…')
    expect(truncatedTag.querySelector('.sr-only')?.textContent).toBe(longVersion)
    act(() => truncatedTag.focus())
    expect(document.activeElement).toBe(truncatedTag)
    act(() => { vi.advanceTimersByTime(150) })
    expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(longVersion)
    act(() => truncatedTag.blur())
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
    act(() => { truncatedTag.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
    act(() => { vi.advanceTimersByTime(150) })
    expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(longVersion)
  })
})
