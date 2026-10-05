// @vitest-environment jsdom
import { act } from 'react'
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
  const identity = host.querySelector('h2')!.parentElement!
  expect(identity.textContent).toContain(name)
  expect(identity.textContent).toContain('Reported status')
  const action = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Object action')!
  expect(identity.contains(action)).toBe(false)
  expect(utilityRow.contains(action)).toBe(false)
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
  expect(host.querySelector('.px-4.pb-2.justify-end')).toBeNull()
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
  const row = host.querySelector('.px-4.pb-2.justify-end')!
  expect(row.matches(':empty')).toBe(true)
  expect(row.classList.contains('empty:hidden')).toBe(true)
  act(() => root.render(<WorkloadView {...props} expanded={false} renderHeaderActions={() => <button>Run now</button>} />))
  expect(row.matches(':empty')).toBe(false)
  act(() => root.unmount())
})
