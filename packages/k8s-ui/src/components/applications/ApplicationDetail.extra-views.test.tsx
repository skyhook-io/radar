// @vitest-environment jsdom
//
// Host-supplied app-level views: tabs after the built-in ones, selected through
// the same selectedView/onSelectView pair, falling back to Overview for an id
// the host no longer offers.
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApplicationDetail, type ApplicationDetailProps, type ApplicationExtraView } from './ApplicationDetail'
import type { AppRow } from '../../utils/applications'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const workload = (name: string) => ({
  kind: 'Deployment',
  namespace: 'shop',
  name,
  workload_class: 'service' as const,
  health: 'healthy' as const,
  ready: 1,
  desired: 1,
  restarts: 0,
})

const app: AppRow = {
  key: 'shop/checkout',
  name: 'checkout',
  namespace: 'shop',
  health: 'healthy',
  workloads: [workload('checkout-api'), workload('checkout-worker')],
}

const extraViews: ApplicationExtraView[] = [
  { id: 'deploys', label: 'Deploys', render: () => <div data-testid="body">Deploys body</div> },
  { id: 'settings', label: 'Settings', render: () => <div data-testid="body">Settings body</div> },
]

let root: Root | undefined
let host: HTMLElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  root = undefined
  host = undefined
})

function show(props: Partial<ApplicationDetailProps>) {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const render = (next: Partial<ApplicationDetailProps>) =>
    act(() =>
      root!.render(
        <ApplicationDetail
          {...({
            app,
            onBack: () => {},
            renderWorkload: () => <div>runtime</div>,
            renderOverviewIssues: () => null,
            selectedWorkloadKey: null,
            onSelectWorkload: () => {},
            ...next,
          } as ApplicationDetailProps)}
        />,
      ),
    )
  render(props)
  return { host, rerender: render }
}

