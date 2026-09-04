import { RolloutRenderer as BaseRolloutRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/RolloutRenderer'
import {
  useRolloutAction,
  useRolloutAnalysisRuns,
  useRolloutCapabilities,
  useWorkloadRevisions,
  useWorkloadPods,
  type RolloutAction,
} from '../../../api/client'

interface RolloutRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function RolloutRenderer({ data, onNavigate }: RolloutRendererProps) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const { data: capabilities } = useRolloutCapabilities(namespace, name)
  const action = useRolloutAction()
  const { data: analysisRunsResponse } = useRolloutAnalysisRuns(namespace, name)
  const { data: revisions } = useWorkloadRevisions('rollouts', namespace, name)
  const { data: podsResponse } = useWorkloadPods('rollouts', namespace, name)

  return (
    <BaseRolloutRenderer
      data={data}
      onNavigate={onNavigate}
      capabilities={capabilities}
      onAction={(next: RolloutAction) => action.mutate({ action: next, namespace, name })}
      pendingAction={action.isPending ? action.variables?.action ?? null : null}
      analysisRunHistory={analysisRunsResponse?.items}
      revisions={revisions}
      pods={podsResponse?.pods}
    />
  )
}
