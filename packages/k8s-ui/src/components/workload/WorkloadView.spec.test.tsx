// @vitest-environment jsdom
import { act, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactNode } from 'react'
import { expect, it, vi } from 'vitest'
import { WorkloadView } from './WorkloadView'

const renderer = vi.hoisted(() => vi.fn(({ mainFooter }: { mainFooter?: ReactNode }) => <div>Generic renderer and Conditions{mainFooter}</div>))
vi.mock('../shared/ResourceRendererDispatch', async (original) => ({ ...await original<typeof import('../shared/ResourceRendererDispatch')>(), ResourceRendererDispatch: renderer }))

const resource = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db', labels: { app: 'postgres' } }, spec: { instances: 2 }, status: {} }
const props = { kind: 'clusters', namespace: 'db', name: 'pg', group: 'postgresql.cnpg.io', resource, onBack: () => {}, activeTab: 'spec' as const, renderSummary: () => <div>Composed Overview</div>, renderOverviewExtra: () => <div>Audit Findings</div> }

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('replaces the entire expanded spec body, including its generic sections and lead', () => {
  renderer.mockClear()
  const html = renderToStaticMarkup(<WorkloadView {...props} specTab={{ label: 'Configuration', lead: <div>Old lead</div>, render: () => <div>Purpose-built configuration</div> }} />)
  expect(html).toContain('Purpose-built configuration')
  expect(html).toContain('Configuration')
  for (const text of ['Generic renderer', 'Conditions', 'Audit Findings', 'Labels (1)', 'Metadata', 'Recent Events', 'Old lead']) expect(html).not.toContain(text)
  expect(renderer).not.toHaveBeenCalled()
})

it('preserves the generic spec body and lead when no replacement is supplied', () => {
  const html = renderToStaticMarkup(<WorkloadView {...props} specTab={{ label: 'Settings', lead: <div>Existing lead</div> }} />)
  for (const text of ['Settings', 'Existing lead', 'Generic renderer and Conditions', 'Audit Findings']) expect(html).toContain(text)
})

it('does not use the spec replacement in a drawer or on Overview', () => {
  const replacement = vi.fn(() => <div>Purpose-built configuration</div>)
  let html = renderToStaticMarkup(<WorkloadView {...props} expanded={false} specTab={{ render: replacement }} />)
  expect(html).toContain('Composed Overview')
  html = renderToStaticMarkup(<WorkloadView {...props} activeTab="overview" specTab={{ render: replacement }} />)
  expect(html).toContain('Composed Overview')
  expect(replacement).not.toHaveBeenCalled()
})

it('still opens the original Spec & status in a drawer when a page replacement is provided', () => {
  const replacement = vi.fn(() => <div>Purpose-built configuration</div>)
  const host = document.createElement('div')
  const root = createRoot(host)
  act(() => root.render(<WorkloadView {...props} expanded={false} specTab={{ label: 'Configuration', render: replacement }} />))
  const tab = [...host.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((b) => b.textContent === 'Spec & status')!
  act(() => tab.click())
  expect(tab.getAttribute('aria-selected')).toBe('true')
  expect(host.textContent).toContain('Generic renderer and Conditions')
  expect(host.textContent).toContain('Audit Findings')
  expect(replacement).not.toHaveBeenCalled()
  act(() => root.unmount())
})

it('lets a host render the reported phase while preserving the default derived badge', () => {
  const instance = { ...resource, status: { phase: 'Cluster in healthy state', readyInstances: 1 } }
  for (const expanded of [true, false]) {
    const html = renderToStaticMarkup(<WorkloadView {...props} expanded={expanded} resource={instance} renderStatusBadge={(r) => <span>Phase: {r.status.phase}</span>} />)
    expect(html).toContain('Phase: Cluster in healthy state')
    expect(html).not.toMatch(/badge[^>]*>Degraded/)
    expect(renderToStaticMarkup(<WorkloadView {...props} expanded={expanded} resource={instance} />)).toMatch(/badge[^>]*>Degraded/)
  }
})

it('waits for the resource before asking a host for its status badge', () => {
  const badge = vi.fn((r: any) => <span>Phase: {r.status.phase}</span>)
  for (const expanded of [true, false]) {
    expect(() => renderToStaticMarkup(<WorkloadView {...props} expanded={expanded} resource={undefined} renderStatusBadge={badge} />)).not.toThrow()
  }
  expect(badge).not.toHaveBeenCalled()
})

it.each(['ScheduledBackup', 'Database', 'Publication', 'ClusterImageCatalog', 'Deployment', 'Pod', 'Pooler'])('keeps %s drawer utilities apart from identity and object actions', (kind) => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const name = 'a-long-object-name-that-needs-the-full-drawer-width'
  act(() => root.render(<WorkloadView {...props} name={name} resource={{ ...resource, kind }} expanded={false} onExpand={() => {}} onClose={() => {}} renderStatusBadge={() => <span>Reported status</span>} renderHeaderActions={() => <button>Object action</button>} />))
  const expand = host.querySelector('[aria-label="Open full view"]')!
  const utilityRow = expand.parentElement!.parentElement!
  expect(utilityRow.classList.contains('shrink-0')).toBe(true)
  expect(utilityRow.classList.contains('flex-nowrap')).toBe(true)
  expect(utilityRow.querySelector('[aria-label="Refresh"]')).not.toBeNull()
  expect(utilityRow.querySelector('[aria-label="Close"]')).not.toBeNull()
  expect(utilityRow.textContent).not.toContain('Object action')
  // The name has its own line under the badges and controls, so a long one gets the drawer's full width.
  const title = host.querySelector('h2')!
  expect(title.textContent).toBe(name)
  const topRow = utilityRow.parentElement!
  expect(topRow.contains(title)).toBe(false)
  expect(topRow.textContent).toContain('Reported status')
  const action = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Object action')!
  expect(topRow.contains(action)).toBe(false)
  expect(title.parentElement!.contains(action)).toBe(false)
  act(() => root.unmount())
})

