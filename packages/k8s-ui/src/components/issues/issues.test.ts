import { afterEach, describe, it, expect, vi } from 'vitest'
import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { compareIssues, subjectRef, memberRef, normalizeImagePullMessage, issueMessageParts, type Issue } from './types'
import { categoryLabel, groupBadgeClass, groupLabel } from './severity'
import { IssueRow } from './IssuesView'

const base: Issue = {
  id: 'id-0',
  severity: 'warning',
  source: 'problem',
  category: 'crashloop',
  category_group: 'runtime',
  grouping_scope: 'workload',
  kind: 'Deployment',
  name: 'app',
  reason: 'CrashLoopBackOff',
}
const mk = (o: Partial<Issue>): Issue => ({ ...base, ...o })

afterEach(() => {
  vi.useRealTimers()
})

describe('compareIssues', () => {
  it('orders critical before warning regardless of observed age', () => {
    const warn = mk({ id: 'w', severity: 'warning', first_seen: '2026-05-01T00:00:00Z' }) // newer
    const crit = mk({ id: 'c', severity: 'critical', first_seen: '2026-01-01T00:00:00Z' }) // older
    expect([warn, crit].sort(compareIssues).map((i) => i.id)).toEqual(['c', 'w'])
  })

  it('breaks same-severity ties by first_seen DESC (newest observed issue first)', () => {
    const older = mk({ id: 'o', first_seen: '2026-01-01T00:00:00Z' })
    const newer = mk({ id: 'n', first_seen: '2026-05-01T00:00:00Z' })
    expect([older, newer].sort(compareIssues).map((i) => i.id)).toEqual(['n', 'o'])
  })

  it('orders direct startup blockers before generic problem rows at same severity', () => {
    const generic = mk({ id: 'generic', source: 'problem', first_seen: '2026-05-01T00:00:00Z' })
    const blocker = mk({ id: 'blocker', source: 'scheduling', first_seen: '2026-01-01T00:00:00Z' })
    const missing = mk({ id: 'missing', source: 'missing_ref', first_seen: '2026-03-01T00:00:00Z' })
    expect([generic, blocker, missing].sort(compareIssues).map((i) => i.id)).toEqual(['blocker', 'missing', 'generic'])
  })

  it('does NOT reshuffle same-severity rows when only last_seen changes (anti-churn)', () => {
    // Two same-severity rows, same first_seen — order is the deterministic name tiebreak.
    const a = mk({ id: 'id-a', name: 'a', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-05-01T00:00:00Z' })
    const b = mk({ id: 'id-b', name: 'b', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-05-30T00:00:00Z' })
    const before = [a, b].sort(compareIssues).map((i) => i.id)
    expect(before).toEqual(['id-a', 'id-b'])
    // A refetch bumps a's last_seen to "now". Sorting on last_seen would flip the
    // order; keying on first_seen + identity must NOT — this is the whole point
    // of the first_seen-based sort.
    const aRefetched = mk({ ...a, last_seen: '2026-06-01T00:00:00Z' })
    const after = [aRefetched, b].sort(compareIssues).map((i) => i.id)
    expect(after).toEqual(before)
  })
})

describe('category/group label fallbacks', () => {
  it('returns the mapped label, else humanizes (server-added category needs no frontend deploy)', () => {
    expect(categoryLabel('crashloop')).toBe('Crash loop')
    expect(categoryLabel('some_new_future_category')).toBe('Some new future category')
  })
  it('humanizes an unmapped group', () => {
    expect(groupLabel('runtime')).toBe('Runtime')
    expect(groupLabel('some_future_group')).toBe('Some future group')
  })
  it('keeps ordinary group chips neutral and emphasizes control-plane/unknown groups', () => {
    expect(groupBadgeClass('runtime')).toContain('text-theme-text-secondary')
    expect(groupBadgeClass('runtime')).not.toContain('ring-theme-border-light')
    expect(groupBadgeClass('control_plane')).toContain('text-theme-text-primary')
    expect(groupBadgeClass('control_plane')).toContain('ring-theme-border-light')
    expect(groupBadgeClass('unknown')).toContain('ring-theme-border-light')
    expect(groupBadgeClass('some_future_group')).toContain('text-theme-text-secondary')
    expect(groupBadgeClass('some_future_group')).not.toContain('ring-theme-border-light')
  })
})

describe('IssueRow', () => {
  it('keeps relative age visible and adds since-deploy as a secondary tag', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-30T12:00:00Z'))

    const html = renderToString(createElement(IssueRow, {
      issue: mk({
        id: 'baseline',
        first_seen: '2026-06-28T12:00:00Z',
        issue_timing: 'started_at_resource_creation',
      }),
      open: false,
      onToggle: () => undefined,
    }))

    expect(html).toContain('2d')
    expect(html).toContain('since deploy')
  })

  it('uses the category-group chip class in the queue row', () => {
    const html = renderToString(createElement(IssueRow, {
      issue: mk({ category_group: 'control_plane' }),
      open: false,
      onToggle: () => undefined,
    }))

    expect(html).toContain('Control plane')
    expect(html).toContain(groupBadgeClass('control_plane'))
  })
})

describe('subjectRef / memberRef', () => {
  it('subjectRef defaults empty group/namespace and threads cluster_id', () => {
    const issue = mk({ cluster_id: 'cl_1', kind: 'Deployment', name: 'web' }) // no group/namespace
    expect(subjectRef(issue)).toEqual({ cluster_id: 'cl_1', group: '', kind: 'Deployment', namespace: '', name: 'web' })
  })
  it('memberRef threads the issue cluster_id onto a member', () => {
    const issue = mk({ cluster_id: 'cl_2' })
    const member = { group: 'apps', kind: 'Pod', namespace: 'ns', name: 'p1' }
    expect(memberRef(issue, member)).toEqual({ ...member, cluster_id: 'cl_2' })
  })
})

describe('image-pull message normalization', () => {
  const notFound =
    'Back-off pulling image "reg.io/team/api:v2": ErrImagePull: rpc error: code = NotFound desc = failed to pull and unpack image "reg.io/team/api:v2": failed to resolve reference "reg.io/team/api:v2": "reg.io/team/api:v2": not found'

  it('extracts cause + single image ref from the verbose CRI string', () => {
    expect(normalizeImagePullMessage(notFound)).toBe('Image not found: reg.io/team/api:v2')
  })
  it('classifies the common failure modes', () => {
    expect(normalizeImagePullMessage('pull access denied for image "x:1", repository does not exist or may require authorization')).toBe('Not authorized to pull image: x:1')
    expect(normalizeImagePullMessage('failed to pull image "x:1": dial tcp: lookup reg.io: no such host')).toBe('Registry unreachable: x:1')
    expect(normalizeImagePullMessage('toomanyrequests: rate limit exceeded for image "x:1"')).toBe('Registry rate-limited: x:1')
  })
  it('returns null for shapes it does not recognize (caller keeps raw)', () => {
    expect(normalizeImagePullMessage('some novel kubelet error')).toBeNull()
    expect(normalizeImagePullMessage('')).toBeNull()
  })

  it('issueMessageParts normalizes image-pull headline and keeps raw as detail', () => {
    const parts = issueMessageParts(mk({ category: 'image_pull_failed', reason: 'ImagePullBackOff', message: notFound }))
    expect(parts.headline).toBe('Image not found: reg.io/team/api:v2')
    expect(parts.detail).toBe(notFound)
  })
  it('does NOT mislabel a non-image "not found" message (gating)', () => {
    // missing_config_ref carries 'secret "x" not found' — must stay verbatim, no detail split.
    const parts = issueMessageParts(mk({ category: 'missing_config_ref', reason: 'Missing Secret', message: 'secret "project-infra" not found' }))
    expect(parts.headline).toBe('secret "project-infra" not found')
    expect(parts.detail).toBe('')
  })
})

describe('IssueRow diagnosis raw messages', () => {
  it('shows raw_message when cleaned issue copy has no parsed cause', () => {
    const issue = mk({
      category: 'gitops_operation_failed',
      category_group: 'configuration',
      severity: 'critical',
      reason: 'OperationFailed',
      message: 'app path does not exist',
      raw_message: 'rpc error: code = Unknown desc = app path does not exist',
    })

    const html = renderToString(createElement(IssueRow, { issue, open: true, onToggle: () => undefined, as: 'div' }))

    expect(html).toContain('app path does not exist')
    expect(html).toContain('rpc error: code = Unknown desc = app path does not exist')
  })
})
