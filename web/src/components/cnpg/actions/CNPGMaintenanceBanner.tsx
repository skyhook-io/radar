import { useState } from 'react'
import { AlertBanner, Tooltip } from '@skyhook-io/k8s-ui'
import { useCNPGClusterCapabilities } from '../../../api/cnpg'
import { ClusterActionDialog } from './CNPGClusterActions'
import { capabilityReason } from '../../../api/actions'

/**
 * Standing notice while spec.nodeMaintenanceWindow.inProgress is true. The
 * mode suspends self-healing, so it must never be forgotten on: the way out is
 * on the banner itself.
 */
export function CNPGMaintenanceBanner({
  namespace,
  name,
  maintenance,
}: {
  namespace: string
  name: string
  maintenance?: { inProgress: boolean; reusePVC: boolean }
}) {
  const caps = useCNPGClusterCapabilities(namespace, name, !!maintenance?.inProgress)
  const [open, setOpen] = useState(false)
  if (!maintenance?.inProgress) return null
  const cap = caps.data?.actions.unsetMaintenance
  return (
    <div className="mb-3">
      <AlertBanner
        variant="warning"
        title="Node maintenance in progress"
        message={
          maintenance.reusePVC
            ? 'Self-healing, rolling updates and disruption budgets are limited: an instance on a drained node waits for that node and keeps its volume. Lift this as soon as the node work is done.'
            : 'Self-healing, rolling updates and disruption budgets are limited: an instance on a drained node is rebuilt elsewhere on a new volume. Lift this as soon as the node work is done.'
        }
      >
        <div className="mt-2">
          <Tooltip content={cap ? capabilityReason(cap) : undefined}>
            <button
              type="button"
              disabled={!cap?.allowed}
              onClick={() => setOpen(true)}
              className="btn-brand px-3 py-1 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50"
            >
              Lift maintenance…
            </button>
          </Tooltip>
          <span className="ml-2 text-xs text-theme-text-tertiary">spec.nodeMaintenanceWindow.inProgress</span>
        </div>
      </AlertBanner>
      {open && caps.data && <ClusterActionDialog kind="unsetMaintenance" caps={caps.data} namespace={namespace} name={name} onClose={() => setOpen(false)} />}
    </div>
  )
}
