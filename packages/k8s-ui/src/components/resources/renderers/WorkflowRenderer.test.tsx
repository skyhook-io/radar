import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { WorkflowRenderer } from './WorkflowRenderer'

describe('WorkflowRenderer', () => {
  it('does not turn retained failed attempts into a failed workflow', () => {
    const html = renderToString(<WorkflowRenderer data={{
      metadata: { name: 'retry-workflow' },
      status: {
        phase: 'Succeeded',
        nodes: {
          root: { name: 'retry-workflow', displayName: 'retry-workflow', templateName: 'main', type: 'Retry', phase: 'Succeeded', children: ['attempt'] },
          attempt: { displayName: 'retry-workflow(0)', type: 'Pod', phase: 'Failed', message: 'first attempt failed' },
        },
      },
    }} />)

    expect(html).toContain('Workflow Completed Successfully')
    expect(html).not.toContain('Workflow Issues')
    expect(html).toContain('Execution (2 nodes)')
  })

  it('lists every failed node and bounds long problem summaries', () => {
    const longMessage = 'x'.repeat(500)
    const html = renderToString(<WorkflowRenderer data={{
      metadata: { name: 'failed-workflow' },
      status: {
        phase: 'Failed',
        message: 'workflow failed',
        nodes: {
          root: { name: 'failed-workflow', displayName: 'failed-workflow', templateName: 'main', type: 'DAG', phase: 'Failed', children: ['with-message', 'without-message'] },
          'with-message': { displayName: 'prepare', type: 'Pod', phase: 'Failed', message: longMessage },
          'without-message': { displayName: 'publish', type: 'Pod', phase: 'Failed' },
        },
      },
    }} />)

    expect(html).toContain('Workflow Issues')
    expect(html).toContain('publish failed')
    expect(html).toContain(`prepare: ${'x'.repeat(290)}…`)
  })
})