it('preserves Diagnose before the resource loads while deferring resource-dependent actions', () => {
  const header = vi.fn(() => <button>Resource action</button>)
  const html = renderToStaticMarkup(<WorkloadView {...props} resource={undefined} expanded={false} renderHeaderActions={header} actionsBarProps={{ renderDiagnose: () => <button>Diagnose this object</button> }} />)
  expect(html).toContain('Diagnose this object')
  expect(header).not.toHaveBeenCalled()
})

it.each(['Deployment', 'Pod', 'ScheduledBackup', 'Database', 'Pooler'])('groups Diagnose with %s drawer utilities and omits empty object actions', (kind) => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const header = vi.fn(() => null)
  const group = kind === 'Deployment' ? 'apps' : kind === 'Pod' ? '' : 'postgresql.cnpg.io'
  const data = { ...resource, kind, apiVersion: group ? `${group}/v1` : 'v1' }
  act(() => root.render(<WorkloadView {...props} kind={`${kind.toLowerCase()}s`} group={group} resource={data} expanded={false} onExpand={() => {}} onClose={() => {}} renderHeaderActions={header} actionsBarProps={{ renderDiagnose: () => <button>Diagnose</button> }} />))
  const utility = host.querySelector('[aria-label="Open full view"]')!.parentElement!.parentElement!
  expect(utility.textContent).toContain('Diagnose')
  expect(host.querySelector('[data-action-group=object]')!.matches(':empty')).toBe(true)
  expect(header).toHaveBeenCalledTimes(1)
  act(() => root.render(<WorkloadView {...props} kind={`${kind.toLowerCase()}s`} group={group} resource={data} expanded renderHeaderActions={header} actionsBarProps={{ renderDiagnose: () => <button>Diagnose</button> }} />))
  const refresh = host.querySelector('[aria-label="Refresh"]')!
  expect(refresh.parentElement!.parentElement!.textContent).toContain('Diagnose')
  act(() => root.unmount())
})

it('hides the object action band when a host action component has no content yet', () => {
  const EmptyAction = () => null
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<WorkloadView {...props} expanded={false} renderHeaderActions={() => <EmptyAction />} />))
  const row = host.querySelector('[data-action-group=object]')!
  expect(row.matches(':empty')).toBe(true)
  expect(row.classList.contains('empty:hidden')).toBe(true)
  act(() => root.render(<WorkloadView {...props} expanded={false} renderHeaderActions={() => <button>Run now</button>} />))
  expect(row.matches(':empty')).toBe(false)
  act(() => root.unmount())
})

