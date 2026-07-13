import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceIssuesSection, sameResource } from './ResourceIssuesSection'
import { IssueRow } from './IssuesView'
import type { Issue } from './types'

const issue: Issue = {
  id: 'pvc-root',
  severity: 'critical',
  source: 'missing_ref',
  category: 'pvc_pending',
  category_group: 'storage',
  grouping_scope: 'pvc',
  kind: 'PersistentVolumeClaim',
  namespace: 'demo',
  name: 'data',
  reason: 'StorageClassMissing',
  cause: 'PVC demo/data references a StorageClass that does not exist.',
  diagnostic_context: {
    role: 'candidate',
    facts: [
      {
        type: 'pvc_blast_radius',
        confidence: 'high',
        message: 'Blocks pods that mount this claim.',
      },
    ],
  },
}

describe('ResourceIssuesSection', () => {
  it('uses the Issues row treatment and defaults collapsed', () => {
    const html = renderToString(<ResourceIssuesSection issues={[issue]} />)

    expect(html).toContain('Operational issues')
    expect(html).toContain('StorageClassMissing')
    expect(html).not.toContain('What&#x27;s wrong')
    expect(html).not.toContain('Context')
    expect(html).not.toContain('Blocked pods')
  })

  it('shows the Subject deep-link by default', () => {
    const html = renderToString(<IssueRow issue={issue} open onToggle={() => {}} onResourceClick={() => {}} />)
    expect(html).toContain('Subject')
  })

  it('suppresses the Subject deep-link when the issue subject is the embedding resource', () => {
    // apiKind (plural) must still resolve to the same identity as the wire kind.
    const html = renderToString(<IssueRow issue={issue} open onToggle={() => {}} onResourceClick={() => {}} hideSubject />)
    expect(html).not.toContain('Subject')
  })
})

describe('sameResource', () => {
  const subject = { kind: 'PersistentVolumeClaim', namespace: 'demo', name: 'data' }

  it('matches across singular kind vs plural API name', () => {
    expect(sameResource(subject, { kind: 'persistentvolumeclaims', namespace: 'demo', name: 'data' })).toBe(true)
  })

  it('treats a missing namespace as empty', () => {
    expect(sameResource({ kind: 'Node', name: 'n1' }, { kind: 'nodes', namespace: '', name: 'n1' })).toBe(true)
  })

  it('does not match a different name', () => {
    expect(sameResource(subject, { kind: 'persistentvolumeclaims', namespace: 'demo', name: 'other' })).toBe(false)
  })

  it('does not match a different kind', () => {
    expect(sameResource(subject, { kind: 'configmaps', namespace: 'demo', name: 'data' })).toBe(false)
  })
})
