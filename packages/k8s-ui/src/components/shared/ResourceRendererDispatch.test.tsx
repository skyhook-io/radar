import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceRendererDispatch, getResourceStatus, type RendererOverrides } from './ResourceRendererDispatch'
import { getCellFilterValue } from '../resources/resource-utils'
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

// The spec field is a probe: GenericRenderer renders spec into a Specification
// section, so its value appearing in the output is proof the fall-through
// reached a renderer. A blank drawer is not an empty string — the surrounding
// wrapper still emits ~50 characters of markup — so asserting non-emptiness
// passes even when the drawer shows the user nothing.
const COLLISION_PROBE = 'collision-probe-value'

function renderCollidingKind(kind: string, apiVersion: string): string {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind, namespace: 'default', name: 'thing' }}
      data={{
        apiVersion,
        kind: 'Cluster',
        metadata: { name: 'thing', namespace: 'default' },
        spec: { collisionProbe: COLLISION_PROBE },
        status: {},
      }}
      onCopy={() => {}}
      copied={null}
      showCommonSections={false}
    />,
  )
}

describe('getResourceStatus — workload rollout activity', () => {
  const steadyDegraded = {
    metadata: { generation: 4 },
    spec: { replicas: 3 },
    status: { observedGeneration: 4, replicas: 3, updatedReplicas: 3, readyReplicas: 2, availableReplicas: 2 },
  }

  it('keeps steady-state availability as health instead of rollout activity', () => {
    expect(getResourceStatus('deployments', steadyDegraded)).toMatchObject({ text: '2/3', level: 'degraded' })
    expect(getCellFilterValue(steadyDegraded, 'status', 'deployments')).toBe('Degraded')
  })

  it('uses the rollout label consistently while a revision is moving', () => {
    const rolling = {
      metadata: { generation: 4 },
      spec: { replicas: 3 },
      status: { observedGeneration: 4, replicas: 4, updatedReplicas: 2, readyReplicas: 3, availableReplicas: 3 },
    }
    expect(getResourceStatus('deployments', rolling)?.text).toBe('Rolling out')
    expect(getCellFilterValue(rolling, 'status', 'deployments')).toBe('Rolling out')
  })

  it('keeps scaled-to-zero workloads neutral', () => {
    const scaled = { metadata: { generation: 2 }, spec: { replicas: 0 }, status: { observedGeneration: 2 } }
    expect(getResourceStatus('deployments', scaled)).toMatchObject({ text: 'Scaled to 0', level: 'neutral' })
    expect(getCellFilterValue(scaled, 'status', 'deployments')).toBe('Scaled to 0')
  })

  it('treats a foreign Rollout CRD as unknown', () => {
    const rollout = {
      apiVersion: 'rollouts.kruise.io/v1alpha1',
      kind: 'Rollout',
      metadata: { name: 'foreign' },
      spec: { replicas: 3 },
      status: { readyReplicas: 3, availableReplicas: 3, updatedReplicas: 3 },
    }
    expect(getResourceStatus('rollouts', rollout)).toMatchObject({ text: 'Unknown', level: 'unknown' })
  })

  it('uses merged rollout and serving health for Argo Rollouts', () => {
    const rollout = {
      apiVersion: 'argoproj.io/v1alpha1',
      metadata: { generation: 2 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: '2',
        phase: 'Progressing',
        updatedReplicas: 1,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }

    expect(getResourceStatus('rollouts', rollout)).toMatchObject({ text: 'Degraded · Rolling out', level: 'degraded' })
  })
})

describe('Rollout kind collision', () => {
  it('routes foreign Rollouts to the generic renderer', () => {
    const html = renderKind('rollouts', {
      apiVersion: 'rollouts.kruise.io/v1alpha1',
      kind: 'Rollout',
      metadata: { name: 'foreign', namespace: 'default' },
      spec: { collisionProbe: COLLISION_PROBE },
      status: {},
    }, 'default')

    expect(html).toContain(COLLISION_PROBE)
    expect(html).not.toContain('Rollout Strategy')
  })
})

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
    // Velero's BackupRepository joined KNOWN_KINDS when it got a renderer, so
    // it needs the same fall-through as its siblings.
    ['backuprepositories', 'other.io/v1'],
  ])('renders something for a foreign %s CRD', (kind, apiVersion) => {
    expect(renderCollidingKind(kind, apiVersion)).toContain(COLLISION_PROBE)
  })

  // A Velero BackupRepository must reach its own renderer, not the generic one.
  // It raises BackupRepositoryNotReady, and the reason Velero recorded is only
  // on the dedicated page.
  it('renders the Velero repository page for a velero.io backuprepositories', () => {
    const html = renderCollidingKind('backuprepositories', 'velero.io/v1')
    expect(html).toContain('Volume Namespace')
    expect(html).not.toContain(COLLISION_PROBE)
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

describe('Calico network policy collision handling', () => {
  const calicoPolicies = [
    ['NetworkPolicy', 'networkpolicies', 'CalicoNetworkPolicy'],
    ['GlobalNetworkPolicy', 'globalnetworkpolicies', 'CalicoGlobalNetworkPolicy'],
    ['StagedNetworkPolicy', 'stagednetworkpolicies', 'CalicoStagedNetworkPolicy'],
    ['StagedGlobalNetworkPolicy', 'stagedglobalnetworkpolicies', 'CalicoStagedGlobalNetworkPolicy'],
    ['StagedKubernetesNetworkPolicy', 'stagedkubernetesnetworkpolicies', 'CalicoStagedKubernetesNetworkPolicy'],
  ] as const

  it.each(calicoPolicies)('renders %s for both supported Calico groups', (kind, plural, label) => {
    for (const apiVersion of ['crd.projectcalico.org/v1', 'projectcalico.org/v3']) {
      const spec = kind === 'StagedKubernetesNetworkPolicy'
        ? {
            podSelector: { matchLabels: { app: 'api' } },
            policyTypes: ['Ingress'],
            stagedAction: 'Deny',
            ingress: [{ from: [{ ipBlock: { cidr: '10.0.0.0/8' } }] }],
          }
        : {
            selector: "app == 'api'",
            tier: 'security',
            order: 100,
            types: ['Ingress'],
            stagedAction: 'Deny',
            ingress: [{ action: 'Log', protocol: 'TCP', source: { nets: ['10.0.0.0/8'] } }],
          }
      const html = renderKind(plural, {
        apiVersion,
        kind,
        metadata: { name: 'policy', namespace: 'default' },
        spec,
      }, 'default')

      if (kind === 'StagedKubernetesNetworkPolicy') {
        expect(html).toContain('Target')
        expect(html).toContain('Ingress Rules')
        expect(html).toContain('Pod Selector')
        expect(html).toContain('Staged preview')
        expect(html).toContain('Dashed paths are evaluated but not enforced')
        expect(html).toContain('stroke-dasharray="4 3"')
        expect(html).not.toContain('CalicoStagedKubernetesNetworkPolicy')
        expect(html).not.toContain('Allow')
      } else {
        expect(html).toContain(label)
        expect(html).toContain('security')
        expect(html).toContain('Log')
      }
      expect(html).toContain('10.0.0.0/8')
      if (kind.startsWith('Staged') && kind !== 'StagedKubernetesNetworkPolicy') expect(html).toContain('Deny')
    }
  })

  it('keeps core networking.k8s.io NetworkPolicy on the core renderer', () => {
    const html = renderKind('networkpolicies', {
      apiVersion: 'networking.k8s.io/v1',
      kind: 'NetworkPolicy',
      metadata: { name: 'core', namespace: 'default' },
      spec: { podSelector: {}, policyTypes: ['Ingress'], ingress: [] },
    }, 'default')

    expect(html).toContain('Pod Selector')
    expect(html).toContain('Deny all ingress')
    expect(html).not.toContain('Calico NetworkPolicy')
  })

  it('uses the native staged Kubernetes presentation without an Allow action', () => {
    const html = renderKind('stagedkubernetesnetworkpolicies', {
      apiVersion: 'projectcalico.org/v3',
      kind: 'StagedKubernetesNetworkPolicy',
      metadata: { name: 'staged', namespace: 'default' },
      spec: {
        podSelector: { matchLabels: { app: 'api' } },
        policyTypes: ['Ingress'],
        ingress: [{}],
      },
    }, 'default')

    expect(html).toContain('Target')
    expect(html).toContain('Ingress Rules')
    expect(html).toContain('All sources')
    expect(html).toContain('Staged preview')
    expect(html).toContain('Dashed paths are evaluated but not enforced')
    expect(html).toContain('stroke-dasharray="4 3"')
    expect(html).not.toContain('Allow')
  })

  it.each([
    ['networkpolicies', 'other.example.io/v1', 'NetworkPolicy'],
    ['networkpolicies', 'extension.projectcalico.org/v1', 'NetworkPolicy'],
    ['globalnetworkpolicies', 'other.example.io/v1', 'GlobalNetworkPolicy'],
    ['stagednetworkpolicies', 'other.example.io/v1', 'StagedNetworkPolicy'],
    ['stagedglobalnetworkpolicies', 'other.example.io/v1', 'StagedGlobalNetworkPolicy'],
    ['stagedkubernetesnetworkpolicies', 'other.example.io/v1', 'StagedKubernetesNetworkPolicy'],
  ])('uses GenericRenderer for foreign %s/%s', (plural, apiVersion, kind) => {
    const html = renderKind(plural, {
      apiVersion,
      kind,
      metadata: { name: 'foreign', namespace: 'default' },
      spec: { providerSpecificField: 'preserved' },
    }, 'default')

    expect(html).toContain('Specification')
    expect(html).toContain('Provider Specific Field')
    expect(html).not.toContain('Calico')
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

describe('CAPI Machine kind collisions', () => {
  const foreignApiVersion = 'extension.cluster.x-k8s.io/v1'

  it.each([
    ['machines', 'Machine', 'Role'],
    ['machinesets', 'MachineSet', 'Delete Policy'],
  ])('routes a foreign %s CRD through the generic renderer only', (plural, kind, capiMarker) => {
    const html = renderKind(plural, {
      apiVersion: foreignApiVersion,
      kind,
      metadata: {
        name: 'foreign',
        namespace: 'default',
        labels: { 'topology.cluster.x-k8s.io/owned': '' },
      },
      spec: { collisionProbe: COLLISION_PROBE },
      status: { phase: 'provisioned' },
    }, 'default')

    expect(html).toContain(COLLISION_PROBE)
    expect(html).not.toContain(capiMarker)
    expect(html).not.toContain('Topology-controlled')
    expect(getResourceStatus(plural, {
      apiVersion: foreignApiVersion,
      status: { phase: 'provisioned' },
    })?.text).toBe('provisioned')
  })

  it.each([
    ['machines', 'Machine', 'Role'],
    ['machinesets', 'MachineSet', 'Delete Policy'],
  ])('keeps exact-group CAPI %s resources on the dedicated path', (plural, kind, capiMarker) => {
    const html = renderKind(plural, {
      apiVersion: 'cluster.x-k8s.io/v1beta1',
      kind,
      metadata: {
        name: 'capi',
        namespace: 'default',
        labels: { 'topology.cluster.x-k8s.io/owned': '' },
      },
      spec: { collisionProbe: COLLISION_PROBE },
      status: { phase: 'provisioned' },
    }, 'default')

    expect(html).toContain(capiMarker)
    expect(html).toContain('Topology-controlled')
    expect(html).not.toContain(COLLISION_PROBE)
    expect(getResourceStatus(plural, {
      apiVersion: 'cluster.x-k8s.io/v1beta1',
      status: { phase: 'provisioned' },
    })?.text).toBe('Provisioned')
  })
})

describe('shared plurals — the status path collides too', () => {
  // The renderer and the status badge are two separate switches over the same
  // plural, and fixing one leaves the other reading a foreign CRD's conditions
  // as if they were Knative's.
  // Phase-only on purpose. Knative reports a bare "Unknown" for a resource with
  // no conditions, while the generic path reports the phase — so this fixture
  // tells the two apart. A Ready=False condition would not: both paths surface
  // the same text for it, and pinning which of phase/conditions wins is a
  // separate concern that generic-status.test.ts owns.
  it('does not give a foreign subscriptions CRD a Knative status', () => {
    const s = getResourceStatus('subscriptions', {
      apiVersion: 'operators.coreos.com/v1alpha1',
      status: { phase: 'AtLatestKnown' },
    })
    expect(s?.text).toBe('AtLatestKnown')
  })

  it('still routes a Knative subscription to the Knative status', () => {
    const s = getResourceStatus('subscriptions', {
      apiVersion: 'messaging.knative.dev/v1',
      status: { phase: 'AtLatestKnown' },
    })
    expect(s?.text).toBe('Unknown')
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

// A BackupRepository has custom table columns and raises its own issue, but no
// detail renderer — so it renders generically. Its status still has to resolve
// through the Velero mapping, or the drawer header prints the raw phase next to
// a table cell showing the human label for the same object.
describe('Velero BackupRepository status', () => {
  const repo = (phase: string) => ({
    apiVersion: 'velero.io/v1',
    kind: 'BackupRepository',
    metadata: { name: 'r', namespace: 'velero' },
    spec: { repositoryType: 'kopia' },
    status: { phase },
  })

  it('resolves through the Velero mapping, not the raw phase', () => {
    expect(getResourceStatus('backuprepositories', repo('NotReady'))?.text).toBe('Not ready')
    expect(getResourceStatus('backuprepositories', repo('Ready'))?.text).toBe('Ready')
  })

  // The plural is not Velero's alone; another group's repository must not pick
  // up Velero's mapping.
  it('leaves a foreign backuprepositories kind alone', () => {
    const foreign = { ...repo('NotReady'), apiVersion: 'example.com/v1' }
    expect(getResourceStatus('backuprepositories', foreign)?.text).not.toBe('Not ready')
  })
})

describe('GPU ecosystem kind collisions', () => {
  it('routes Volcano Jobs away from the core JobRenderer to GenericRenderer', () => {
    const html = renderKind('jobs', {
      apiVersion: 'batch.volcano.sh/v1alpha1',
      kind: 'Job',
      metadata: { name: 'train', namespace: 'ml' },
      spec: { queue: 'gpu-queue', minAvailable: 4 },
      status: { state: { phase: 'Running' } },
    }, 'ml')
    expect(html).toContain('Min Available')
    expect(html).toContain('gpu-queue')
    expect(html).not.toContain('Completions')
  })

  it('routes status for queues by group (Volcano vs KAI)', () => {
    const volcano = getResourceStatus('queues', {
      apiVersion: 'scheduling.volcano.sh/v1beta1',
      status: { state: 'Open' },
    })
    const kai = getResourceStatus('queues', {
      apiVersion: 'scheduling.run.ai/v2',
      spec: { priority: 100 },
    })
    expect(volcano?.text).toBe('Open')
    expect(kai?.text).not.toBe('Open')
  })

  it('routes status for podgroups by group (Volcano vs KAI)', () => {
    const volcano = getResourceStatus('podgroups', {
      apiVersion: 'scheduling.volcano.sh/v1beta1',
      status: { phase: 'Running' },
    })
    expect(volcano?.text).toBe('Running')
  })

  it('routes Kueue Workload status only for the kueue group', () => {
    const admitted = getResourceStatus('workloads', {
      apiVersion: 'kueue.x-k8s.io/v1beta2',
      status: { conditions: [{ type: 'Admitted', status: 'True' }] },
    })
    expect(admitted?.text).toBe('Admitted')
    const foreign = getResourceStatus('workloads', {
      apiVersion: 'scheduling.k8s.io/v1alpha2',
      status: { conditions: [{ type: 'Admitted', status: 'True' }] },
    })
    expect(foreign?.text).toBe('Admitted')
    expect(foreign?.color).not.toBe(admitted?.color)
  })

  it('does not assign Volcano or core Job semantics to foreign collisions', () => {
    const foreign = {
      apiVersion: 'example.io/v1',
      kind: 'Job',
      metadata: { name: 'foreign', namespace: 'default' },
      spec: { collisionProbe: COLLISION_PROBE },
      status: { state: { phase: 'Running' } },
    }
    expect(getResourceStatus('jobs', foreign)).toBeNull()
    const html = renderKind('jobs', foreign, 'default')
    expect(html).toContain(COLLISION_PROBE)
    expect(html).not.toContain('Completions')
  })

  it('keeps typed core Jobs on the core path when TypeMeta is absent', () => {
    const core = {
      kind: 'Job',
      metadata: { name: 'core', namespace: 'default' },
      spec: { completions: 1, parallelism: 1 },
      status: { succeeded: 1, conditions: [{ type: 'Complete', status: 'True' }] },
    }
    expect(getResourceStatus('jobs', core)?.text).toBe('Complete')
    const html = renderKind('jobs', core, 'default')
    expect(html).toContain('Completions')
  })

  it('leaves foreign Queues and PodGroups on generic status handling', () => {
    expect(getResourceStatus('queues', { apiVersion: 'example.io/v1', status: {} })).toBeNull()
    expect(getResourceStatus('podgroups', { apiVersion: 'example.io/v1', status: {} })).toBeNull()
  })

  it('accepts the current llm-d InferenceObjective group', () => {
    expect(getResourceStatus('inferenceobjectives', {
      apiVersion: 'llm-d.ai/v1alpha2',
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    })?.text).toBe('Accepted')
  })

  it('routes KAITO Workspace status only for the kaito group', () => {
    const ws = getResourceStatus('workspaces', {
      apiVersion: 'kaito.sh/v1beta1',
      status: { conditions: [{ type: 'ResourceReady', status: 'False' }] },
    })
    expect(ws).not.toBeNull()
  })
})

describe('GPU ecosystem status edge cases', () => {
  it('JobSet with minimal status is Pending, not Running', () => {
    const fresh = getResourceStatus('jobsets', {
      apiVersion: 'jobset.x-k8s.io/v1alpha2',
      status: { replicatedJobsStatus: [{ name: 'w', active: 0, ready: 0 }] },
    })
    const live = getResourceStatus('jobsets', {
      apiVersion: 'jobset.x-k8s.io/v1alpha2',
      status: { replicatedJobsStatus: [{ name: 'w', active: 1, ready: 1 }] },
    })
    expect(fresh?.text).toBe('Pending')
    expect(live?.text).toBe('Running')
  })

  it('InferencePool with only an empty-parentRef default entry reads Not referenced', () => {
    const pool = getResourceStatus('inferencepools', {
      apiVersion: 'inference.networking.x-k8s.io/v1alpha2',
      status: { parent: [{ parentRef: {}, conditions: [{ type: 'Accepted', status: 'Unknown' }] }] },
    })
    expect(pool?.text).toBe('Not referenced')
  })
})
