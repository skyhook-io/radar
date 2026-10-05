// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CNPGClusterSummary, CNPGDimensionMark, CNPGDimensionVerdict, CNPGServingStatus } from './CNPGClusterSummary'
import type { CNPGFleetRow, CNPGProblem } from './workspace'
import type { CNPGDimension } from './ha'
import { OpenIssueContext } from '../problems'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function row(over: Partial<CNPGFleetRow> = {}): CNPGFleetRow {
  return {
    key: 'db/pg',
    namespace: 'db',
    name: 'pg',
    cluster: { status: { currentPrimary: 'pg-1' } },
    controllerStatus: { text: 'Healthy', level: 'healthy' },
    instances: { ready: 2, desired: 2 },
    pods: [
      { name: 'pg-1', role: 'primary', ready: true },
      { name: 'pg-2', role: 'replica', ready: true },
    ],
    replicaCluster: null,
    hibernated: false,
    pgVersion: '17',
    catalog: null,
    replication: { text: 'Lag unknown', tone: 'unknown' },
    protection: {
      schedule: { text: 'Active', tone: 'healthy', names: [] },
      destination: { text: 'ObjectStore', tone: 'healthy', method: 'plugin' },
      lastSuccessfulBackup: { text: '1 h ago', tone: 'healthy' },
      walArchiving: { text: 'Archiving', tone: 'healthy' },
      recoveryWindow: { text: 'x', tone: 'healthy' },
      restoreValidation: { text: 'None recorded', tone: 'unknown' },
      summary: { text: 'ok', tone: 'healthy' },
    },
    declarations: { summary: { text: 'None', tone: 'neutral' }, total: 0, failed: 0, pending: 0 },
    poolers: [],
    poolersKnown: true,
    problems: [],
    attention: false,
    categories: new Set(),
    ...over,
  }
}

const problem = (id: string, severity: CNPGProblem['severity'], title: string): CNPGProblem => ({
  id,
  severity,
  category: 'availability',
  title,
  subject: { kind: 'Backup', group: 'postgresql.cnpg.io', namespace: 'db', name: `b-${id}` },
  source: 'issue',
})

