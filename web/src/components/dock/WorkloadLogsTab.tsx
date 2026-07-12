import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { ScheduledWorkloadLogsViewer } from '../logs/ScheduledWorkloadLogsViewer'

interface WorkloadLogsTabProps {
  namespace: string
  workloadKind: string
  workloadName: string
}

export function WorkloadLogsTab({
  namespace,
  workloadKind,
  workloadName,
}: WorkloadLogsTabProps) {
  const normalizedKind = workloadKind.toLowerCase()
  const isScheduled = normalizedKind === 'cronjob' || normalizedKind === 'cronjobs' ||
    normalizedKind === 'cronworkflow' || normalizedKind === 'cronworkflows' ||
    normalizedKind === 'workflowtemplate' || normalizedKind === 'workflowtemplates' ||
    normalizedKind === 'clusterworkflowtemplate' || normalizedKind === 'clusterworkflowtemplates' ||
    normalizedKind === 'scaledjob' || normalizedKind === 'scaledjobs'

  return (
    <div className="h-full">
      {isScheduled ? (
        <ScheduledWorkloadLogsViewer
          kind={workloadKind}
          namespace={namespace}
          name={workloadName}
        />
      ) : (
        <WorkloadLogsViewer
          kind={workloadKind}
          namespace={namespace}
          name={workloadName}
        />
      )}
    </div>
  )
}
