import { WorkloadRenderer as BaseWorkloadRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/WorkloadRenderer'
import { useNavigate } from 'react-router-dom'
import { useScaleWorkload } from '../../../api/client'
import { useQueryClient } from '@tanstack/react-query'

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
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function WorkloadRenderer({ kind, data, onNavigate }: WorkloadRendererProps) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const scaleMutation = useScaleWorkload()

  const metadata = data.metadata || {}
  const viewPodsUrl = `/resources/pods?ownerKind=${encodeURIComponent(getOwnerKind(kind))}&ownerName=${encodeURIComponent(metadata.name || '')}&namespace=${encodeURIComponent(metadata.namespace || '')}`

  return (
    <BaseWorkloadRenderer
      kind={kind}
      data={data}
      onNavigate={onNavigate}
      onViewPods={() => navigate(viewPodsUrl)}
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