function render(node: ReactNode) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => root.render(node))
  return root
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('CNPGClusterSummary', () => {
  it('opens the other problems in place when the host links nowhere else', () => {
    const onNavigate = vi.fn()
    const r = row({ problems: [problem('a', 'critical', 'WAL archiving failing'), problem('b', 'warning', 'Backup failed'), problem('c', 'posture', 'No schedule')], attention: true })
    const root = render(<CNPGClusterSummary row={r} onNavigate={onNavigate} />)
    const more = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('+2 more'))!
    expect(more.getAttribute('aria-expanded')).toBe('false')
    act(() => more.click())
    expect(more.getAttribute('aria-expanded')).toBe('true')
    expect(document.body.textContent).toContain('Backup failed')
    expect(document.body.textContent).toContain('No schedule')
    act(() => [...document.querySelectorAll('button')].find((b) => b.textContent === 'b-b')!.click())
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ kind: 'Backup', name: 'b-b' }))
    act(() => root.unmount())
  })
  it('can open with every problem listed', () => {
    const r = row({ problems: [problem('a', 'critical', 'WAL archiving failing'), problem('b', 'warning', 'Backup failed')], attention: true })
    const root = render(<CNPGClusterSummary row={r} initialProblemsExpanded />)
    const more = [...document.querySelectorAll('button')].find((b) => b.getAttribute('aria-expanded') !== null)!
    expect(more.getAttribute('aria-expanded')).toBe('true')
    act(() => root.unmount())
  })
  it('keeps a host link as the override', () => {
    const r = row({ problems: [problem('a', 'warning', 'One'), problem('b', 'warning', 'Two')], attention: true })
    const root = render(<CNPGClusterSummary row={r} problemsLink={(n) => <a href="#all">All {n}</a>} />)
    expect(document.body.textContent).toContain('All 2')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.includes('+1 more'))).toBe(false)
    act(() => root.unmount())
  })
  it('marks a tab only when its dimension needs a look or could not be assessed', () => {
    const dim = (tone: CNPGDimension['tone']): CNPGDimension => ({ id: 'protection', label: 'Backups', tone, text: 'verdict', source: 's' })
    for (const tone of ['healthy', 'neutral'] as const) {
      const root = render(<CNPGDimensionMark dimension={dim(tone)} />)
      expect(document.querySelector('[aria-label="Backups: verdict"]')).toBeNull()
      act(() => root.unmount())
    }
    for (const tone of ['degraded', 'unhealthy', 'unknown'] as const) {
      const root = render(<CNPGDimensionMark dimension={dim(tone)} />)
      expect(document.querySelector('[aria-label="Backups: verdict"]')).not.toBeNull()
      act(() => root.unmount())
    }
  })
  it("says on the tab why it is marked, and nothing when it is not", () => {
    const backups = (tone: CNPGDimension['tone']): CNPGDimension => ({ id: 'protection', label: 'Backups', tone, text: 'no backup destination', source: 'Cluster spec' })
    let root = render(<CNPGDimensionVerdict dimension={backups('degraded')} />)
    expect(document.body.textContent).toContain('no backup destination')
    expect(document.body.textContent).toContain('Cluster spec')
    act(() => root.unmount())
    root = render(<CNPGDimensionVerdict dimension={backups('healthy')} />)
    expect(document.body.textContent).toBe('')
    act(() => root.unmount())
  })
  it('draws an unassessed dimension as a ring, never a calm dot', () => {
    const root = render(<CNPGDimensionMark dimension={{ id: 'storage', label: 'Storage', tone: 'unknown', text: 'unassessed', source: 's' }} />)
    const mark = document.querySelector('[aria-label="Storage: unassessed"]')!
    expect(mark.querySelector('.rounded-full.border')).not.toBeNull()
    expect(mark.querySelector('.rounded-full:not(.border)')).toBeNull()
    act(() => root.unmount())
  })
  it('makes the Serving status a button only when the host can open its details', () => {
    const serving: CNPGDimension = { id: 'serving', label: 'Serving', tone: 'healthy', text: 'primary ready', source: 's' }
    const onSelect = vi.fn()
    let root = render(<CNPGServingStatus dimension={serving} />)
    expect(document.querySelector('button')).toBeNull()
    expect(document.body.textContent).toContain('primary ready')
    act(() => root.unmount())
    root = render(<CNPGServingStatus dimension={serving} onSelect={onSelect} />)
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="Serving: primary ready. Open its details"]')!.click())
    expect(onSelect).toHaveBeenCalled()
    act(() => root.unmount())
  })
  it('lists each dimension at a glance, opening its tab', () => {
    const dims: CNPGDimension[] = [{ id: 'storage', label: 'Storage', tone: 'degraded', text: 'WAL held by an inactive slot', source: 'slot' }]
    const onSelect = vi.fn()
    const root = render(<CNPGClusterSummary row={row()} dimensions={dims} onSelectDimension={onSelect} />)
    expect(document.body.textContent).toContain('WAL held by an inactive slot')
    const open = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Storage →')!
    act(() => open.click())
    expect(onSelect).toHaveBeenCalledWith('storage')
    act(() => root.unmount())
  })
})

describe('problem meta', () => {
  it('keeps a space between the Backup and "and N more"', () => {
    const r = row({
      problems: [{ ...problem('a', 'warning', '3 backups failed'), alsoAbout: [{ kind: 'Backup', name: 'b-2' }, { kind: 'Backup', name: 'b-1' }] }],
      attention: true,
    })
    const root = render(<CNPGClusterSummary row={r} onNavigate={() => {}} />)
    expect(document.body.textContent).toContain('b-a and 2 more')
    act(() => root.unmount())
  })
})

describe('problem provenance', () => {
  it('names the origin instead of "Radar issue" and links to Issues when the host can', () => {
    const open = vi.fn()
    const r = row({
      problems: [{ ...problem('a', 'critical', 'WAL archiving is failing'), subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }, origin: { label: 'Reported by CNPG', detail: 'ContinuousArchiving condition' } }],
      attention: true,
    })
    const root = render(
      <OpenIssueContext.Provider value={open}>
        <CNPGClusterSummary row={r} />
      </OpenIssueContext.Provider>,
    )
    expect(document.body.textContent).toContain('Reported by CNPG')
    expect(document.body.textContent).not.toContain('Radar issue')
    act(() => [...document.querySelectorAll('button')].find((b) => b.textContent === 'See in Issues →')!.click())
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: 'a' }))
    act(() => root.unmount())
  })
  it('offers no Issues link without a host handler', () => {
    const r = row({ problems: [problem('a', 'warning', 'Backup failed')], attention: true })
    const root = render(<CNPGClusterSummary row={r} />)
    expect(document.body.textContent).toContain('Detected by Radar')
    expect(document.body.textContent).not.toContain('See in Issues')
    act(() => root.unmount())
  })
})
