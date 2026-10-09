import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import { WorkloadView } from './WorkloadView'

const pod = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: { name: 'web-1', namespace: 'shop' },
  spec: { containers: [{ name: 'app', image: 'nginx' }] },
  status: { phase: 'Running', containerStatuses: [{ name: 'app', ready: true, state: { running: {} } }] },
}

function render(resourceError: unknown) {
  return renderToStaticMarkup(
    <WorkloadView
      kind="pods"
      namespace="shop"
      name="web-1"
      onBack={vi.fn()}
      resource={pod}
      resourceError={resourceError}
      relationships={{ deployment: { kind: 'Deployment', namespace: 'shop', name: 'web' } }}
      onNavigateToResource={vi.fn()}
      actionsBarProps={{ canExec: true, canViewLogs: true, onOpenTerminal: vi.fn(), onOpenLogs: vi.fn(), onDelete: vi.fn(), renderDiagnose: () => 'Diagnose' }}
      hasOperationalIssues
      renderOverviewLead={() => 'Host issues panel'}
      renderOverviewExtra={() => 'Host reachability panel'}
      renderDiagnoseTab={() => 'Host reachability tab'}
      reachableVia={[{ kind: 'Service', namespace: 'shop', name: 'web' }]}
    />,
  )
}

describe('WorkloadView for an object deleted while open', () => {
  it('says it is gone, names its owner, and offers nothing that acts on it', () => {
    const html = render(Object.assign(new Error('pods "web-1" not found'), { status: 404 }))
    expect(html).toContain('This Pod no longer exists in the cluster')
    expect(html).toContain('Go to its Deployment web')
    expect(html).toContain('>Deleted<')
    expect(html).not.toContain('Terminal')
    expect(html).not.toContain('Diagnose')
    expect(html).not.toContain('Host issues panel')
    expect(html).not.toContain('Host reachability panel')
    expect(html).not.toContain('Reachability')
  })

  it('shows the live view when the fetch failed for another reason', () => {
    const html = render(Object.assign(new Error('boom'), { status: 500 }))
    expect(html).not.toContain('no longer exists')
    expect(html).not.toContain('>Deleted<')
    expect(html).toContain('Terminal')
    expect(html).toContain('Diagnose')
    expect(html).toContain('Host issues panel')
    expect(html).toContain('Host reachability panel')
    expect(html).toContain('Reachability')
  })
})