it.each(['Deployment', 'Pod', 'Pooler', 'ScheduledBackup'])('groups %s drawer actions with utilities in one toolbar', (kind) => {
  const host = document.createElement('div'); const root = createRoot(host)
  const apiKind = kind === 'Deployment' ? 'deployments' : kind === 'Pod' ? 'pods' : kind === 'Pooler' ? 'poolers' : 'scheduledbackups'
  const group = kind === 'Deployment' ? 'apps' : kind === 'Pod' ? '' : 'postgresql.cnpg.io'
  const data = { ...resource, kind, apiVersion: group ? `${group}/v1` : 'v1', spec: { containers: [{ name: 'postgres', image: 'postgres' }] }, status: { phase: 'Running' } }
  act(() => root.render(<WorkloadView {...props} kind={apiKind} group={group} expanded={false} resource={data} actionsBarProps={{ canExec: true, onOpenTerminal: () => {}, onRestart: () => {} }} renderHeaderActions={() => kind === 'ScheduledBackup' ? <><div><button>Suspend</button><button>Run now</button><button>Edit schedule</button></div><div>Run blocked: no backup destination</div></> : <button>{kind === 'Pooler' ? 'Pause' : 'Object action'}</button>} />))
  const object = host.querySelector('[data-action-group=object]')!
  const utility = host.querySelector('[data-action-group=utility]')!
  expect(object.parentElement).toBe(utility.parentElement)
  expect(object.parentElement!.classList.contains('flex-wrap')).toBe(true)
  expect(utility.classList.contains('ml-auto')).toBe(true)
  expect(object.querySelector('button')).not.toBeNull()
  expect(utility.textContent).toContain('YAML')
  if (kind === 'Pod') expect(object.textContent).toContain('Terminal')
  if (kind === 'Deployment') expect(object.textContent).toContain('Restart')
  if (kind === 'ScheduledBackup') { expect(object.querySelectorAll('button')).toHaveLength(3); expect(object.textContent).toContain('Run blocked: no backup destination') }
  act(() => root.unmount())
})

it('does not carry a picked Pod to the next resource the view shows', () => {
  const pods = (name: string) => ({ pods: [{ kind: 'Pod', namespace: 'db', name }] })
  const seen: (string | null)[] = []
  const Probe = ({ selectedPod, onSelectPod }: { selectedPod: string | null; onSelectPod: (pod: string | null) => void }) => {
    useEffect(() => { seen.push(selectedPod) })
    return <button data-pick onClick={() => onSelectPod('pg-1')}>pick</button>
  }
  const renderLogsTab = (tab: { selectedPod: string | null; onSelectPod: (pod: string | null) => void }) => <Probe {...tab} />
  const host = document.createElement('div')
  const root = createRoot(host)
  act(() => root.render(<WorkloadView {...props} activeTab="logs" relationships={pods('pg-1')} renderLogsTab={renderLogsTab} />))
  act(() => host.querySelector<HTMLButtonElement>('[data-pick]')!.click())
  expect(seen.at(-1)).toBe('pg-1')
  const before = seen.length
  act(() => root.render(<WorkloadView {...props} name="other" activeTab="logs" relationships={pods('other-1')} renderLogsTab={renderLogsTab} />))
  expect(seen.slice(before)).not.toContain('pg-1')
  act(() => root.unmount())
})

it('does not carry a picked Pod to a recreated resource with the same name', () => {
  const relationships = { pods: [{ kind: 'Pod', namespace: 'db', name: 'pg-1' }] }
  const seen: (string | null)[] = []
  const Probe = ({ selectedPod, onSelectPod }: { selectedPod: string | null; onSelectPod: (pod: string | null) => void }) => {
    useEffect(() => { seen.push(selectedPod) })
    return <button data-pick onClick={() => onSelectPod('pg-1')}>pick</button>
  }
  const renderLogsTab = (tab: { selectedPod: string | null; onSelectPod: (pod: string | null) => void }) => <Probe {...tab} />
  const host = document.createElement('div')
  const root = createRoot(host)
  const withUID = (uid: string) => ({ ...resource, kind: 'Deployment', apiVersion: 'apps/v1', metadata: { ...resource.metadata, uid } })
  act(() => root.render(<WorkloadView {...props} kind="deployments" group={undefined} resource={withUID('a')} activeTab="logs" relationships={relationships} renderLogsTab={renderLogsTab} />))
  act(() => host.querySelector<HTMLButtonElement>('[data-pick]')!.click())
  act(() => root.render(<WorkloadView {...props} kind="deployments" group="apps" resource={withUID('a')} activeTab="logs" relationships={relationships} renderLogsTab={renderLogsTab} />))
  expect(seen.at(-1)).toBe('pg-1')
  const before = seen.length
  act(() => root.render(<WorkloadView {...props} kind="deployments" group="apps" resource={withUID('b')} activeTab="logs" relationships={relationships} renderLogsTab={renderLogsTab} />))
  expect(seen.slice(before)).not.toContain('pg-1')
  act(() => root.unmount())
})

