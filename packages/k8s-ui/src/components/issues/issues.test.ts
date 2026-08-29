import { afterEach, describe, it, expect, vi } from 'vitest'
import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { compareIssues, issueSortAnchor, subjectRef, memberRef, normalizeImagePullMessage, issueMessageParts, type Issue } from './types'
import { categoryLabel, groupBadgeClass, groupLabel } from './severity'
import { IssueRow } from './IssuesView'
import { issueFirstSeenTitle, issueOnsetUnknownTitle, issueResourceCreatedTitle, issueTiming, partialIssueOnsetTitle } from './issue-timing'

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

  it('compares sort anchors as instants across timezone formats', () => {
    const newerLocal = mk({ id: 'local', first_seen: '2026-08-09T23:00:00-07:00' })
    const olderUTC = mk({ id: 'utc', onset_unknown: true, resource_created_at: '2026-08-10T01:00:00Z' })
    expect([olderUTC, newerLocal].sort(compareIssues).map((i) => i.id)).toEqual(['local', 'utc'])
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

  it('uses resource creation as the stable fallback only when onset is unknown', () => {
    const unknownOlder = mk({ id: 'unknown-old', name: 'unknown-old', onset_unknown: true, resource_created_at: '2026-01-01T00:00:00Z' })
    const unknownNewer = mk({ id: 'unknown-new', name: 'unknown-new', onset_unknown: true, resource_created_at: '2026-05-01T00:00:00Z' })
    const known = mk({ id: 'known', name: 'known', first_seen: '2026-03-01T00:00:00Z', resource_created_at: '2025-01-01T00:00:00Z' })
    const noMetadata = mk({ id: 'none', name: 'none', onset_unknown: true })

    expect(issueSortAnchor(known)).toBe('2026-03-01T00:00:00Z')
    expect([unknownOlder, noMetadata, known, unknownNewer].sort(compareIssues).map((i) => i.id)).toEqual([
      'unknown-new',
      'known',
      'unknown-old',
      'none',
    ])
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
  it('keeps relative age visible and adds deployment timing as a secondary tag', () => {
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

  it('promotes after-healthy timing in the collapsed row', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-30T12:00:00Z'))

    const html = renderToString(createElement(IssueRow, {
      issue: mk({
        id: 'regression',
        first_seen: '2026-06-30T10:00:00Z',
        issue_timing: 'started_after_resource_was_healthy',
        issue_timing_basis: 'condition',
      }),
      open: false,
      onToggle: () => undefined,
    }))

    expect(html).toContain('2h')
    expect(html).toContain('after healthy')
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

  it('does not fill collapsed rows with low-information unknown-onset labels', () => {
    const html = renderToString(createElement(IssueRow, {
      issue: mk({ onset_unknown: true }),
      open: false,
      onToggle: () => undefined,
    }))

    expect(html).not.toContain('onset unknown')
    expect(html).not.toContain('0s')
  })

  it('shows independent timing alongside an unknown onset', () => {
    const html = renderToString(createElement(IssueRow, {
      issue: mk({
        onset_unknown: true,
        issue_timing: 'started_at_resource_creation',
        issue_timing_basis: 'owner_condition',
      }),
      open: true,
      onToggle: () => undefined,
    }))

    expect(html).toContain('workload never healthy')
    expect(html).toContain('exact onset unknown; owner workload never became healthy after deployment')
    expect(html).not.toContain('since deploy')
  })

  it('renders mixed groups as a lower-bound age and suppresses a group-wide timing claim', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-30T12:00:00Z'))

    const html = renderToString(createElement(IssueRow, {
      issue: mk({
        first_seen: '2026-06-30T10:00:00Z',
        onset_coverage: { known: 2, unknown: 1 },
        issue_timing: 'started_after_resource_was_healthy',
      }),
      open: true,
      onToggle: () => undefined,
    }))

    expect(html).toContain('≥')
    expect(html).toContain('timing unknown for 1 contributing signal')
    expect(html).not.toContain('after healthy')
  })

  it('presents ordinary first-seen time as a conservative active lower bound', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-30T12:00:00Z'))

    const html = renderToString(createElement(IssueRow, {
      issue: mk({ first_seen: '2026-06-30T10:00:00Z' }),
      open: true,
      onToggle: () => undefined,
    }))

    expect(html).toContain('active at least 2h ago')
    expect(html).not.toContain('started 2h ago')
  })
})

describe('onset provenance copy', () => {
  it('describes a partial group as a lower bound', () => {
    const title = partialIssueOnsetTitle(mk({
      first_seen: '2026-06-30T10:00:00Z',
      onset_coverage: { known: 2, unknown: 1 },
    }))

    expect(title).toContain('Active at least since')
    expect(title).toContain('timing unknown for 1 contributing signal')
  })

  it('describes a known first-seen value as an active lower bound', () => {
    const title = issueFirstSeenTitle(mk({ first_seen: '2026-06-30T10:00:00Z' }))

    expect(title).toContain('Active at least since')
    expect(title).not.toContain('started')
  })

  it('uses grouped language and oldest-resource context when every onset is unknown', () => {
    const title = issueOnsetUnknownTitle(mk({
      onset_unknown: true,
      onset_coverage: { known: 0, unknown: 3 },
      resource_created_at: '2026-06-30T10:00:00Z',
      count: 3,
    }))

    expect(title).toContain('3 contributing signals')
    expect(title).toContain('does not reveal when any')
    expect(title).toContain('Oldest affected resource created')
  })

  it('does not describe one resource with multiple status signals as multiple resources', () => {
    const title = issueOnsetUnknownTitle(mk({
      onset_unknown: true,
      onset_coverage: { known: 0, unknown: 3 },
      resource_created_at: '2026-06-30T10:00:00Z',
    }))

    expect(title).toContain('3 contributing signals')
    expect(title).toContain('Resource created')
    expect(title).not.toContain('Oldest affected resource')
  })

  it('keeps singular resource context for an ungrouped issue', () => {
    const title = issueOnsetUnknownTitle(mk({
      onset_unknown: true,
      resource_created_at: '2026-06-30T10:00:00Z',
    }))

    expect(title).toContain('does not reveal when it began')
    expect(title).toContain('Resource created')
    expect(title).not.toContain('Oldest affected')
  })

  it('labels a one-member workload rollup as affected-resource context', () => {
    const title = issueResourceCreatedTitle(mk({
      kind: 'Deployment',
      count: 1,
      members: [{ kind: 'Pod', namespace: 'prod', name: 'web-abc' }],
      resource_created_at: '2026-06-30T10:00:00Z',
    }))

    expect(title).toContain('Affected resource created')
  })
})

describe('issueTiming', () => {
  it('keeps deployment wording for deployment-like creation failures', () => {
    expect(issueTiming(mk({
      kind: 'Deployment',
      group: 'apps',
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'condition',
    }))).toMatchObject({
      kind: 'creation',
      chip: 'since deploy',
      meta: 'present since deployment or first reconciliation',
    })

    expect(issueTiming(mk({
      kind: 'Rollout',
      group: 'argoproj.io',
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'condition',
    }))).toMatchObject({
      kind: 'creation',
      chip: 'since deploy',
      meta: 'present since deployment or first reconciliation',
    })
  })

  it('uses deployment wording for pod issues only when workload timing evidence is present', () => {
    expect(issueTiming(mk({
      kind: 'Pod',
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'pod_creation',
    }))).toMatchObject({
      kind: 'creation',
      chip: 'since deploy',
    })

    expect(issueTiming(mk({
      kind: 'Pod',
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'phase',
    }))).toMatchObject({
      kind: 'creation',
      chip: 'since creation',
    })
  })

  it('uses self-explanatory copy when exact onset and independent timing have different scopes', () => {
    expect(issueTiming(mk({
      kind: 'Pod',
      onset_unknown: true,
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'pod_creation',
    }))).toMatchObject({
      chip: 'pod failed at startup',
      meta: 'exact onset unknown; affected pod failed during workload startup',
    })

    expect(issueTiming(mk({
      kind: 'Rollout',
      group: 'argoproj.io',
      onset_unknown: true,
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'spec',
    }))).toMatchObject({
      chip: 'invalid at first reconcile',
      meta: 'exact onset unknown; initial spec was failing from first reconciliation',
    })
  })

  it('uses resource creation wording for non-deployment creation failures', () => {
    expect(issueTiming(mk({
      kind: 'PersistentVolumeClaim',
      issue_timing: 'started_at_resource_creation',
      issue_timing_basis: 'phase',
    }))).toMatchObject({
      kind: 'creation',
      chip: 'since creation',
      meta: 'present since creation or first reconciliation',
    })
  })

  it('describes direct after-healthy timing without coupling it to first_seen', () => {
    const display = issueTiming(mk({
      first_seen: '2026-06-30T10:00:00Z',
      issue_timing: 'started_after_resource_was_healthy',
      issue_timing_basis: 'condition',
    }))

    expect(display).toMatchObject({
      kind: 'regression',
      chip: 'after healthy',
      meta: 'failing evidence followed a healthy period',
    })
    expect(`${display?.chip} ${display?.meta} ${display?.tooltip}`).not.toContain('2h')
    expect(`${display?.chip} ${display?.meta} ${display?.tooltip}`).not.toMatch(/baseline|safe|ignore/i)
  })

  it('presents owner-condition timing as workload-level rather than cause-specific', () => {
    const display = issueTiming(mk({
      first_seen: '2026-06-30T10:00:00Z',
      issue_timing: 'started_after_resource_was_healthy',
      issue_timing_basis: 'owner_condition',
    }))

    expect(display).toMatchObject({
      kind: 'regression',
      chip: 'health regressed',
      meta: 'workload health regressed',
    })
    expect(display?.tooltip).toContain('does not date or attribute this specific issue')
    expect(`${display?.chip} ${display?.meta} ${display?.tooltip}`).not.toContain('2h')
  })

  it('returns null when there is no confident timing signal', () => {
    expect(issueTiming(mk({ issue_timing: undefined, issue_timing_basis: undefined }))).toBeNull()
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