const tabs = (el: HTMLElement) => Array.from(el.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
const tab = (el: HTMLElement, label: string) => tabs(el).find((t) => t.textContent?.trim() === label)!
const selected = (el: HTMLElement) => tabs(el).filter((t) => t.getAttribute('aria-selected') === 'true').map((t) => t.textContent?.trim())

describe('ApplicationDetail extra views', () => {
  it('lists host views after the built-in ones and shows no extra tab without them', () => {
    const plain = show({})
    expect(tabs(plain.host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'History'])
    act(() => root?.unmount())

    const withExtras = show({ extraViews })
    expect(tabs(withExtras.host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'History', 'Deploys', 'Settings'])
    expect(selected(withExtras.host)).toEqual(['Overview'])
    expect(withExtras.host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('selects a host view on click when the host does not control the view', () => {
    const { host } = show({ extraViews })
    act(() => tab(host, 'Settings').click())
    expect(selected(host)).toEqual(['Settings'])
    expect(host.querySelector('[data-testid="body"]')?.textContent).toBe('Settings body')
    act(() => tab(host, 'Topology').click())
    expect(selected(host)).toEqual(['Topology'])
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('resets an uncontrolled view before rendering the next instance', () => {
    const render = vi.fn(() => <div>Instance settings</div>)
    const views = [{ id: 'settings', label: 'Settings', render }]
    const { host, rerender } = show({ extraViews: views, activeInstanceKey: 'prod' })
    act(() => tab(host, 'Settings').click())
    expect(host.textContent).toContain('Instance settings')
    render.mockClear()
    rerender({ extraViews: views, activeInstanceKey: 'staging' })
    expect(render).not.toHaveBeenCalled()
    expect(selected(host)).toEqual(['Overview'])
  })

  it('reports the id through onSelectView and renders the view the host selects', () => {
    const onSelectView = vi.fn()
    const { host, rerender } = show({ extraViews, selectedView: 'overview', onSelectView })
    act(() => tab(host, 'Deploys').click())
    expect(onSelectView).toHaveBeenCalledWith('deploys')
    expect(selected(host)).toEqual(['Overview'])

    rerender({ extraViews, selectedView: 'deploys', onSelectView })
    expect(selected(host)).toEqual(['Deploys'])
    expect(host.querySelector('[data-testid="body"]')?.textContent).toBe('Deploys body')
  })

  it("starts a host view afresh on another instance, so one instance's state never shows on the next", () => {
    const Counter = () => {
      const [n, setN] = useState(0)
      return (
        <button type="button" data-testid="count" onClick={() => setN((x) => x + 1)}>
          {n}
        </button>
      )
    }
    const views: ApplicationExtraView[] = [{ id: 'deploys', label: 'Deploys', render: () => <Counter /> }]
    const { host, rerender } = show({ extraViews: views, selectedView: 'deploys', onSelectView: () => {}, activeInstanceKey: 'prod', selectedWorkloadKey: undefined })
    const count = () => host.querySelector<HTMLButtonElement>('[data-testid="count"]')!
    act(() => count().click())
    expect(count().textContent).toBe('1')
    rerender({ extraViews: views, selectedView: 'deploys', onSelectView: () => {}, activeInstanceKey: 'staging', selectedWorkloadKey: undefined })
    expect(count().textContent).toBe('0')
    act(() => count().click())
    rerender({ app: { ...app, key: 'shop/another' }, extraViews: views, selectedView: 'deploys', onSelectView: () => {}, activeInstanceKey: 'staging', selectedWorkloadKey: undefined })
    expect(count().textContent).toBe('0')
  })

  it("shows the host's line above every view's content", () => {
    const { host, rerender } = show({ extraViews, selectedView: 'overview', onSelectView: () => {}, viewHeader: <span data-testid="vh">Pods on old values</span> })
    expect(host.querySelector('[data-testid="vh"]')).not.toBeNull()
    rerender({ extraViews, selectedView: 'deploys', onSelectView: () => {}, viewHeader: <span data-testid="vh">Pods on old values</span> })
    expect(host.querySelector('[data-testid="vh"]')).not.toBeNull()
  })

  it("orders the tabs as the host asks, built-in views first otherwise", () => {
    const { host } = show({ extraViews, viewOrder: ['overview', 'deploys', 'settings', 'topology'] })
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Deploys', 'Settings', 'Topology', 'History'])
  })

  it('falls back to the Overview when the selected id is not offered', () => {
    const { host } = show({ extraViews: extraViews.slice(1), selectedView: 'deploys', onSelectView: () => {} })
    expect(selected(host)).toEqual(['Overview'])
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('uses the visible tab order to replace a stale controlled view', () => {
    const onSelectView = vi.fn()
    const { host } = show({ extraViews, viewOrder: ['settings', 'history'], selectedView: 'old-view', onSelectView })
    expect(selected(host)).toEqual(['Settings'])
    expect(onSelectView).toHaveBeenCalledExactlyOnceWith('settings', { replace: true })
  })

  it('clears a stale workload before asking the host to correct the selected view', () => {
    const onSelectWorkload = vi.fn()
    const onSelectView = vi.fn()
    const props = { extraViews, selectedView: 'removed', onSelectWorkload, onSelectView }
    const { rerender } = show({ ...props, selectedWorkloadKey: 'removed' })
    expect(onSelectWorkload).toHaveBeenCalledExactlyOnceWith(null, { replace: true })
    expect(onSelectView).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: 'another removed workload' })
    expect(onSelectWorkload).toHaveBeenCalledTimes(2)
    expect(onSelectView).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: null })
    expect(onSelectView).toHaveBeenCalledExactlyOnceWith('overview', { replace: true })
  })

  it('preserves workload and tab links while the host cannot see live workloads', () => {
    const onSelectWorkload = vi.fn()
    const onSelectView = vi.fn()
    const props = { extraViews, viewOrder: ['deploys', 'overview'], selectedView: 'yaml', selectedWorkloadKey: 'removed', onSelectWorkload, onSelectView }
    const empty: AppRow = { ...app, workloads: [] }
    const { host, rerender } = show({ ...props, app: empty, liveUnavailableNotice: <p>Offline</p> })
    expect(host.textContent).toContain('Offline')
    expect(host.textContent).not.toContain('Deploys body')
    expect(onSelectView).not.toHaveBeenCalled()
    expect(onSelectWorkload).not.toHaveBeenCalled()
    rerender({ ...props, app: empty, selectionLoading: true })
    expect(host.textContent).not.toContain('Deploys body')
    expect(onSelectView).not.toHaveBeenCalled()
    expect(onSelectWorkload).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: null, app: { ...app, workloads: app.workloads?.slice(0, 1) }, liveUnavailableNotice: false })
    expect(host.textContent).toContain('runtime')
    expect(onSelectView).not.toHaveBeenCalled()
  })

  it('does not render reserved or duplicate extra views', () => {
    const reserved = extraViews[0]
    const { host } = show({
      builtInViews: [],
      extraViews: [{ ...reserved, id: 'overview' }, { ...reserved, id: 'cost' }, ...extraViews, reserved],
      renderCostView: () => <div>Cost body</div>,
      selectedView: 'overview',
      onSelectView: () => {},
    })
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Cost', 'Deploys', 'Settings'])
    expect(selected(host)).toEqual(['Deploys'])
  })

  it('keeps the Cost view ahead of a selected host view', () => {
    const { host } = show({
      extraViews,
      selectedView: 'deploys',
      onSelectView: () => {},
      renderCostView: () => <div>Cost body</div>,
      costViewSelected: true,
      onSelectCostView: () => {},
    })
    expect(selected(host)).toEqual(['Cost'])
    expect(host.textContent).toContain('Cost body')
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('never lets a host view replace a built-in one', () => {
    const { host } = show({
      extraViews: [{ id: 'history', label: 'Not history', render: () => <div data-testid="body">shadow</div> }],
      selectedView: 'history',
      onSelectView: () => {},
    })
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'History'])
    expect(selected(host)).toEqual(['History'])
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('shows the host notice in place of the built-in views when the instance has nothing live', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const { host, rerender } = show({ app: empty, extraViews, selectedView: 'topology', onSelectView: () => {}, liveUnavailableNotice: <p data-testid="notice">Not running</p> })
    expect(selected(host)).toEqual(['Topology'])
    expect(host.querySelector('[data-testid="notice"]')?.textContent).toBe('Not running')

    rerender({ app: empty, extraViews, selectedView: 'deploys', onSelectView: () => {}, liveUnavailableNotice: <p data-testid="notice">Not running</p> })
    expect(host.querySelector('[data-testid="notice"]')).toBeNull()
    expect(host.querySelector('[data-testid="body"]')?.textContent).toBe('Deploys body')
  })

  it('uses a host-only default view even without live workload evidence', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const { host } = show({ app: empty, extraViews, builtInViews: [], liveUnavailableNotice: <p>Offline</p> })
    expect(selected(host)).toEqual(['Deploys'])
    expect(host.textContent).toContain('Deploys body')
  })

  it('shows an unavailable workload link before a host-only default view', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const onSelectWorkload = vi.fn()
    const { host, rerender } = show({ app: empty, extraViews, builtInViews: [], selectedWorkloadKey: 'missing', onSelectWorkload, liveUnavailableNotice: <p>Offline</p> })
    expect(host.textContent).toContain('Offline')
    expect(host.textContent).not.toContain('Deploys body')
    expect(onSelectWorkload).not.toHaveBeenCalled()
    rerender({ app: empty, extraViews, builtInViews: [], selectedWorkloadKey: 'missing', onSelectWorkload, liveUnavailableNotice: <p>Offline</p>, selectedView: 'deploys', onSelectView: vi.fn() })
    expect(host.textContent).not.toContain('Deploys body')
  })

  it("shows an instance's detail beside its env in the switcher", () => {
    const instance = (env: string, detail?: string) => ({ appKey: env, name: `${env} on nonprod`, env, detail, health: 'healthy' as const, confidence: 'high', evidence: '' })
    const { host } = show({ identityInstances: [instance('prod', 'nonprod'), instance('staging')], activeInstanceKey: 'prod', onSwitchInstance: () => {} })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => /^(prod|staging)/.test(b.textContent ?? ''))
    expect(pills.map((b) => b.textContent)).toEqual(['prodnonprod', 'staging'])
  })

  it('names a duplicated env by its instance when the detail is empty, and shows an adornment', () => {
    const instance = (appKey: string, env: string, extra: object = {}) => ({ appKey, name: `${appKey} on nonprod`, env, health: 'healthy' as const, confidence: 'high', evidence: '', ...extra })
    const { host } = show({
      identityInstances: [instance('a', 'prod', { detail: '' }), instance('b', 'prod', { adornment: <span data-testid="mark">•</span> })],
      activeInstanceKey: 'a',
      onSwitchInstance: () => {},
    })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => (b.textContent ?? '').startsWith('prod'))
    expect(pills.map((b) => b.textContent)).toEqual(['proda on nonprod', 'prodb on nonprod•'])
    expect(host.querySelectorAll('[data-testid="mark"]')).toHaveLength(1)
  })

  it('distinguishes instances that share an environment and cluster label', () => {
    const instance = (appKey: string) => ({ appKey, name: `${appKey} on nonprod`, env: 'prod', detail: 'nonprod', health: 'healthy' as const, confidence: 'high', evidence: '' })
    const { host } = show({ identityInstances: [instance('a'), instance('b')], activeInstanceKey: 'a', onSwitchInstance: () => {} })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => (b.textContent ?? '').startsWith('prod'))
    expect(pills.map((b) => b.textContent)).toEqual(['proda on nonprod', 'prodb on nonprod'])
  })

  it('offers only the built-in views the host asks for, landing on the first view offered', () => {
    const none = show({ extraViews, builtInViews: [], selectedView: 'overview', onSelectView: () => {} })
    expect(tabs(none.host).map((t) => t.textContent?.trim())).toEqual(['Deploys', 'Settings'])
    expect(selected(none.host)).toEqual(['Deploys'])
    expect(none.host.querySelector('[data-testid="body"]')?.textContent).toBe('Deploys body')
    act(() => root?.unmount())

    const some = show({ extraViews, builtInViews: ['topology'], selectedView: 'history', onSelectView: () => {} })
    expect(tabs(some.host).map((t) => t.textContent?.trim())).toEqual(['Topology', 'Deploys', 'Settings'])
    expect(selected(some.host)).toEqual(['Topology'])
  })

  it('keeps every environment a pill, with its version, up to six environments; All environments is not counted', () => {
    const instance = (env: string, extra: object = {}) => ({ appKey: env, name: env, env, version: `${env}-v1`, health: 'healthy' as const, confidence: 'high', evidence: '', ...extra })
    const envs = ['prod', 'prod-internal', 'staging', 'demo', 'qa', 'dev']
    const { host } = show({
      showEnvironmentVersions: true,
      identityInstances: [instance('All environments', { aggregate: true, version: undefined }), ...envs.map((e) => instance(e))],
      activeInstanceKey: 'prod',
      onSwitchInstance: () => {},
    })
    const pills = Array.from(host.querySelectorAll('button[aria-current], button')).filter((b) => envs.some((e) => b.textContent === `${e}${e}-v1`))
    expect(pills).toHaveLength(6)
    expect(host.textContent).not.toContain('environments ')
    act(() => root?.unmount())

    // A seventh: the picker, counting environments only.
    const more = show({
      showEnvironmentVersions: true,
      identityInstances: [instance('All environments', { aggregate: true, version: undefined }), ...[...envs, 'perf'].map((e) => instance(e))],
      activeInstanceKey: 'prod',
      onSwitchInstance: () => {},
    })
    expect(more.host.textContent).toContain('· 7 environments')
  })

  it('marks the current instance with aria-current and shows each version on its pill', () => {
    const instance = (env: string, version: string) => ({ appKey: env, name: `${env} on nonprod`, env, version, health: 'healthy' as const, confidence: 'high', evidence: `${env} evidence` })
    const { host } = show({ showEnvironmentVersions: true, identityInstances: [instance('prod', '1.2.0'), instance('staging', '1.3.0')], activeInstanceKey: 'prod', onSwitchInstance: () => {} })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => /^(prod|staging)/.test(b.textContent ?? ''))
    expect(pills.map((b) => [b.textContent, b.getAttribute('aria-current'), b.disabled])).toEqual([
      ['prod1.2.0', 'true', true],
      ['staging1.3.0', null, false],
    ])
  })

  it('displays a configured version without using it as evidence of promotion lag', () => {
    const instance = (env: string) => ({ appKey: env, name: env, env, health: 'healthy' as const, confidence: 'high', evidence: '' })
    const prod = { ...instance('prod'), version: '1.0.0' }
    const staging = { ...instance('staging'), declaredVersion: '2.0.0' }
    const props = { showEnvironmentVersions: true, activeInstanceKey: 'prod', onSwitchInstance: () => {} }
    const { host, rerender } = show({ ...props, identityInstances: [prod, staging] })
    expect(host.textContent).toContain('staging2.0.0')
    expect(host.textContent).not.toContain('behind')
    rerender({ ...props, identityInstances: [prod, { ...staging, version: '2.0.0' }] })
    expect(host.textContent).toContain('behind')
  })
})
