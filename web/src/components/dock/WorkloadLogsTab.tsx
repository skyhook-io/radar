import { AlertBanner, PaneLoader, RadarUpgradeNote } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { ScheduledWorkloadLogsViewer } from '../logs/ScheduledWorkloadLogsViewer'

interface WorkloadLogsTabProps {
  namespace: string
  workloadKind: string
  workloadName: string
  viewNamespaces?: string[]
}

function ReplicaSetDockLogs({ namespace, name }: { namespace: string; name: string }) {
  const { support, upgrade } = useRadarFeature('replicaSetLogs')
  const { canViewLogs, logsUnknown } = useNamespacedCapabilities(namespace)
  if (upgrade) return <RadarUpgradeNote requirement={upgrade} />
  if (support !== 'supported') return <PaneLoader label="Checking ReplicaSet log support…" className="h-full" />
  if (!canViewLogs && !logsUnknown) return <AlertBanner variant="warning" title="Pod logs are restricted in this namespace." />
  return <WorkloadLogsViewer kind="replicasets" namespace={namespace} name={name} />
}

export function WorkloadLogsTab({
  namespace,
  workloadKind,
  workloadName,
  viewNamespaces,
}: WorkloadLogsTabProps) {
  const normalizedKind = workloadKind.toLowerCase()
  if (normalizedKind === 'replicasets' || normalizedKind === 'replicaset') {
    return <ReplicaSetDockLogs namespace={namespace} name={workloadName} />
  }
  const isScheduled = normalizedKind === 'cronjob' || normalizedKind === 'cronjobs' ||
    normalizedKind === 'cronworkflow' || normalizedKind === 'cronworkflows' ||
    normalizedKind === 'workflowtemplate' || normalizedKind === 'workflowtemplates' ||
    normalizedKind === 'clusterworkflowtemplate' || normalizedKind === 'clusterworkflowtemplates' ||
    normalizedKind === 'scaledjob' || normalizedKind === 'scaledjobs' ||
    normalizedKind === 'jobset' || normalizedKind === 'jobsets'

  return (
    <div className="h-full">
      {isScheduled ? (
        <ScheduledWorkloadLogsViewer
          kind={workloadKind}
          namespace={namespace}
          name={workloadName}
          viewNamespaces={viewNamespaces}
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
