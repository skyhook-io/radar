import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { Issue } from '@skyhook-io/k8s-ui'
import { ProblemsPanel } from './HomeView'
import { uncheckedSourceLines } from '../issues/uncheckedSources'
import type { IssuesResponse } from '../../api/client'

const line = "Helm releases couldn't be checked, so failed or stuck releases may be missing."
const generic = "Some issue checks couldn't run, so issues may be missing."
const helm = [{ source: 'helm', reason: 'read_failed' }]
const issue: Issue = {
  id: 'pod-failed', severity: 'critical' as const, source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Pod', group: '', namespace: 'db', name: 'pg-1', reason: 'CrashLoopBackOff', message: 'The selected Pod keeps crashing',
}

function render(props: { issues?: Issue[]; unchecked?: IssuesResponse['unchecked_sources']; visibility?: { impact?: string } }) {
  const issues = props.issues ?? []
  return renderToString(
    <ProblemsPanel
      issues={issues}
      issueCount={issues.length}
      uncheckedLines={uncheckedSourceLines(props.unchecked)}
      onRetryIssues={() => {}}
      visibility={props.visibility}
      hasData
      isLoading={false}
      isRefreshing={false}
      error={null}
      totalReturned={issues.length}
      onNavigateToIssues={() => {}}
      onResourceClick={() => {}}
    />,
  ).replaceAll('<!-- -->', '').replace(/&#x27;|&#39;/g, "'")
}

describe('Home Active Issues when Radar could not read Helm releases', () => {
  it('gives no all-clear when nothing else was found', () => {
    const html = render({ unchecked: helm })
    expect(html).toContain('No issues found')
    expect(html).toContain(line)
    expect(html).not.toContain('No active issues')
    expect(html).not.toContain('status-healthy')
    expect(html).not.toContain('text-green-500')
  })

  it('keeps the line when limited visibility owns the empty state', () => {
    const html = render({ unchecked: helm, visibility: { impact: 'Pods are not readable.' } })
    expect(html).toContain('No visible active issues')
    expect(html).toContain(line)
  })

  it('says so above the rows when there are other issues', () => {
    const html = render({ issues: [issue], unchecked: helm })
    expect(html).toContain(line)
  })

  it('keeps the all-clear when the field is absent', () => {
    const html = render({})
    expect(html).toContain('No active issues')
    expect(html).toContain('status-healthy')
    expect(html).not.toContain('couldn')
  })

  it('gives no all-clear for a source this version does not know, and says so generically', () => {
    const html = render({ unchecked: [{ source: 'some-new-source', reason: 'read_failed' }] })
    expect(html).toContain('No issues found')
    expect(html).toContain(generic)
    expect(html).not.toContain('some-new-source')
    expect(html).not.toContain('No active issues')
    expect(html).not.toContain('status-healthy')
    expect(html).not.toContain('text-green-500')
  })

  it('shows one line per source above the rows, without repeats', () => {
    const html = render({
      issues: [issue],
      unchecked: [...helm, { source: 'a', reason: 'x' }, { source: 'b', reason: 'y' }, ...helm],
    })
    expect(html.split(line).length - 1).toBe(1)
    expect(html.split(generic).length - 1).toBe(1)
  })
})
