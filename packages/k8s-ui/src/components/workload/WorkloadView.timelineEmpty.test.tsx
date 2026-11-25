// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { formatRelativeAgeTime } from '../../utils/format'
import { WorkloadView } from './WorkloadView'

const props: ComponentProps<typeof WorkloadView> = {
  kind: 'configmaps', namespace: 'demo', name: 'settings',
  resource: { apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: 'settings', namespace: 'demo' }, data: {} },
  onBack: () => {}, activeTab: 'timeline', historyScoped: true, allEvents: [],
}

function render(overrides: Partial<Omit<typeof props, 'extraTabs' | 'onTabChange'>> = {}) {
  const host = document.createElement('div')
  host.innerHTML = renderToStaticMarkup(<WorkloadView {...props} {...overrides} />)
  return host
}

function expectSingleEmpty(host: HTMLElement) {
  expect(host.textContent?.match(/No recorded activity/g)).toHaveLength(1)
  for (const old of ['No events yet', 'No activity found', 'Events will appear', 'Activity will appear']) {
    expect(host.textContent).not.toContain(old)
  }
  expect(host.querySelector('[data-event-id]')).toBeNull()
  expect(host.textContent).not.toContain('Now')
  expect([...host.querySelectorAll('span')].some((span) => span.textContent === 'Resource')).toBe(false)
}

it.each([
  ['ConfigMap', 'v1', 'Changes to this ConfigMap.'],
  ['Deployment', 'apps/v1', 'This Deployment, its recorded children and attached resources.'],
  ['CronJob', 'batch/v1', 'This CronJob, its recorded children and attached resources.'],
  ['Agent', 'example.io/v1', 'Changes to this Agent.'],
  ['Deployment', 'example.io/v1', 'Changes to this Deployment.'],
])('describes the certain history scope for a %s in %s', (kind, apiVersion, scope) => {
  const host = render({ kind: `${kind.toLowerCase()}s`, resource: { ...props.resource, kind, apiVersion } })
  expectSingleEmpty(host)
  expect(host.textContent).toContain('No changes were found in the history Radar holds for this resource.')
  expect(host.textContent).toContain(scope)
  expect(host.textContent).not.toContain('what it runs')
  expect(host.textContent).not.toContain('complete history')
  expect(host.textContent).not.toContain('since')
})

it('keeps the dated history boundary and both missing-history reasons inside the empty state', () => {
  const since = '2026-11-03T05:00:00Z'
  const host = render({
    historyIncomplete: true,
    historyCoverage: { incomplete: true, complete_since: since, reasons: ['observation_started_late', 'events_evicted'] },
  })
  expectSingleEmpty(host)
  const body = [...host.querySelectorAll('p')].find((p) => p.textContent?.startsWith('No changes were found'))!.parentElement!
  expect(body.textContent).toContain(`Changes before ${formatRelativeAgeTime(since)} may be missing`)
  expect(body.textContent).toContain('recording started later and older history was deleted')
  expect(body.querySelector('[title]')?.getAttribute('title')).toBe(new Date(since).toLocaleString())
  expect(body.querySelector('[role=note]')?.className).toContain('border-t')
  expect(body.querySelector('[role=note]')?.className).toContain('text-left')
  expect(body.textContent).toContain('Some past runs or related resources may be missing.')
})

it('does not invent a history start time when it is withheld, and preserves access gaps', () => {
  const host = render({
    historyCoverage: { incomplete: true, reasons: ['observation_started_late'] },
    historyGaps: [{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'Workload', group: 'kueue.x-k8s.io' }] } }],
  })
  expectSingleEmpty(host)
  expect(host.textContent).toContain('Earlier changes may be missing')
  expect(host.textContent).toContain("Changes to Kueue Workloads aren't shown: you don't have access to list them in this namespace.")
  expect(host.textContent).not.toContain('Changes before')
})

it('preserves loading and errors instead of claiming an empty history', () => {
  expect(render({ eventsLoading: true }).textContent).toContain('Loading events')
  const host = render({ historyError: new Error('connection lost'), onRetryHistory: () => {} })
  expect(host.textContent).toContain("Couldn't load this workload's history")
  expect(host.textContent).toContain('connection lost')
  expect(host.textContent).toContain('Try again')
  expect(host.textContent).not.toContain('No recorded activity')
})

it('lets an empty readable page load older history without rendering two empty states', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const host = document.createElement('div')
  const root = createRoot(host)
  const loadOlder = vi.fn()
  try {
    await act(async () => root.render(<WorkloadView {...props} historyTruncated onLoadOlderHistory={loadOlder} />))
    expectSingleEmpty(host)
    expect(host.textContent).toContain('Older history exists.')
    const button = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Load older')!
    await act(async () => button.click())
    expect(loadOlder).toHaveBeenCalledTimes(1)
  } finally {
    await act(async () => root.unmount())
    vi.unstubAllGlobals()
  }
})

it('keeps the lanes and list when activity exists', () => {
  const host = render({ allEvents: [{
    id: 'change', kind: 'ConfigMap', namespace: 'demo', name: 'settings', apiVersion: 'v1',
    timestamp: new Date(Date.now() - 60_000).toISOString(), source: 'informer', eventType: 'update',
  }] })
  expect(host.textContent).not.toContain('No recorded activity')
  expect(host.textContent).toContain('Now')
  expect(host.textContent).not.toContain('No events yet')
  expect(host.textContent).not.toContain('No activity found')
})

it('explains unusable timestamps without claiming that nothing was recorded', () => {
  const host = render({ allEvents: [{
    id: 'invalid', kind: 'ConfigMap', namespace: 'demo', name: 'settings', apiVersion: 'v1',
    timestamp: '0001-01-01T00:00:00Z', source: 'informer', eventType: 'update',
  }] })
  expectSingleEmpty(host)
  expect(host.textContent).toContain('The recorded events have no usable timestamps to display.')
  expect(host.textContent).not.toContain('No changes were found')
})
