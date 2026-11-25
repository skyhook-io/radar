// @vitest-environment jsdom
//
// A host scoped to one source can have the tiles and facet counts count only
// what the list shows; by default they count every app.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import { buildSingleAppEntry, isHostManagedSource, SOURCE_META, SOURCE_ORDER, type FleetAppEntry, type AppHealth, type AppRow } from '../../utils/applications'
import { ApplicationsView } from './ApplicationsView'

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
function show(countsFrom?: 'all' | 'searchAndSource', apps = entries()) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() =>
    root!.render(
      <KeyboardShortcutProvider>
        <ApplicationsView entries={apps} variant="single" onSelect={() => {}} countsFrom={countsFrom} />
      </KeyboardShortcutProvider>,
    ),
  )
  return host
}
const pickManagedSource = (host: Element) =>
  act(() => [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith(SOURCE_META[managedSource].label))!.click())
const unhealthyFacet = (host: Element) => [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Unhealthy'))?.textContent

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
  it('counts every app by default', () => {
    const host = show()
    pickManagedSource(host)
    expect([...host.querySelectorAll('h3, [role="heading"], span')].some((el) => el.textContent === 'Type')).toBe(true)
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
    // Other sources stay, read dimmed: outside the filter, still pickable.
    const other = [...host.querySelectorAll('button')].find((b) => b.getAttribute('aria-pressed') === 'false' && /^(Ungrouped|Label|Helm|Argo CD|Flux)/.test(b.textContent ?? '') && b.className.includes('opacity-60'))
    expect(other).toBeDefined()
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
  it("shows what each environment runs when the host knows it", () => {
    const single = buildSingleAppEntry(app('demo-app', 'healthy'))
    const entry: FleetAppEntry = {
      ...single,
      variant: 'fleet',
      envs: [{ env: 'prod', health: 'healthy', inferred: false }, { env: 'staging', health: 'healthy', inferred: false }],
      clusters: [],
      versionSkew: false,
      envVersions: [{ env: 'prod', version: '6.7.0', source: 'observed' }, { env: 'staging', version: '6.8.0', source: 'configured' }],
    }
    const host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    act(() =>
      root!.render(
        <KeyboardShortcutProvider>
          <ApplicationsView entries={[entry]} variant="fleet" onSelect={() => {}} />
        </KeyboardShortcutProvider>,
      ),
    )
    expect(host.textContent).toContain('prod 6.7.0·staging 6.8.0 (configured)')
    vi.useFakeTimers()
    const shortTag = [...host.querySelectorAll('td .font-mono')].find((el) => el.textContent === '6.7.0')!
    act(() => { shortTag.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
    act(() => { vi.advanceTimersByTime(150) })
    expect(shortTag.hasAttribute('title')).toBe(false)
    expect(document.body.lastElementChild).toBe(host)
    act(() =>
      root!.render(
        <KeyboardShortcutProvider>
          <ApplicationsView entries={[{ ...entry, versionSkew: true, versions: ['6.7.0', '6.8.0'] }]} variant="fleet" onSelect={() => {}} />
        </KeyboardShortcutProvider>,
      ),
    )
    expect(host.textContent).toContain('version skew')
    expect(host.textContent).toContain('staging 6.8.0 (configured)')
    const configuredOnly = { ...entry, versions: ['6.7.0'], envVersions: entry.envVersions?.slice(1) }
    act(() =>
      root!.render(
        <KeyboardShortcutProvider>
          <ApplicationsView entries={[configuredOnly]} variant="fleet" onSelect={() => {}} />
        </KeyboardShortcutProvider>,
      ),
    )
    expect(host.textContent).toContain('6.7.0')
    expect(host.textContent).toContain('staging 6.8.0 (configured)')

    const longVersion = 'release-2026-11-25-123456789'
    act(() =>
      root!.render(
        <KeyboardShortcutProvider>
          <ApplicationsView entries={[{ ...entry, envVersions: [{ env: 'prod', version: longVersion, source: 'observed' }] }]} variant="fleet" onSelect={() => {}} />
        </KeyboardShortcutProvider>,
      ),
    )
    const truncatedTag = [...host.querySelectorAll('td .font-mono')].find((el) => el.textContent?.includes('…'))!
    act(() => { truncatedTag.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })) })
    act(() => { vi.advanceTimersByTime(150) })
    expect(document.body.lastElementChild?.textContent).toBe(longVersion)
  })
})
