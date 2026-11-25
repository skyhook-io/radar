import { canStopWorkflow, type WorkflowStopActionProps } from '@skyhook-io/k8s-ui'
import { isWorkflowStopTargetGone, useRadarFeature, useStopWorkflow } from '../api/client'
import { useNamespacedCapabilities } from '../contexts/CapabilitiesContext'

type WorkflowStopHostProps = Pick<WorkflowStopActionProps, 'onStopWorkflow' | 'isStoppingWorkflow' | 'workflowStopUpgrade'>

/**
 * Wires Stop / Terminate for an Argo Workflow in `namespace`: offered only when
 * the Radar serves the stop endpoint and the caller may patch Workflows there.
 */
export function useWorkflowStopAction(kind: string, group: string | undefined, namespace: string | undefined): WorkflowStopHostProps {
  const { workloadWrites, workloadWritesPending } = useNamespacedCapabilities(namespace)
  const { support, upgrade } = useRadarFeature('workflowStop')
  const mutation = useStopWorkflow()
  const allowed = !workloadWritesPending && canStopWorkflow({ name: kind, group }, workloadWrites, support === 'supported')
  return {
    // mutateAsync, not mutate: the stop dialog stays open when this rejects.
    // A run that is gone or replaced has nothing left to retry, so those
    // resolve and close it; the mutation meta has already reported them.
    onStopWorkflow: allowed
      ? (params) => mutation.mutateAsync(params).catch((error: unknown) => {
          if (isWorkflowStopTargetGone(error)) return
          throw error
        })
      : undefined,
    isStoppingWorkflow: mutation.isPending,
    // Only a Radar known to lack the endpoint gets the upgrade note; a failed
    // capabilities probe is not evidence of an old Radar.
    workflowStopUpgrade: kind === 'workflows' && group === 'argoproj.io' && support === 'unsupported' ? upgrade : null,
  }
}
