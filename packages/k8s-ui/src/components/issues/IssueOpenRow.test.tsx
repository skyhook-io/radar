// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { IssuesView } from './IssuesView'
import { ResourceIssuesSection } from './ResourceIssuesSection'
import type { Issue } from './types'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

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
  first_seen: new Date(Date.now() - 600_000).toISOString(),
  change_context: { changed: true, what: 'image_change', when: '5m', evidence: 'ReplicaSet api-7d9 replaced api-5c4' },
  diagnostic_context: { role: 'candidate', facts: [{ type: 'pvc_blast_radius', message: 'Blocks every Pod that mounts this claim.' }] },
}

const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach((cleanup) => cleanup()) })

async function mount(node: ReactNode) {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  cleanups.push(() => { act(() => root.unmount()); host.remove() })
  await act(async () => root.render(node))
  const expand = async () => {
    await act(async () => { host.querySelector<HTMLElement>('li [role="button"]')!.click() })
  }
  return { host, root, expand }
}

describe.each([
  ['IssuesView', (props: { onOpenIssueChange?: (i: Issue | null) => void; issues?: Issue[] }) => <IssuesView anyData {...props} issues={props.issues ?? [issue]} />],
  ['ResourceIssuesSection', (props: { onOpenIssueChange?: (i: Issue | null) => void; issues?: Issue[] }) => <ResourceIssuesSection {...props} issues={props.issues ?? [issue]} />],
])('%s', (_name, List) => {
  it('tells the host nothing is open once the list unmounts with a row open', async () => {
    const onOpenIssueChange = vi.fn()
    const { root, expand } = await mount(<List onOpenIssueChange={onOpenIssueChange} />)
    await expand()
    expect(onOpenIssueChange).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'crash-api' }))

    // The host replaces the list, as the Issues page does when a severity filter matches nothing.
    await act(async () => root.render(<p>No issues match the selected severity.</p>))
    expect(onOpenIssueChange).toHaveBeenLastCalledWith(null)
  })

  it('tells the host about a new occurrence of the open issue, and not about a plain refresh', async () => {
    const onOpenIssueChange = vi.fn()
    const { root, expand } = await mount(<List onOpenIssueChange={onOpenIssueChange} />)
    await expand()
    const calls = onOpenIssueChange.mock.calls.length

    await act(async () => root.render(<List onOpenIssueChange={onOpenIssueChange} issues={[{ ...issue, last_seen: new Date().toISOString() }]} />))
    expect(onOpenIssueChange.mock.calls.length).toBe(calls)

    const recurred = { ...issue, first_seen: new Date(Date.now() - 60_000).toISOString() }
    await act(async () => root.render(<List onOpenIssueChange={onOpenIssueChange} issues={[recurred]} />))
    expect(onOpenIssueChange).toHaveBeenLastCalledWith(expect.objectContaining({ id: issue.id, first_seen: recurred.first_seen }))
  })

  it('tells the host when the open row is closed', async () => {
    const onOpenIssueChange = vi.fn()
    const { expand } = await mount(<List onOpenIssueChange={onOpenIssueChange} />)
    await expand()
    await expand()
    expect(onOpenIssueChange).toHaveBeenLastCalledWith(null)
  })

  it('expands as before when the host passes no override: existing sections and change context, no Recent changes', async () => {
    const { host, expand } = await mount(<List />)
    await expand()
    const text = host.textContent ?? ''
    expect(text).toContain(issue.cause)
    expect(text).toContain('ReplicaSet api-7d9 replaced api-5c4')
    expect(text).toContain('Blocks every Pod that mounts this claim.')
    expect(text).not.toContain('Recent changes')
    expect(host.querySelector('[data-recent-changes]')).toBeNull()
  })
})
