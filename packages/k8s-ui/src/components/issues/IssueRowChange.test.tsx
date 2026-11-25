// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { IssueRow, IssuesView, issueRowKey } from './IssuesView'
import { ResourceIssuesSection } from './ResourceIssuesSection'
import { rowChangeVerb } from './change-verb'
import { recentChangeTimelineTarget, type IssueExpansion, type IssueTimelineEventTarget } from './IssueRecentChanges'
import type { Issue, IssueRecentChange, IssueRowChange } from './types'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const FIRST_SEEN = new Date(Date.now() - 600_000).toISOString()
const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

const issue: Issue = {
  id: 'crash-api',
  severity: 'critical',
  source: 'problem',
  category: 'crashloop',
  category_group: 'runtime',
  grouping_scope: 'workload',
  kind: 'Deployment',
  group: 'apps',
  namespace: 'shop',
  name: 'api',
  reason: 'CrashLoopBackOff',
  cause: 'Container api exits with code 1 shortly after start.',
  action: 'Inspect the previous container logs.',
  raw_message: 'back-off restarting failed container api',
  first_seen: FIRST_SEEN,
  change_context: { changed: true, what: 'image_change', when: '5m', evidence: 'ReplicaSet api-7d9 replaced api-5c4' },
  diagnostic_context: { role: 'candidate', facts: [{ type: 'pvc_blast_radius', message: 'Blocks every Pod that mounts this claim.' }] },
}

const imageChange: IssueRecentChange = {
  kind: 'Deployment', apiVersion: 'apps/v1', namespace: 'shop', name: 'api', changeType: 'update', timestamp: minutesAgo(14),
  fields: [{ path: 'spec.template.spec.containers[api].image' }], event_id: 'ev-image',
}
const configChange: IssueRecentChange = {
  kind: 'ConfigMap', apiVersion: 'v1', namespace: 'shop', name: 'api-config', changeType: 'update', timestamp: minutesAgo(16),
  fields: [{ path: 'data (modified keys)' }], event_id: 'ev-config',
}
const helmChange: IssueRecentChange = {
  source: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'api', changeType: 'helm_release_revision', timestamp: minutesAgo(20),
  fields: [{ path: 'helm.revision', newValue: 4 }],
}

const rowChange = (change: IssueRecentChange, occurrence: Partial<IssueRowChange> = { first_seen: FIRST_SEEN }): IssueRowChange => ({ ...occurrence, change })
// The note's visible text: everything outside its tags.
const textBetweenTags = (html: string) => Array.from(html.matchAll(/(?:^|>)([^<]*)/g), (m) => m[1]).join('')
const note = (html: string) => {
  const inner = html.match(/data-row-change[^>]*>(.*?)<\/span><\/span>/)?.[1]
  return inner === undefined ? null : textBetweenTags(inner)
}

