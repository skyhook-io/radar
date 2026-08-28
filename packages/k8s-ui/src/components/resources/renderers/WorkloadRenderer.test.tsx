import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { WorkloadRenderer } from './WorkloadRenderer'

const deployment = {
  metadata: { name: 'api', namespace: 'prod' },
  spec: { replicas: 3 },
  status: { readyReplicas: 3, availableReplicas: 3, updatedReplicas: 3 },
}

describe('WorkloadRenderer', () => {
  it('shows a calm rollout notice even when every desired replica remains ready', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={{
          ...deployment,
          metadata: { ...deployment.metadata, generation: 2 },
          status: { observedGeneration: 2, replicas: 4, readyReplicas: 4, availableReplicas: 4, updatedReplicas: 2 },
        }}
      />,
    )

    expect(html).toContain('Rolling out')
    expect(html).toContain('2/3 updated · 4 available')
    expect(html).not.toContain('Issues Detected')
  })

  it('uses new-revision Pod failure evidence in the rollout notice', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={{
          ...deployment,
          metadata: { ...deployment.metadata, generation: 2 },
          status: {
            observedGeneration: 2,
            replicas: 4,
            readyReplicas: 2,
            availableReplicas: 2,
            unavailableReplicas: 1,
            updatedReplicas: 1,
            conditions: [{ type: 'Progressing', status: 'False', message: 'Progress deadline exceeded' }],
          },
        }}
        workloadPods={[{
          name: 'api-new-revision',
          containers: ['api'],
          ready: false,
          healthLevel: 'unhealthy',
          reason: 'ImagePullBackOff',
          updatedRevision: true,
        }]}
      />,
    )

    expect(html).toContain('New revision cannot start')
    expect(html).toContain('ImagePullBackOff · Pod api-new-revision')
    expect(html).toContain('Issues Detected')
    expect(html).toContain('Progressing: Progress deadline exceeded')
    expect(html).not.toContain('Rolling out')
  })

  it('explains that OnDelete needs a manual Pod restart', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="statefulsets"
        data={{
          metadata: { name: 'db', namespace: 'prod', generation: 3 },
          spec: { replicas: 3, updateStrategy: { type: 'OnDelete' } },
          status: { observedGeneration: 3, replicas: 3, readyReplicas: 3, updatedReplicas: 0 },
        }}
      />,
    )

    expect(html).toContain('Waiting for Pod restart')
    expect(html).toContain('OnDelete strategy · 0/3 updated')
  })

  it('keeps outage problems visible when an OnDelete workload has no update pending', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="statefulsets"
        data={{
          metadata: { name: 'db', namespace: 'prod', generation: 3 },
          spec: { replicas: 3, updateStrategy: { type: 'OnDelete' } },
          status: { observedGeneration: 3, replicas: 3, readyReplicas: 0, updatedReplicas: 3 },
        }}
      />,
    )

    expect(html).toContain('3 of 3 replicas are not ready')
  })

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
    expect(html).not.toContain('title="Manual scaling is disabled')
    expect(html).toContain('Controlled by')
    expect(html).toContain('HorizontalPodAutoscaler prod/api')
    expect(html).toContain('ScaledObject prod/api-queue')
    expect(html).toContain('flex-wrap')
    expect(html).toContain('hpa<!-- -->/')
  })

  it('renders compact HPA diagnosis inline with the state badge first', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={deployment}
        onScale={async () => {}}
        scaleBlockedBy={[{ kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' }]}
        scalerDiagnostics={[
          {
            ref: { kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' },
            diagnosis: {
              state: 'limited_max',
              summary: 'HPA wants more replicas but is capped at maxReplicas=5',
              target: { kind: 'Deployment', name: 'api' },
              bounds: { min: 2, max: 5, current: 5, desired: 5 },
            },
          },
        ]}
      />,
    )

    expect(html.indexOf('Maxed')).toBeLessThan(html.indexOf('Wants more; capped at maxReplicas=5'))
    expect(html).toContain('Wants more; capped at maxReplicas=5')
    expect(html).not.toContain('HPA wants more replicas but is capped at maxReplicas=5')
    expect(html).toContain('px-2 py-1.5')
  })

  it('uses compact missing-metrics copy in workload autoscaler context', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={deployment}
        onScale={async () => {}}
        scaleBlockedBy={[{ kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' }]}
        scalerDiagnostics={[
          {
            ref: { kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' },
            diagnosis: {
              state: 'metrics_unavailable',
              summary: 'Add memory requests to the target pods so HPA can compute replicas',
              target: { kind: 'Deployment', name: 'api' },
              bounds: { min: 2, max: 10, current: 3, desired: 3 },
              metrics: [{ type: 'Resource', name: 'memory', status: 'missing' }],
            },
          },
        ]}
      />,
    )

    expect(html).toContain('Metrics unavailable')
    expect(html).toContain('Add memory requests; HPA cannot compute replicas')
    expect(html).not.toContain('Add memory requests to the target pods')
  })

  it('uses compact pinned copy in workload autoscaler context', () => {
    const html = renderToString(
      <WorkloadRenderer
        kind="deployments"
        data={deployment}
        onScale={async () => {}}
        scaleBlockedBy={[{ kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' }]}
        scalerDiagnostics={[
          {
            ref: { kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api' },
            diagnosis: {
              state: 'pinned',
              summary: 'HPA is configured for a fixed replica count of 3',
              target: { kind: 'Deployment', name: 'api' },
              bounds: { min: 3, max: 3, current: 3, desired: 3 },
            },
          },
        ]}
      />,
    )

    expect(html).toContain('Pinned')
    expect(html).toContain('Fixed at 3 replicas')
    expect(html).not.toContain('fixed replica count')
  })
})
