import { useMemo } from 'react'
import { cnpgDimensions } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime } from '../../api/cnpg'
import { useCNPGClusterHA } from '../../api/cnpg-ha'
import { cnpgReplicationGap, cnpgReplicationLive, withLiveReplication } from './runtimeAssessment'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'

/**
 * One Cluster's assessment, shared by the page header's chips, the Overview
 * and the tabs: the fleet row (cached objects, issues, Prometheus) enriched
 * with what the instance managers report, and the four dimensions derived
 * from it. Every consumer reads this, so none can read calmer than another.
 */
export function useCNPGClusterAssessment(namespace: string, name: string) {
  // The workspace is read for the object's own namespace: an explicitly opened
  // Cluster shows its facts whatever the namespace filter is.
  const { query, fleet } = useCNPGFleet([namespace])
  const runtime = useCNPGRuntime(namespace, name)
  const ha = useCNPGClusterHA(namespace, name)
  const baseRow = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const row = useMemo(() => (baseRow ? withLiveReplication(baseRow, runtime.data) : undefined), [baseRow, runtime.data])
  const dimensions = useMemo(
    () =>
      row
        ? cnpgDimensions({ row, ha: ha.data, replication: cnpgReplicationLive(runtime.data), replicationGap: cnpgReplicationGap(runtime.data, runtime.error) })
        : undefined,
    [row, ha.data, runtime.data, runtime.error],
  )
  return { query, fleet, runtime, ha, row, dimensions }
}
