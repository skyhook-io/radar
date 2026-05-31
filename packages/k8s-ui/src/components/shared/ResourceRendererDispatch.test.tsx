import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceRendererDispatch, type RendererOverrides } from './ResourceRendererDispatch'
import type { ResourceRef } from '../../types'

function renderWithScalers(scalers: ResourceRef[]): string {
  const overrides: RendererOverrides = {
    WorkloadRenderer: ({ scaleBlockedBy }) => (
      <span>{scaleBlockedBy?.map((ref) => ref.kind).join(',') || 'none'}</span>
    ),
  }

  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind: 'deployments', namespace: 'prod', name: 'api' }}
      data={{ metadata: { name: 'api', namespace: 'prod' } }}
      relationships={{ scalers }}
      onCopy={() => {}}
      copied={null}
      rendererOverrides={overrides}
      showCommonSections={false}
    />,
  )
}

describe('ResourceRendererDispatch', () => {
  it('blocks manual replica scaling for HPA and KEDA ScaledObject scalers only', () => {
    const html = renderWithScalers([
      { kind: 'VerticalPodAutoscaler', namespace: 'prod', name: 'api-vpa' },
      { kind: 'HorizontalPodAutoscaler', namespace: 'prod', name: 'api-hpa' },
      { kind: 'ScaledObject', namespace: 'prod', name: 'api-keda' },
    ])

    expect(html).toContain('HorizontalPodAutoscaler,ScaledObject')
    expect(html).not.toContain('VerticalPodAutoscaler')
  })

  it('does not block manual replica scaling for VPA-only relationships', () => {
    const html = renderWithScalers([
      { kind: 'VerticalPodAutoscaler', namespace: 'prod', name: 'api-vpa' },
    ])

    expect(html).toContain('none')
  })
})