it.each([
  ['Deployment', 'deployments', 'apps'],
  ['Cluster', 'clusters', 'postgresql.cnpg.io'],
])('preserves %s panel state on first group resolution and resets it for a new identity or tab', (kind, apiKind, group) => {
  function Panel() {
    const [edited, setEdited] = useState(false)
    return <button data-edit onClick={() => setEdited(true)}>{edited ? 'Edited' : 'New'}</button>
  }
  const tabs = ['settings', 'runs'].map(id => ({ id, label: id, render: () => <Panel /> }))
  const host = document.createElement('div')
  const root = createRoot(host)
  function render(routeGroup: string | undefined, uid?: string, tab = 'settings') {
    const workload = uid ? {
      kind, apiVersion: `${routeGroup || group}/v1`, metadata: { name: 'web', namespace: 'db', uid },
    } : undefined
    act(() => root.render(<WorkloadView {...props} name="web" kind={apiKind} resource={workload}
      group={routeGroup} activeTab={tab} extraTabs={tabs} />))
  }
  const panel = () => host.querySelector<HTMLButtonElement>('[data-edit]')!
  render(undefined)
  act(() => panel().click())
  render(group, 'first')
  expect(panel().textContent).toBe('Edited')
  render('', 'first')
  expect(panel().textContent).toBe('Edited')
  render('', 'recreated')
  expect(panel().textContent).toBe('New')
  act(() => panel().click())
  render('example.io', 'recreated')
  expect(panel().textContent).toBe('New')
  act(() => panel().click())
  const scrollContainer = panel().parentElement
  render('example.io', 'recreated', 'runs')
  expect(panel().textContent).toBe('New')
  expect(panel().parentElement).not.toBe(scrollContainer)
  act(() => root.unmount())
})

it('keeps an asynchronous deep link and reconciles an explicit built-in replacement', () => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const onTabChange = vi.fn()
  const extraTabs = [{ id: 'runs', label: 'Runs', replaces: 'spec' as const, render: () => <div>The runs</div> }]
  act(() => root.render(<WorkloadView {...props} expanded activeTab="runs" onTabChange={onTabChange} />))
  expect(host.textContent).toContain('Composed Overview')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...props} expanded activeTab="runs" extraTabsComplete={false} onTabChange={onTabChange} />))
  expect(host.textContent).toContain('Composed Overview')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...props} expanded activeTab="runs" extraTabs={extraTabs} onTabChange={onTabChange} />))
  expect(host.textContent).toContain('The runs')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...props} expanded activeTab="spec" extraTabs={extraTabs} onTabChange={onTabChange} />))
  expect(onTabChange).toHaveBeenCalledWith('runs', { replace: true })
  act(() => root.unmount())
})

it.each(['found', 'missing'] as const)('keeps the requested Timeline until the Cluster read settles as %s', outcome => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const onTabChange = vi.fn()
  const extraTabs = [{ id: 'activity', label: 'Activity', replaces: 'timeline' as const, render: () => 'Cluster activity' }]
  function Page({ loading }: { loading: boolean }) {
    const [tab, setTab] = useState('timeline')
    const missing = !loading && outcome === 'missing'
    return <WorkloadView {...props} activeTab={tab} extraTabs={extraTabs} isLoading={loading}
      resource={loading || missing ? undefined : resource}
      resourceError={missing ? Object.assign(new Error('not found'), { status: 404 }) : undefined}
      onTabChange={(next, options) => { onTabChange(next, options); setTab(next) }} />
  }
  act(() => root.render(<Page loading />))
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<Page loading={false} />))
  const selected = host.querySelector('[role="tab"][aria-selected="true"]')
  if (outcome === 'missing') {
    expect(selected?.textContent).toBe('Timeline')
    expect(onTabChange).not.toHaveBeenCalled()
  } else {
    expect(selected?.textContent).toBe('Activity')
    expect(onTabChange).toHaveBeenCalledExactlyOnceWith('activity', { replace: true })
  }
  act(() => root.unmount())
})

it.each([{}, { extraTabsComplete: false }])('keeps a hidden Spec link for a late replacement with %j', completeness => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const onTabChange = vi.fn()
  const base = { ...props, ...completeness, renderSummary: undefined, onTabChange }
  act(() => root.render(<WorkloadView {...base} />))
  expect(host.querySelector('[aria-selected="true"]')?.textContent).toBe('Overview')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...base}
    extraTabs={[{ id: 'settings', label: 'Settings', replaces: 'spec', render: () => 'Cluster settings' }]} />))
  expect(host.textContent).toContain('Cluster settings')
  expect(onTabChange).toHaveBeenCalledExactlyOnceWith('settings', { replace: true })
  act(() => root.render(<WorkloadView {...base} extraTabsComplete />))
  expect(onTabChange).toHaveBeenLastCalledWith('overview', { replace: true })
  act(() => root.unmount())
})

