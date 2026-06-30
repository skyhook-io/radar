import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceIssuesSection } from './ResourceIssuesSection'
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
  it('keeps drawer context facts but omits the role badge', () => {
    const html = renderToString(<ResourceIssuesSection issues={[issue]} />)

    expect(html).toContain('Context')
    expect(html).toContain('Blocked pods')
    expect(html).toContain('high<!-- --> confidence')
    expect(html).not.toContain('Possible cause')
  })
})
