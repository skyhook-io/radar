import { WorkloadRenderer as BaseWorkloadRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/WorkloadRenderer'
import { useNavigate } from 'react-router-dom'
import { useScaleWorkload, fetchJSON } from '../../../api/client'
import { useRBACSubject } from '../../../api/rbac'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { kindToPlural } from '@skyhook-io/k8s-ui/utils/navigation'
import type { Relationships, ResourceRef, ResourceWithRelationships } from '../../../types'
import type { ScalerDiagnosis } from '@skyhook-io/k8s-ui/components/resources/renderers/WorkloadRenderer'

// Map plural lowercase kind to singular PascalCase for ownerReferences matching
function getOwnerKind(kind: string): string {
  const kindMap: Record<string, string> = {
    'daemonsets': 'DaemonSet',
    'deployments': 'Deployment',
    'statefulsets': 'StatefulSet',
    'replicasets': 'ReplicaSet',
    'jobs': 'Job',
  }
  return kindMap[kind] || kind
}

interface WorkloadRendererProps {
  kind: string
  data: any
  onNavigate?: (ref: ResourceRef) => void
  relationships?: Relationships
  scaleBlockedBy?: ResourceRef[]
}

export function WorkloadRenderer({ kind, data, onNavigate, scaleBlockedBy }: WorkloadRendererProps) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const scaleMutation = useScaleWorkload()

  const metadata = data.metadata || {}
  const viewPodsUrl = `/resources/pods?ownerKind=${encodeURIComponent(getOwnerKind(kind))}&ownerName=${encodeURIComponent(metadata.name || '')}&namespace=${encodeURIComponent(metadata.namespace || '')}`

  // SA reverse-lookup for the workload's pod template. "default" when unset
  // (matches PodRenderer's semantics — the SA every Pod uses by default).
  const saName = data?.spec?.template?.spec?.serviceAccountName || 'default'
  const namespace = metadata.namespace ?? ''
  const { data: rbacData, isLoading: rbacLoading, error: rbacError } = useRBACSubject(
    'ServiceAccount', namespace, saName, !!namespace,
  )
  const hpaRefs = (scaleBlockedBy ?? []).filter(ref => {
    const refKind = ref.kind.toLowerCase()
    return refKind === 'horizontalpodautoscaler' || refKind === 'hpa'
  })
  const hpaQueries = useQueries({
    queries: hpaRefs.map(ref => ({
      queryKey: ['resource', kindToPlural(ref.kind), ref.namespace, ref.name, ref.group],
      queryFn: () => {
        const ns = ref.namespace || '_'
        const params = new URLSearchParams()
        if (ref.group) params.set('group', ref.group)
        const query = params.toString()
        return fetchJSON<ResourceWithRelationships<any>>(`/resources/${kindToPlural(ref.kind)}/${ns}/${ref.name}${query ? `?${query}` : ''}`)
      },
      enabled: Boolean(ref.kind && ref.name),
      staleTime: 10000,
      retry: false,
    })),
  })
  const scalerDiagnostics: ScalerDiagnosis[] = hpaRefs.map((ref, index) => {
    const query = hpaQueries[index]
    return {
      ref,
      diagnosis: query.data?.hpaDiagnosis,
      loading: query.isLoading,
      error: query.isError ? (query.error instanceof Error ? query.error.message : 'Failed to fetch HPA') : undefined,
    }
  })

  return (
    <BaseWorkloadRenderer
      kind={kind}
      data={data}
      onNavigate={onNavigate}
      onViewPods={() => navigate(viewPodsUrl)}
      rbacData={rbacData ?? null}
      rbacLoading={rbacLoading}
      rbacError={rbacError as Error | null}
      scaleBlockedBy={scaleBlockedBy}
      scalerDiagnostics={scalerDiagnostics}
      onScale={async (replicas) => {
        await scaleMutation.mutateAsync({
          kind,
          namespace: metadata.namespace,
          name: metadata.name,
          replicas,
        })
      }}
      isScalePending={scaleMutation.isPending}
      onRequestRefresh={() => {
        queryClient.invalidateQueries({ queryKey: ['resource', kind, metadata.namespace, metadata.name] })
      }}
    />
  )
}
