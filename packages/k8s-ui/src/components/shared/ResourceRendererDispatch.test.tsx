import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceRendererDispatch, getResourceStatus, type RendererOverrides } from './ResourceRendererDispatch'
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

function renderKind(kind: string, data: any, namespace = '', onNavigate?: (ref: ResourceRef) => void): string {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind, namespace, name: data?.metadata?.name || 'x' }}
      data={data}
      onCopy={() => {}}
      copied={null}
      onNavigate={onNavigate}
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

// ============================================================================
// COLLIDING CRD PLURALS — `clusters` (CNPG / CAPI / third parties) and
// `backups` (CNPG / Velero). Both used to be resolved with a negative guard,
// so any third operator's CRD inherited whichever branch was the fallback.
// ============================================================================

function renderCollidingKind(kind: string, apiVersion: string): string {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind, namespace: 'default', name: 'thing' }}
      data={{ apiVersion, kind: 'Cluster', metadata: { name: 'thing', namespace: 'default' }, spec: {}, status: {} }}
      onCopy={() => {}}
      copied={null}
      showCommonSections={false}
    />,
  )
}

describe('getResourceStatus — colliding plurals', () => {
  it('fabricates no PostgreSQL status for a third-party clusters CRD', () => {
    // KubeBlocks, Redis/Valkey operators and friends all ship `clusters`. With
    // no status of their own they get nothing; with a phase they get the
    // generic phase badge, never a CNPG-derived one.
    expect(getResourceStatus('clusters', { apiVersion: 'apps.kubeblocks.io/v1alpha1', status: {} })).toBeNull()
    expect(getResourceStatus('clusters', { apiVersion: 'apps.kubeblocks.io/v1alpha1', status: { phase: 'Running' } }))
      .toMatchObject({ text: 'Running' })
    // CNPG would have rendered "Not Ready" from the absent instance counts.
    expect(getResourceStatus('clusters', { apiVersion: 'redis.redis.opstreelabs.in/v1beta2', spec: { instances: 3 }, status: {} })).toBeNull()
  })

  it('still resolves both known clusters engines positively', () => {
    expect(getResourceStatus('clusters', {
      apiVersion: 'postgresql.cnpg.io/v1',
      spec: { instances: 2 },
      status: { phase: 'Cluster in healthy state', readyInstances: 2 },
    })).toMatchObject({ text: 'Healthy' })

    expect(getResourceStatus('clusters', {
      apiVersion: 'cluster.x-k8s.io/v1beta1',
      status: { phase: 'Provisioned' },
    })).not.toBeNull()
  })

  it('fabricates no engine status for a third-party backups CRD', () => {
    expect(getResourceStatus('backups', { apiVersion: 'kubevirt.io/v1', status: {} })).toBeNull()
    expect(getResourceStatus('backups', { apiVersion: 'kubevirt.io/v1', status: { phase: 'Running' } }))
      .toMatchObject({ text: 'Running' })
  })

  it('still resolves both known backups engines positively', () => {
    expect(getResourceStatus('backups', {
      apiVersion: 'postgresql.cnpg.io/v1',
      status: { phase: 'completed' },
    })).toMatchObject({ text: 'Completed' })

    expect(getResourceStatus('backups', {
      apiVersion: 'velero.io/v1',
      status: { phase: 'Completed' },
    })).not.toBeNull()
  })

  it('gives no CNPG badge to third-party scheduledbackups and poolers', () => {
    // CNPG's Pooler getter would have said "Not Scheduled" from spec.instances.
    expect(getResourceStatus('poolers', { apiVersion: 'other.io/v1', spec: { instances: 2 }, status: {} })).toBeNull()
    expect(getResourceStatus('scheduledbackups', { apiVersion: 'other.io/v1', spec: {}, status: {} })).toBeNull()
  })
})

describe('ResourceRendererDispatch — colliding plurals fall through', () => {
  // These plurals are in KNOWN_KINDS, which suppresses the generic renderer.
  // Making every render line apiVersion-gated without an explicit fall-through
  // renders a blank drawer for a foreign CRD — the trap the Crossplane
  // collision block documents.
  it.each([
    ['clusters', 'apps.kubeblocks.io/v1alpha1'],
    ['backups', 'kubevirt.io/v1'],
    ['scheduledbackups', 'other.io/v1'],
    ['poolers', 'other.io/v1'],
    // The CNPG declarative kinds and the barman-cloud store extend the same
    // list. `databases` and `publications` ship with several database
    // operators, `objectstores` is a generic plural, and `subscriptions` is
    // Knative's as well — every one of them was blank until this fall-through
    // covered it.
    ['objectstores', 'other.io/v1'],
    ['databases', 'mysql.oracle.com/v2'],
    ['publications', 'other.io/v1'],
    ['subscriptions', 'operators.coreos.com/v1alpha1'],
    ['imagecatalogs', 'other.io/v1'],
    ['clusterimagecatalogs', 'other.io/v1'],
    ['policies', 'operators.coreos.com/v1'],
  ])('renders something for a foreign %s CRD', (kind, apiVersion) => {
    expect(renderCollidingKind(kind, apiVersion).trim()).not.toBe('')
  })

  // Both owners of `subscriptions` still get their own renderer.
  it.each([
    ['messaging.knative.dev/v1', 'Subscriber'],
    ['postgresql.cnpg.io/v1', 'Applied'],
  ])('still renders the owning engine for subscriptions/%s', (apiVersion, marker) => {
    expect(renderCollidingKind('subscriptions', apiVersion)).toContain(marker)
  })

  it('does not double-render when a known engine matches', () => {
    const html = renderCollidingKind('clusters', 'postgresql.cnpg.io/v1')
    expect(html).toContain('Cluster Overview')
    // The generic renderer's raw-spec dump must not appear alongside it.
    expect(html.match(/Cluster Overview/g)).toHaveLength(1)
  })
})