describe('the change note on a collapsed row', () => {
  const row = (props: Partial<Parameters<typeof IssueRow>[0]> = {}) =>
    renderToString(<IssueRow issue={issue} open={false} onToggle={() => {}} {...props} />)

  it('names the change and when it happened, mid-sentence', () => {
    expect(note(row({ rowChange: rowChange(imageChange) }))).toBe('image changed 14m ago')
    expect(note(row({ rowChange: rowChange(configChange) }))).toBe('config changed 16m ago')
    expect(rowChangeVerb(helmChange)).toBe('Helm upgraded')
    expect(rowChangeVerb({ ...imageChange, fields: [{ path: 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]' }] })).toBe('restarted')
  })

  it('matches the occurrence by instant when the answer writes it in another time zone', () => {
    const plusTwo = new Date(Date.parse(FIRST_SEEN) + 2 * 3_600_000).toISOString().replace('Z', '+02:00')
    expect(note(row({ rowChange: rowChange(imageChange, { first_seen: plusTwo }) }))).toBe('image changed 14m ago')
  })

  it('shows nothing for an answer read for another occurrence of the issue', () => {
    expect(note(row({ rowChange: rowChange(imageChange, { first_seen: minutesAgo(90) }) }))).toBeNull()
    expect(note(row({ rowChange: rowChange(imageChange, {}) }))).toBeNull()
  })

  it('renders exactly as before when the host passes nothing', () => {
    expect(row({ rowChange: undefined })).toBe(row())
    expect(row()).not.toContain('data-row-change')
  })
})

describe('one time label for a change', () => {
  it('says the same age on the collapsed row and in the section', () => {
    const at = new Date(Date.now() - (4 * 60 + 40) * 1000).toISOString()
    const change = { ...imageChange, timestamp: at }
    const noFirstSeen = { ...issue, first_seen: undefined, onset_unknown: true }
    const collapsed = renderToString(<IssueRow issue={noFirstSeen} open={false} onToggle={() => {}} rowChange={{ change }} />)
    const opened = renderToString(<IssueRow issue={noFirstSeen} open onToggle={() => {}} rowChange={{ change }}
      expandedIssue={{ issue: { ...noFirstSeen, correlation: { window_from: minutesAgo(1440), window_to: minutesAgo(0) }, correlated_changes: [change] }, recentChanges: { status: 'loaded' } }} />)
    const rowAge = note(collapsed)?.replace(/^image changed /, '')
    const sectionAge = opened.match(/<time[^>]*dateTime="[^"]*"[^>]*tabindex="0"[^>]*>(.*?)<span/i)?.[1].replace(/<!-- -->/g, '')
    expect(rowAge).toBeTruthy()
    expect(sectionAge).toBe(rowAge)
  })
})

describe('the expanded issue', () => {
  const loaded = (fresh: Issue): IssueExpansion => ({ issue: fresh, recentChanges: { status: 'loaded' } })
  const withChanges = { ...issue, correlation: { window_from: minutesAgo(70), window_to: minutesAgo(0) }, correlated_changes: [imageChange] }
  const open = (expandedIssue?: IssueExpansion, onTimelineEventClick?: (target: IssueTimelineEventTarget) => void) =>
    renderToString(<IssueRow issue={issue} open onToggle={() => {}} expandedIssue={expandedIssue} onResourceClick={() => {}}
      timelineLinks={onTimelineEventClick ? { onTimelineEventClick } : undefined} />)
  const changeContextShown = (html: string) => html.includes('ReplicaSet api-7d9 replaced api-5c4')

  it("puts Recent changes right under What's wrong, before Next step, Raw error, Context and the subject", () => {
    const html = open(loaded(withChanges))
    const order = ["What&#x27;s wrong", 'Recent changes', 'Next step', 'Raw error', 'Context', 'Subject'].map((label) => html.indexOf(label))
    expect(order.every((index) => index >= 0)).toBe(true)
    expect([...order].sort((a, b) => a - b)).toEqual(order)
  })

  it('drops the old change line from What\'s wrong only while the section shows the correlation', () => {
    expect(changeContextShown(open(loaded(withChanges)))).toBe(false)
    expect(changeContextShown(open({ recentChanges: { status: 'loading' } }))).toBe(false)
    expect(changeContextShown(open(loaded({ ...issue, correlation: { window_from: minutesAgo(70), window_to: minutesAgo(0) }, no_recent_changes: { window_seconds: 4200 } })))).toBe(false)
    // No host (Radar Hub's fleet page), a Radar too old to correlate, a failed
    // fetch, and an issue Radar doesn't correlate keep it.
    expect(changeContextShown(open())).toBe(true)
    expect(changeContextShown(open({ recentChanges: { status: 'unsupported', upgrade: { feature: 'Recent changes on an issue' } } }))).toBe(true)
    expect(changeContextShown(open({ recentChanges: { status: 'failed' } }))).toBe(true)
    expect(changeContextShown(open(loaded(issue)))).toBe(true)
  })

  describe('where a change links', () => {
    const rows = (html: string) => [...html.matchAll(/<li[^>]*data-change-row[^>]*>(.*?)<\/li>/g)].map((m) => m[1])
    const fresh = { ...withChanges, correlated_changes: [imageChange, configChange, helmChange] }

    it('links a change read from a timeline event to that event, and names its object without a link', () => {
      const html = open(loaded(fresh), () => {})
      const config = rows(html).find((r) => r.includes('api-config'))!
      expect(config).toContain('data-change-link="timeline"')
      expect(config).not.toMatch(/<button[^>]*>api-config<\/button>/)
    })

    it('keeps the object link for a change with no timeline event, such as a Helm release record', () => {
      const html = open(loaded(fresh), () => {})
      const helm = rows(html).find((r) => r.includes('HelmRelease') || r.includes('Helm release'))!
      expect(helm).not.toContain('data-change-link="timeline"')
    })

    it('keeps the object links when the host has no way to open the Timeline', () => {
      const html = open(loaded(fresh))
      expect(html).not.toContain('data-change-link="timeline"')
      expect(rows(html).find((r) => r.includes('api-config'))).toMatch(/<button[^>]*>api-config<\/button>/)
    })

    it('keeps the object link when the host says the Timeline can\'t show that event', () => {
      const hook: IssueRecentChange = { kind: 'ValidatingWebhookConfiguration', apiVersion: 'admissionregistration.k8s.io/v1', name: 'pod-gate',
        changeType: 'update', timestamp: minutesAgo(15), fields: [{ path: 'webhooks[0].rules' }], event_id: 'ev-hook' }
      const html = renderToString(<IssueRow issue={issue} open onToggle={() => {}} onResourceClick={() => {}}
        expandedIssue={loaded({ ...withChanges, correlated_changes: [configChange, hook] })}
        timelineLinks={{ onTimelineEventClick: () => {}, canOpenTimelineEvent: (target) => target.namespace !== '' }} />)
      const hookRow = rows(html).find((r) => r.includes('pod-gate'))!
      const configRow = rows(html).find((r) => r.includes('api-config'))!
      expect(hookRow).not.toContain('data-change-link="timeline"')
      expect(hookRow).toMatch(/<button[^>]*>pod-gate<\/button>/)
      expect(configRow).toContain('data-change-link="timeline"')
    })

    it('targets the event, its namespace and its time', () => {
      expect(recentChangeTimelineTarget(issue, configChange)).toEqual({ eventId: 'ev-config', namespace: 'shop', timestamp: configChange.timestamp, cluster_id: undefined })
      expect(recentChangeTimelineTarget(issue, { ...configChange, kind: 'ValidatingWebhookConfiguration', namespace: undefined })?.namespace).toBe('')
      expect(recentChangeTimelineTarget(issue, helmChange)).toBeNull()
    })
  })
})

describe('which rows the host fetches for', () => {
  const cleanups: (() => void)[] = []
  afterEach(() => { cleanups.splice(0).forEach((cleanup) => cleanup()) })
  async function mount(node: ReactNode) {
    const host = document.createElement('div')
    document.body.append(host)
    const root = createRoot(host)
    cleanups.push(() => { act(() => root.unmount()); host.remove() })
    await act(async () => root.render(node))
    return { host, rerender: (next: ReactNode) => act(async () => root.render(next)) }
  }
  const second: Issue = { ...issue, id: 'crash-web', name: 'web', severity: 'warning' }

  it('reports every row when the browser cannot say which are on screen, again when they change, and none on unmount', async () => {
    const seen: string[][] = []
    const report = (issues: Issue[]) => seen.push(issues.map((i) => i.id))
    const { rerender } = await mount(<IssuesView issues={[issue, second]} anyData onVisibleIssuesChange={report} />)
    expect(seen.at(-1)).toEqual(['crash-api', 'crash-web'])
    await rerender(<IssuesView issues={[issue]} anyData onVisibleIssuesChange={report} />)
    expect(seen.at(-1)).toEqual(['crash-api'])
    cleanups.splice(0).forEach((cleanup) => cleanup())
    expect(seen.at(-1)).toEqual([])
  })

  it('reports no rows when the list empties while mounted, so the host stops polling for the old ones', async () => {
    const seen: string[][] = []
    const report = (issues: Issue[]) => seen.push(issues.map((i) => i.id))
    const { rerender } = await mount(<IssuesView issues={[issue]} anyData onVisibleIssuesChange={report} />)
    expect(seen.at(-1)).toEqual(['crash-api'])
    await rerender(<IssuesView issues={[]} anyData onVisibleIssuesChange={report} />)
    expect(seen.at(-1)).toEqual([])
  })

  it('reports only the rows the observer sees', async () => {
    let fire: ((entries: { target: Element; isIntersecting: boolean }[]) => void) | undefined
    vi.stubGlobal('IntersectionObserver', class {
      constructor(cb: (entries: { target: Element; isIntersecting: boolean }[]) => void) { fire = cb }
      observe() {}
      disconnect() {}
    })
    cleanups.push(() => vi.unstubAllGlobals())
    const seen: string[][] = []
    const { host } = await mount(<ResourceIssuesSection issues={[issue, second]} onVisibleIssuesChange={(issues) => seen.push(issues.map((i) => i.id))} />)
    const items = host.querySelectorAll('ol > li')
    await act(async () => fire!([{ target: items[1], isIntersecting: true }]))
    expect(seen.at(-1)).toEqual(['crash-web'])
  })

  it('shows each row the change the host has for it', async () => {
    const changes = new Map([[issueRowKey(issue), rowChange(imageChange)]])
    const { host } = await mount(<IssuesView issues={[issue, second]} anyData rowChanges={changes} />)
    const notes = [...host.querySelectorAll('[data-row-change]')].map((n) => n.closest('li')?.textContent ?? '')
    expect(notes).toHaveLength(1)
    expect(notes[0]).toContain('shop / api')
  })
})
