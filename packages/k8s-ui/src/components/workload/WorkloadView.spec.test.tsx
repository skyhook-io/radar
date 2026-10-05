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