describe('Calico IPPool collision handling', () => {
  it.each(['crd.projectcalico.org/v1', 'projectcalico.org/v3'])('renders IPPool details for %s', (apiVersion) => {
    const html = renderKind('ippools', {
      apiVersion,
      kind: 'IPPool',
      metadata: { name: 'workloads' },
      spec: {
        allowedUses: ['Workload', 'Tunnel'],
        assignmentMode: 'Automatic',
        blockSize: 26,
        cidr: '172.16.0.0/16',
        ipipMode: 'CrossSubnet',
        vxlanMode: 'Never',
        natOutgoing: true,
        disabled: false,
        disableBGPExport: true,
        nodeSelector: 'all()',
        namespaceSelector: "environment == 'production'",
      },
    })

    expect(html).toContain('IP Pool')
    expect(html).toContain('Allowed Uses')
    expect(html).toContain('Workload, Tunnel')
    expect(html).toContain('Assignment Mode')
    expect(html).toContain('Automatic')
    expect(html).toContain('Block Size')
    expect(html).toContain('26')
    expect(html).toContain('CIDR')
    expect(html).toContain('172.16.0.0/16')
    expect(html).toContain('IP-in-IP Mode')
    expect(html).toContain('CrossSubnet')
    expect(html).toContain('VXLAN Mode')
    expect(html).toContain('Never')
    expect(html).toContain('NAT Outgoing')
    expect(html).toContain('Yes')
    expect(html).toContain('Disabled')
    expect(html).toContain('No')
    expect(html).toContain('BGP Export Disabled')
    expect(html).toContain('Node Selector')
    expect(html).toContain('all()')
    expect(html).toContain('Namespace Selector')
    expect(html).toContain('environment == &#x27;production&#x27;')
  })

  it('renders documented defaults when fields are omitted', () => {
    const html = renderKind('ippools', {
      apiVersion: 'projectcalico.org/v3',
      kind: 'IPPool',
      metadata: { name: 'default-ipv6' },
      spec: { cidr: 'fd00::/48' },
    })

    expect(html).toContain('Workload, Tunnel')
    expect(html).toContain('Automatic')
    expect(html).toContain('122')
    expect(html.match(/Never/g)).toHaveLength(2)
    expect(html.match(/>No</g)).toHaveLength(3)
    expect(html).toContain('all()')
  })

  it.each(['networking.example.io/v1', 'extension.projectcalico.org/v1'])('uses the generic renderer for %s', (apiVersion) => {
    const html = renderKind('ippools', {
      apiVersion,
      kind: 'IPPool',
      metadata: { name: 'foreign-pool' },
      spec: { providerSpecificField: 'preserved' },
    })

    expect(html).toContain('Specification')
    expect(html).toContain('Provider Specific Field')
    expect(html).not.toContain('IP Pool')
  })
})

describe('Calico HostEndpoint collision handling', () => {
  it.each(['crd.projectcalico.org/v1', 'projectcalico.org/v3'])('renders HostEndpoint details for %s', (apiVersion) => {
    const html = renderKind('hostendpoints', {
      apiVersion,
      kind: 'HostEndpoint',
      metadata: { name: 'infra-1' },
      spec: {
        expectedIPs: ['172.20.16.133', '172.16.199.199'],
        interfaceName: '*',
        node: 'gdn-test-k8s-infra-1',
        profiles: ['projectcalico-default-allow'],
        ports: [{ name: 'ssh', protocol: 'TCP', port: 22 }],
      },
    }, '', () => {})

    expect(html).toContain('Host Endpoint')
    expect(html).toContain('Expected IPs')
    expect(html).toContain('172.20.16.133')
    expect(html).toContain('172.16.199.199')
    expect(html).toContain('Interface Name')
    expect(html).toContain('Node')
    expect(html).toContain('<button')
    expect(html).toContain('gdn-test-k8s-infra-1')
    expect(html).toContain('Profiles')
    expect(html).toContain('projectcalico-default-allow')
    expect(html).toContain('Named Ports')
    expect(html).toContain('ssh: TCP/22')
  })

  it.each(['networking.example.io/v1', 'extension.projectcalico.org/v1'])('uses the generic renderer for %s', (apiVersion) => {
    const html = renderKind('hostendpoints', {
      apiVersion,
      kind: 'HostEndpoint',
      metadata: { name: 'foreign-endpoint' },
      spec: { providerSpecificField: 'preserved' },
    })

    expect(html).toContain('Specification')
    expect(html).toContain('Provider Specific Field')
    expect(html).not.toContain('Host Endpoint')
  })
})

