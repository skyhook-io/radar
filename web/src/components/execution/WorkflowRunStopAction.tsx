import { WorkflowStopAction } from '@skyhook-io/k8s-ui'
import { useWorkflowStopAction } from '../../hooks/useWorkflowStopAction'

/** Stop / Terminate for a child Workflow selected in a scheduled parent's run view. */
export function WorkflowRunStopAction({ workflow, namespace, name }: { workflow: any; namespace: string; name: string }) {
  const stop = useWorkflowStopAction('workflows', 'argoproj.io', namespace)
  return <WorkflowStopAction data={workflow} namespace={namespace} name={name} {...stop} />
}
