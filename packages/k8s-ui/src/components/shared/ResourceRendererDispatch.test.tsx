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

function renderKind(kind: string, data: any, namespace = ''): string {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind, namespace, name: data?.metadata?.name || 'x' }}
      data={data}
      onCopy={() => {}}
      copied={null}
      showCommonSections={false}
    />,
  )
}

describe('ClusterPolicy kind collision (nvidia.com vs kyverno.io)', () => {
  it('routes nvidia.com ClusterPolicy to the GPU Operator renderer', () => {
    const html = renderKind('clusterpolicies', {
      apiVersion: 'nvidia.com/v1',
      kind: 'ClusterPolicy',
      metadata: { name: 'cluster-policy' },
      spec: { driver: { enabled: true }, devicePlugin: { enabled: true } },
      status: { state: 'ready' },
    })
    expect(html).toContain('Operator Status')
    expect(html).not.toContain('Rules')
  })

  it('routes kyverno.io ClusterPolicy to the Kyverno renderer', () => {
    const html = renderKind('clusterpolicies', {
      apiVersion: 'kyverno.io/v1',
      kind: 'ClusterPolicy',
      metadata: { name: 'require-labels' },
      spec: { rules: [{ name: 'check-labels' }] },
    })
    expect(html).not.toContain('Operator Status')
  })
})

describe('DRA renderers dispatch', () => {
  it('renders ResourceClaim with allocation sections (not GenericRenderer)', () => {
    const html = renderKind('resourceclaims', {
      apiVersion: 'resource.k8s.io/v1',
      kind: 'ResourceClaim',
      metadata: { name: 'gpu-claim', namespace: 'ml' },
      spec: { devices: { requests: [{ name: 'gpu', exactly: { deviceClassName: 'gpu.nvidia.com', count: 1 } }] } },
      status: {
        allocation: { devices: { results: [{ request: 'gpu', driver: 'gpu.nvidia.com', pool: 'node-1', device: 'gpu-0' }] } },
        reservedFor: [{ resource: 'pods', name: 'train-1' }],
      },
    }, 'ml')
    expect(html).toContain('Device Requests')
    expect(html).toContain('gpu.nvidia.com')
    expect(html).toContain('Reserved For')
  })

  it('does not treat an empty allocation block as allocated', () => {
    const html = renderKind('resourceclaims', {
      apiVersion: 'resource.k8s.io/v1',
      kind: 'ResourceClaim',
      metadata: { name: 'partial-claim', namespace: 'ml' },
      spec: { devices: { requests: [{ name: 'gpu', exactly: { deviceClassName: 'gpu.example.com' } }] } },
      status: { allocation: {} },
    }, 'ml')
    expect(html).toContain('Not allocated')
    expect(html).not.toContain('Allocated but unreserved')
  })

  it('reads v1beta1 request shape (deviceClassName at request level)', () => {
    const html = renderKind('resourceclaims', {
      apiVersion: 'resource.k8s.io/v1beta1',
      kind: 'ResourceClaim',
      metadata: { name: 'old-claim', namespace: 'ml' },
      spec: { devices: { requests: [{ name: 'gpu', deviceClassName: 'gpu.example.com' }] } },
    }, 'ml')
    expect(html).toContain('gpu.example.com')
  })

  it('renders ResourceSlice with device inventory', () => {
    const html = renderKind('resourceslices', {
      apiVersion: 'resource.k8s.io/v1',
      kind: 'ResourceSlice',
      metadata: { name: 'node-1-gpus' },
      spec: {
        driver: 'gpu.nvidia.com',
        pool: { name: 'node-1' },
        nodeName: 'node-1',
        devices: [{ name: 'gpu-0', attributes: { productName: { string: 'H100' } } }],
      },
    })
    expect(html).toContain('Slice Info')
    expect(html).toContain('H100')
  })

  it('renders DeviceClass selectors', () => {
    const html = renderKind('deviceclasses', {
      apiVersion: 'resource.k8s.io/v1',
      kind: 'DeviceClass',
      metadata: { name: 'gpu.nvidia.com' },
      spec: { selectors: [{ cel: { expression: "device.driver == 'gpu.nvidia.com'" } }] },
    })
    expect(html).toContain('Selectors (1)')
    expect(html).toContain('device.driver')
  })
})