describe('Calico Tier collision handling', () => {
  it.each(['crd.projectcalico.org/v1', 'projectcalico.org/v3'])('renders Tier details for %s', (apiVersion) => {
    const html = renderKind('tiers', {
      apiVersion,
      kind: 'Tier',
      metadata: { name: 'default' },
      spec: { defaultAction: 'Deny', order: 1000000 },
    })

    expect(html).toContain('Tier')
    expect(html).toContain('Default Action')
    expect(html).toContain('Deny')
    expect(html).toContain('Order')
    expect(html).toContain('1000000')
  })

  it('renders documented defaults when fields are omitted', () => {
    const html = renderKind('tiers', {
      apiVersion: 'projectcalico.org/v3',
      kind: 'Tier',
      metadata: { name: 'default' },
      spec: {},
    })

    expect(html).toContain('Deny')
    expect(html).toContain('Last (lowest precedence)')
  })

  it.each(['networking.example.io/v1', 'extension.projectcalico.org/v1'])('uses the generic renderer for %s', (apiVersion) => {
    const html = renderKind('tiers', {
      apiVersion,
      kind: 'Tier',
      metadata: { name: 'foreign-tier' },
      spec: { providerSpecificField: 'preserved' },
    })

    expect(html).toContain('Specification')
    expect(html).toContain('Provider Specific Field')
    expect(html).not.toContain('Default Action')
  })
})

describe('colliding plurals — near-match API groups', () => {
  // A substring guard would hand these to CNPG/Velero. They are different groups.
  it.each([
    ['clusters', 'extension.postgresql.cnpg.io/v1'],
    ['clusters', 'barmancloud.cnpg.io/v1'],
    ['backups', 'extension.postgresql.cnpg.io/v1'],
    ['backups', 'backup.velero.io/v1'],
    ['poolers', 'sub.postgresql.cnpg.io/v1'],
    ['scheduledbackups', 'sub.postgresql.cnpg.io/v1'],
  ])('does not give %s/%s an engine-specific status', (kind, apiVersion) => {
    const s = getResourceStatus(kind, { apiVersion, spec: { instances: 2 }, status: { phase: 'completed' } })
    // The generic fallback echoes the phase verbatim; the CNPG/Velero getters
    // would have mapped it to "Completed" or read the instance counts.
    expect(s?.text).not.toBe('Completed')
    expect(s?.text).not.toBe('Not Scheduled')
    expect(s?.text).not.toBe('Not Ready')
  })

  it.each([
    ['clusters', 'extension.postgresql.cnpg.io/v1'],
    ['backups', 'backup.velero.io/v1'],
  ])('falls %s/%s through to the generic renderer', (kind, apiVersion) => {
    const html = renderCollidingKind(kind, apiVersion)
    expect(html.trim()).not.toBe('')
    expect(html).not.toContain('Cluster Overview')
  })
})

describe('shared plurals — the status path collides too', () => {
  // The renderer and the status badge are two separate switches over the same
  // plural, and fixing one leaves the other reading a foreign CRD's conditions
  // as if they were Knative's.
  it('does not give a foreign subscriptions CRD a Knative status', () => {
    const s = getResourceStatus('subscriptions', {
      apiVersion: 'operators.coreos.com/v1alpha1',
      status: { conditions: [{ type: 'Ready', status: 'False' }], phase: 'AtLatestKnown' },
    })
    expect(s?.text).toBe('AtLatestKnown')
  })

  it('still gives Knative its own status', () => {
    const s = getResourceStatus('subscriptions', {
      apiVersion: 'messaging.knative.dev/v1',
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    })
    expect(s?.text).toBe('Ready')
  })

  // The legacy namespaced Kyverno kind is served as `policies`, which several
  // operators also use.
  it('gives a Kyverno Policy its policy status and a foreign one the generic', () => {
    const kyverno = getResourceStatus('policies', {
      apiVersion: 'kyverno.io/v1',
      spec: { validationFailureAction: 'Audit' },
      status: { conditions: [{ type: 'Ready', status: 'True', reason: 'Succeeded' }] },
    })
    expect(kyverno?.text).toBeTruthy()
    const foreign = getResourceStatus('policies', {
      apiVersion: 'operators.coreos.com/v1',
      status: { phase: 'Installed' },
    })
    expect(foreign?.text).toBe('Installed')
  })
})
