import { WorkloadRenderer as BaseWorkloadRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/WorkloadRenderer'
import { useNavigate } from 'react-router-dom'
import { useScaleWorkload } from '../../../api/client'
import { useRBACSubject } from '../../../api/rbac'
import { useQueryClient } from '@tanstack/react-query'
import type { Relationships, ResourceRef } from '../../../types'

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
