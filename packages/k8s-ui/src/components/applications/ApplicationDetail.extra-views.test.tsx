// @vitest-environment jsdom
//
// Host-supplied app-level views: tabs after the built-in ones, selected through
// the same selectedView/onSelectView pair, falling back to Overview for an id
// the host no longer offers.
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApplicationDetail, type ApplicationDetailProps, type ApplicationExtraView } from './ApplicationDetail'
import { workloadKey } from '../../utils/topology-neighborhood'
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
  { id: 'release-notes', label: 'Release notes', render: () => <div data-testid="body">Release notes body</div> },
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
    expect(tabs(plain.host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'Timeline'])
    act(() => root?.unmount())

    const withExtras = show({ extraViews })
    expect(tabs(withExtras.host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'Timeline', 'Release notes', 'Settings'])
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
    act(() => tab(host, 'Release notes').click())
    expect(onSelectView).toHaveBeenCalledWith('release-notes')
    expect(selected(host)).toEqual(['Overview'])

    rerender({ extraViews, selectedView: 'release-notes', onSelectView })
    expect(selected(host)).toEqual(['Release notes'])
    expect(host.querySelector('[data-testid="body"]')?.textContent).toBe('Release notes body')
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
    const views: ApplicationExtraView[] = [{ id: 'release-notes', label: 'Release notes', render: () => <Counter /> }]
    const { host, rerender } = show({ extraViews: views, selectedView: 'release-notes', onSelectView: () => {}, activeInstanceKey: 'prod', selectedWorkloadKey: undefined })
    const count = () => host.querySelector<HTMLButtonElement>('[data-testid="count"]')!
    act(() => count().click())
    expect(count().textContent).toBe('1')
    rerender({ extraViews: views, selectedView: 'release-notes', onSelectView: () => {}, activeInstanceKey: 'staging', selectedWorkloadKey: undefined })
    expect(count().textContent).toBe('0')
    act(() => count().click())
    rerender({ app: { ...app, key: 'shop/another' }, extraViews: views, selectedView: 'release-notes', onSelectView: () => {}, activeInstanceKey: 'staging', selectedWorkloadKey: undefined })
    expect(count().textContent).toBe('0')
  })

  it("orders the tabs as the host asks, built-in views first otherwise", () => {
    const { host } = show({ extraViews, viewOrder: ['overview', 'release-notes', 'settings', 'topology'] })
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Release notes', 'Settings', 'Topology', 'Timeline'])
  })

  it('falls back to the Overview when the selected id is not offered', () => {
    const { host } = show({ extraViews: extraViews.slice(1), selectedView: 'release-notes', onSelectView: () => {} })
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
    const render = vi.fn(() => <div>Release notes</div>)
    const props = { extraViews: [{ id: 'release-notes', label: 'Release notes', render }], viewOrder: ['release-notes', 'overview'], selectedView: 'removed', onSelectWorkload, onSelectView }
    const { rerender } = show({ ...props, selectedWorkloadKey: 'removed' })
    expect(onSelectWorkload).toHaveBeenCalledExactlyOnceWith(null, { replace: true })
    expect(onSelectView).not.toHaveBeenCalled()
    expect(render).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: 'another removed workload' })
    expect(onSelectWorkload).toHaveBeenCalledTimes(2)
    expect(onSelectView).not.toHaveBeenCalled()
    expect(render).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: null })
    expect(onSelectView).toHaveBeenCalledExactlyOnceWith('release-notes', { replace: true })
  })

  it('preserves workload and tab links while the host cannot see live workloads', () => {
    const onSelectWorkload = vi.fn()
    const onSelectView = vi.fn()
    const props = { extraViews, viewOrder: ['release-notes', 'overview'], selectedView: 'yaml', selectedWorkloadKey: 'removed', onSelectWorkload, onSelectView }
    const empty: AppRow = { ...app, workloads: [] }
    const { host, rerender } = show({ ...props, app: empty, liveUnavailableNotice: <p>Offline</p> })
    expect(host.textContent).toContain('Offline')
    expect(host.textContent).not.toContain('Release notes body')
    expect(onSelectView).not.toHaveBeenCalled()
    expect(onSelectWorkload).not.toHaveBeenCalled()
    rerender({ ...props, app: empty, selectionLoading: true })
    expect(host.textContent).not.toContain('Release notes body')
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
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Cost', 'Release notes', 'Settings'])
    expect(selected(host)).toEqual(['Release notes'])
  })

  it('keeps the Cost view ahead of a selected host view', () => {
    const { host } = show({
      extraViews,
      selectedView: 'release-notes',
      onSelectView: () => {},
      renderCostView: () => <div>Cost body</div>,
      costViewSelected: true,
      onSelectCostView: () => {},
    })
    expect(selected(host)).toEqual(['Cost'])
    expect(host.textContent).toContain('Cost body')
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('opens Cost when the allowlisted Deploys has no renderer and Cost is the only available view', () => {
    const { host } = show({
      builtInViews: ['deploys'],
      selectedView: 'deploys',
      onSelectView: vi.fn(),
      renderCostView: () => <div>Cost body</div>,
      costViewSelected: false,
      onSelectCostView: vi.fn(),
    })
    expect(selected(host)).toEqual(['Cost'])
    expect(host.textContent).toContain('Cost body')
  })

  it('remounts a singleton workload editor when switching instances', () => {
    function WorkloadEditor() {
      const [draft, setDraft] = useState('Clean')
      return <button onClick={() => setDraft('Staging draft')}>{draft}</button>
    }
    const props = { app: { ...app, workloads: [workload('api')] }, renderWorkload: () => <WorkloadEditor /> }
    const { host, rerender } = show({ ...props, activeInstanceKey: 'staging' })
    act(() => Array.from(host.querySelectorAll('button')).find(button => button.textContent === 'Clean')!.click())
    expect(host.textContent).toContain('Staging draft')
    rerender({ ...props, activeInstanceKey: 'prod' })
    expect(host.textContent).toContain('Clean')
    expect(host.textContent).not.toContain('Staging draft')
  })

  it('remounts a controlled Cost panel when switching instances', () => {
    function CostBody({ instance }: { instance: string }) {
      const [mountedInstance] = useState(instance)
      return <div>{mountedInstance}</div>
    }
    const props = {
      activeInstanceKey: 'prod',
      costViewSelected: true,
      renderCostView: () => <CostBody instance="prod costs" />,
    }
    const { host, rerender } = show(props)
    expect(host.textContent).toContain('prod costs')
    rerender({ ...props, activeInstanceKey: 'staging', renderCostView: () => <CostBody instance="staging costs" /> })
    expect(host.textContent).toContain('staging costs')
    expect(host.textContent).not.toContain('prod costs')
  })

  it('never lets a host view replace a built-in one', () => {
    const { host } = show({
      extraViews: [{ id: 'history', label: 'Not history', render: () => <div data-testid="body">shadow</div> }],
      selectedView: 'history',
      onSelectView: () => {},
    })
    expect(tabs(host).map((t) => t.textContent?.trim())).toEqual(['Overview', 'Topology', 'Timeline'])
    expect(selected(host)).toEqual(['Timeline'])
    expect(host.querySelector('[data-testid="body"]')).toBeNull()
  })

  it('reserves Deploys even when its renderer is absent or the built-in view is hidden', () => {
    const shadow = vi.fn(() => <div>Shadow delivery view</div>)
    const renderDeploys = vi.fn(() => <div>Recorded deliveries</div>)
    const props = {
      extraViews: [{ id: 'deploys', label: 'Shadow Deploys', render: shadow }, ...extraViews],
      selectedView: 'deploys',
      onSelectView: vi.fn(),
    }
    const { host, rerender } = show(props)
    expect(tabs(host).map((t) => t.textContent)).not.toContain('Shadow Deploys')
    expect(selected(host)).toEqual(['Overview'])
    rerender({ ...props, renderDeploys, builtInViews: ['overview'] })
    expect(selected(host)).toEqual(['Overview'])
    expect(renderDeploys).not.toHaveBeenCalled()
    rerender({ ...props, renderDeploys, viewOrder: ['deploys', 'release-notes'] })
    expect(tabs(host).slice(0, 2).map((t) => t.textContent)).toEqual(['Deploys', 'Release notes'])
    expect(selected(host)).toEqual(['Deploys'])
    expect(host.textContent).toContain('Recorded deliveries')
    expect(shadow).not.toHaveBeenCalled()
  })

  it('renders explicit Deploys without live workloads and leaves it suspended for an unresolved workload link', () => {
    const renderDeploys = vi.fn(() => <div>Recorded deliveries</div>)
    const props = {
      app: { ...app, workloads: [] },
      selectedView: 'deploys',
      onSelectView: vi.fn(),
      onSelectWorkload: vi.fn(),
      renderDeploys,
      viewOrder: ['deploys', 'overview'],
      liveUnavailableNotice: <p>Offline</p>,
    } satisfies Partial<ApplicationDetailProps>
    const { host, rerender } = show({ ...props, selectedWorkloadKey: 'unresolved' })
    expect(host.textContent).toContain('Offline')
    expect(renderDeploys).not.toHaveBeenCalled()
    rerender({ ...props, selectedWorkloadKey: null })
    expect(host.textContent).toContain('Recorded deliveries')
    expect(host.textContent).not.toContain('Offline')
  })

  it('honors the Deploys allowlist in the single-workload tab strip', () => {
    const single = workload('checkout-api')
    const renderWorkload: ApplicationDetailProps['renderWorkload'] = (_, { extraTabs }) =>
      <div>{extraTabs?.map(({ id, label }) => <button key={id}>{label}</button>)}</div>
    const props = {
      app: { ...app, workloads: [single] },
      selectedWorkloadKey: workloadKey(single),
      onSelectWorkload: vi.fn(),
      renderWorkload,
      renderDeploys: () => <div>Recorded deliveries</div>,
    }
    const { host, rerender } = show({ ...props, builtInViews: ['overview'] })
    expect(host.textContent).not.toContain('Deploys')
    rerender({ ...props, builtInViews: ['deploys'] })
    expect(host.textContent).toContain('Deploys')
  })

  it('suspends singleton Deploys content while a controlled workload link is provisional', () => {
    const single = workload('checkout-api')
    const renderDeploys = vi.fn(() => <div>Recorded deliveries</div>)
    const renderWorkload: ApplicationDetailProps['renderWorkload'] = (_, { extraTabs }) =>
      extraTabs?.find(({ id }) => id === 'deploys')?.render()
    const props = {
      app: { ...app, workloads: [single] },
      selectedWorkloadKey: workloadKey(single),
      onSelectWorkload: vi.fn(),
      renderWorkload,
      renderDeploys,
    }
    const { host, rerender } = show({ ...props, selectionLoading: true })
    expect(host.textContent).toContain('Loading application views')
    expect(renderDeploys).not.toHaveBeenCalled()
    rerender(props)
    expect(host.textContent).toContain('Recorded deliveries')
  })

  it('shows the host notice in place of the built-in views when the instance has nothing live', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const { host, rerender } = show({ app: empty, extraViews, selectedView: 'topology', onSelectView: () => {}, liveUnavailableNotice: <p data-testid="notice">Not running</p> })
    expect(selected(host)).toEqual(['Topology'])
    expect(host.querySelector('[data-testid="notice"]')?.textContent).toBe('Not running')

    rerender({ app: empty, extraViews, selectedView: 'release-notes', onSelectView: () => {}, liveUnavailableNotice: <p data-testid="notice">Not running</p> })
    expect(host.querySelector('[data-testid="notice"]')).toBeNull()
    expect(host.querySelector('[data-testid="body"]')?.textContent).toBe('Release notes body')
  })

  it('uses a host-only default view even without live workload evidence', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const { host } = show({ app: empty, extraViews, builtInViews: [], liveUnavailableNotice: <p>Offline</p> })
    expect(selected(host)).toEqual(['Release notes'])
    expect(host.textContent).toContain('Release notes body')
  })

  it('shows a loader and leaves every tab unselected until a requested view is available', () => {
    const renderOverviewIssues = vi.fn(() => null)
    const props = { selectedView: 'deploys', onSelectView: vi.fn(), renderOverviewIssues }
    const { host, rerender } = show({ ...props, selectionLoading: true })
    expect(tab(host, 'Overview')).toBeDefined()
    expect(selected(host)).toEqual([])
    expect(host.textContent).toContain('Loading application views')
    expect(renderOverviewIssues).not.toHaveBeenCalled()
    expect(props.onSelectView).not.toHaveBeenCalled()
    rerender({ ...props, renderDeploys: () => <div>Recorded deliveries</div> })
    expect(selected(host)).toEqual(['Deploys'])
    expect(host.textContent).toContain('Recorded deliveries')
  })

  it('holds an empty workload key offline before mounting an available host view', () => {
    const render = vi.fn(() => <p>Settings body</p>)
    const props = { selectedView: 'settings', extraViews: [{ id: 'settings', label: 'Settings', render }], onSelectView: vi.fn(), onSelectWorkload: vi.fn() }
    const { host } = show({ ...props, selectedWorkloadKey: '', liveUnavailableNotice: <p>Offline</p> })
    expect(host.textContent).toContain('Offline')
    expect(selected(host)).toEqual([])
    expect(render).not.toHaveBeenCalled()
    expect(props.onSelectView).not.toHaveBeenCalled()
    expect(props.onSelectWorkload).not.toHaveBeenCalled()
  })

  it('keeps an unavailable controlled Deploys link offline without mounting another host view', () => {
    const onSelectView = vi.fn()
    const { host } = show({
      app: { ...app, workloads: [] },
      extraViews,
      selectedView: 'deploys',
      onSelectView,
      liveUnavailableNotice: <p>Offline</p>,
    })
    expect(host.textContent).toContain('Offline')
    expect(tab(host, 'Overview')).toBeDefined()
    expect(selected(host)).toEqual([])
    expect(host.textContent).not.toContain('Release notes body')
    expect(onSelectView).not.toHaveBeenCalled()
  })

  it('holds the offline notice for a denied Deploys link even when Cost is available', () => {
    const { host } = show({
      app: { ...app, workloads: [] },
      builtInViews: ['deploys'],
      selectedView: 'deploys',
      onSelectView: vi.fn(),
      costViewSelected: false,
      renderCostView: () => <div>Cost body</div>,
      liveUnavailableNotice: <p>Offline</p>,
    })
    expect(host.textContent).toContain('Offline')
    expect(tab(host, 'Cost').getAttribute('aria-selected')).toBe('false')
    expect(host.textContent).not.toContain('Cost body')
  })

  it('shows an unavailable workload link before a host-only default view', () => {
    const empty: AppRow = { ...app, workloads: [] }
    const onSelectWorkload = vi.fn()
    const { host, rerender } = show({ app: empty, extraViews, builtInViews: [], selectedWorkloadKey: 'missing', onSelectWorkload, liveUnavailableNotice: <p>Offline</p> })
    expect(host.textContent).toContain('Offline')
    expect(host.textContent).not.toContain('Release notes body')
    expect(onSelectWorkload).not.toHaveBeenCalled()
    rerender({ app: empty, extraViews, builtInViews: [], selectedWorkloadKey: 'missing', onSelectWorkload, liveUnavailableNotice: <p>Offline</p>, selectedView: 'release-notes', onSelectView: vi.fn() })
    expect(host.textContent).not.toContain('Release notes body')
  })

  it('leaves an unresolved workload link on a deliberate app-tab click', () => {
    const onSelectWorkload = vi.fn()
    const props = { app: { ...app, workloads: [] }, extraViews, liveUnavailableNotice: <p>Offline</p>, onSelectWorkload }
    const { host, rerender } = show({ ...props, selectedWorkloadKey: 'missing' })
    act(() => tab(host, 'Release notes').click())
    expect(onSelectWorkload).toHaveBeenCalledExactlyOnceWith(null)
    rerender({ ...props, selectedWorkloadKey: null })
    expect(host.textContent).toContain('Release notes body')
  })

  it("shows an instance's detail beside its env in the switcher", () => {
    const instance = (env: string, detail?: string) => ({ appKey: env, name: `${env} on nonprod`, env, detail, health: 'healthy' as const, confidence: 'high', evidence: '' })
    const { host } = show({ identityInstances: [instance('prod', 'nonprod'), instance('staging')], activeInstanceKey: 'prod', onSwitchInstance: () => {} })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => /^(prod|staging)/.test(b.textContent ?? ''))
    expect(pills.map((b) => b.textContent)).toEqual(['prodnonprod', 'staging'])
  })

  it('names a duplicated env by its instance when the detail is empty', () => {
    const instance = (appKey: string, env: string, extra: object = {}) => ({ appKey, name: `${appKey} on nonprod`, env, health: 'healthy' as const, confidence: 'high', evidence: '', ...extra })
    const { host } = show({
      identityInstances: [instance('a', 'prod', { detail: '' }), instance('b', 'prod')],
      activeInstanceKey: 'a',
      onSwitchInstance: () => {},
    })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => (b.textContent ?? '').startsWith('prod'))
    expect(pills.map((b) => b.textContent)).toEqual(['proda on nonprod', 'prodb on nonprod'])
  })

  it('distinguishes instances that share an environment and cluster label', () => {
    const instance = (appKey: string) => ({ appKey, name: `${appKey} on nonprod`, env: 'prod', detail: 'nonprod', health: 'healthy' as const, confidence: 'high', evidence: '' })
    const { host } = show({ identityInstances: [instance('a'), instance('b')], activeInstanceKey: 'a', onSwitchInstance: () => {} })
    const pills = Array.from(host.querySelectorAll('button')).filter((b) => (b.textContent ?? '').startsWith('prod'))
    expect(pills.map((b) => b.textContent)).toEqual(['proda on nonprod', 'prodb on nonprod'])
  })

  it('offers only the built-in views the host asks for, landing on the first view offered', () => {
    const none = show({ extraViews, builtInViews: [], selectedView: 'overview', onSelectView: () => {} })
    expect(tabs(none.host).map((t) => t.textContent?.trim())).toEqual(['Release notes', 'Settings'])
    expect(selected(none.host)).toEqual(['Release notes'])
    expect(none.host.querySelector('[data-testid="body"]')?.textContent).toBe('Release notes body')
    act(() => root?.unmount())

    const some = show({ extraViews, builtInViews: ['topology'], selectedView: 'history', onSelectView: () => {} })
    expect(tabs(some.host).map((t) => t.textContent?.trim())).toEqual(['Topology', 'Release notes', 'Settings'])
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
    const stagingButton = () => Array.from(host.querySelectorAll('button')).find(b => b.textContent?.startsWith('staging'))!
    expect(stagingButton().textContent).toBe('staging2.0.0 (configured)')
    expect(host.textContent).not.toContain('behind')
    rerender({ ...props, identityInstances: [prod, { ...staging, version: '1.5.0' }] })
    expect(stagingButton().textContent).toBe('staging1.5.0')
    expect(host.textContent).toContain('behind')

    rerender({ ...props, identityInstances: [prod, staging, ...['dev', 'qa', 'test', 'demo', 'perf'].map(instance)] })
    act(() => Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('7 environments'))!.click())
    expect(stagingButton().textContent).toContain('2.0.0 (configured)')
  })

  it('shows the identity evidence chip only for evidence from a non-aggregate instance', () => {
    const prod = { appKey: 'prod', name: 'prod', env: 'prod', health: 'healthy' as const, confidence: 'high', evidence: '' }
    const all = { ...prod, appKey: 'all', env: 'All environments', aggregate: true, evidence: 'aggregate' }
    const props = { activeInstanceKey: 'prod', onSwitchInstance: () => {} }
    const { host, rerender } = show({ ...props, identityInstances: [all, prod] })
    const evidenceChip = () => host.querySelector('[aria-label="Application grouping evidence"]')
    expect(evidenceChip()).toBeNull()
    rerender({ ...props, identityInstances: [all, { ...prod, evidence: 'app annotation' }] })
    expect(evidenceChip()).not.toBeNull()
  })
})