it('repairs unknown tab IDs once the host list is complete and the workload read settles', () => {
  const root = createRoot(document.createElement('div'))
  const onTabChange = vi.fn()
  const render = (activeTab: string, extraTabsComplete: boolean, isLoading: boolean) => act(() => root.render(
    <WorkloadView {...props} expanded activeTab={activeTab} extraTabsComplete={extraTabsComplete}
      isLoading={isLoading} onTabChange={onTabChange} />,
  ))
  render('removed', false, false)
  expect(onTabChange).not.toHaveBeenCalled()
  render('removed', true, true)
  expect(onTabChange).not.toHaveBeenCalled()
  render('removed', true, false)
  expect(onTabChange).toHaveBeenCalledExactlyOnceWith('overview', { replace: true })
  render('also-removed', true, false)
  expect(onTabChange).toHaveBeenCalledTimes(2)
  act(() => root.unmount())
})

it('waits until the expanded view is active to repair a stale Deploys link', () => {
  const root = createRoot(document.createElement('div'))
  const onTabChange = vi.fn()
  const render = (active: boolean) => act(() => root.render(
    <WorkloadView {...props} expanded active={active} activeTab="deploys" extraTabs={[]} extraTabsComplete onTabChange={onTabChange} />,
  ))
  render(false)
  expect(onTabChange).not.toHaveBeenCalled()
  render(true)
  expect(onTabChange).toHaveBeenCalledExactlyOnceWith('overview', { replace: true })
  act(() => root.unmount())
})

it.each([['cold', undefined], ['cached', resource]] as const)('gates host tabs on a %s 404 and preserves the requested tab for recovery', (_, cached) => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const onTabChange = vi.fn()
  const extraTabs = [
    { id: 'deploys', label: 'Deploys', render: () => 'Deploy history', availableWhenDeleted: true, replaces: 'spec' as const },
    { id: 'runs', label: 'Runs', render: () => 'Run history' },
  ]
  const gone = Object.assign(new Error('not found'), { status: 404 })
  const hosted = { ...props, extraTabs, extraTabsComplete: true, onTabChange }
  act(() => root.render(<WorkloadView {...hosted} activeTab="runs" resource={cached} resourceError={gone} />))
  expect([...host.querySelectorAll('[role="tab"]')].map(tab => tab.textContent)).not.toContain('Runs')
  expect(host.textContent).not.toContain('Run history')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...hosted} activeTab="runs" />))
  expect(host.textContent).toContain('Run history')
  act(() => root.render(<WorkloadView {...hosted} activeTab="deploys" resource={cached} resourceError={gone} />))
  expect(host.textContent).toContain('Deploy history')
  expect(onTabChange).not.toHaveBeenCalled()
  act(() => root.render(<WorkloadView {...hosted} activeTab="spec" resource={cached} resourceError={gone} />))
  expect(host.textContent).toContain('Deploy history')
  expect(onTabChange).toHaveBeenCalledExactlyOnceWith('deploys', { replace: true })
  act(() => root.unmount())
})

it('restores an uncontrolled workload tab when its data becomes available again', () => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const render = (available: boolean) => act(() => root.render(
    <WorkloadView {...props} activeTab={undefined} isMetricsAvailable={() => available}
      renderMetricsTab={() => <div>Metrics charts</div>} />,
  ))
  render(true)
  const metrics = [...host.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((tab) => tab.textContent === 'Metrics')!
  act(() => metrics.click())
  expect(host.textContent).toContain('Metrics charts')
  render(false)
  expect(host.textContent).not.toContain('Metrics charts')
  render(true)
  expect(host.textContent).toContain('Metrics charts')
  act(() => root.unmount())
})

it('uses the singular replica label for a single replica', () => {
  const deployment = { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'shop' }, spec: { replicas: 1, strategy: { type: 'RollingUpdate' }, template: { spec: { containers: [{ name: 'web', image: 'web:1' }] } } }, status: { replicas: 1, readyReplicas: 1, updatedReplicas: 1, availableReplicas: 1 } }
  const base = { ...props, kind: 'deployments', group: 'apps', name: 'web', namespace: 'shop', resource: deployment, activeTab: 'overview' as const, renderSummary: undefined, expanded: true }
  const html = renderToStaticMarkup(<WorkloadView {...base} />)
  expect(html).toContain('1 replica<')
  expect(html).not.toContain('1 replicas')
})
