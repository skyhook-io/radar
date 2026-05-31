import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { WorkloadRenderer } from './WorkloadRenderer'

const deployment = {
  metadata: { name: 'api', namespace: 'prod' },
  spec: { replicas: 3 },
  status: { readyReplicas: 3, availableReplicas: 3, updatedReplicas: 3 },
}

describe('WorkloadRenderer', () => {
  it('enables manual scaling when no replica controller targets the workload', () => {
    const html = renderToString(
      <WorkloadRenderer kind="deployments" data={deployment} onScale={async () => {}} />,
    )

    expect(html).toContain('Scale')
    expect(html).not.toContain('disabled=""')
    expect(html).not.toContain('Manual scaling is disabled')
  })

  it('disables manual scaling when an HPA or KEDA ScaledObject owns replicas', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={deployment}
        onScale={async () => {}}
        scaleBlockedBy={[
          { kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' },
          { kind: 'ScaledObject', namespace: 'prod', name: 'api-queue' },
        ]}
      />,
    )

    expect(html).toContain('disabled=""')
    expect(html).toContain('Manual scaling is disabled')
    expect(html).toContain('Controlled by')
    expect(html).toContain('HorizontalPodAutoscaler prod/api')
    expect(html).toContain('ScaledObject prod/api-queue')
  })
})
