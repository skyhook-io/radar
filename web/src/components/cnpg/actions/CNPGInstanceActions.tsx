import { useState } from 'react'
import { useCNPGClusterCapabilities, type CNPGClusterActionName } from '../../../api/cnpg'
import { ClusterActionDialog } from './CNPGClusterActions'
import { Tooltip } from '@skyhook-io/k8s-ui'

/** Per-instance operations shown on a replication row. */
export function CNPGInstanceActions({ namespace, cluster, pod }: { namespace: string; cluster: string; pod: string }) {
  const caps = useCNPGClusterCapabilities(namespace, cluster)
  const [open, setOpen] = useState<CNPGClusterActionName | null>(null)
  const data = caps.data
  if (!data) return null
  const inst = data.facts.instances.find((i) => i.pod === pod)
  if (!inst) return null
  const isPrimary = pod === data.facts.currentPrimary
  const allFenced = data.facts.fencedInstances.all
  const isFenced = allFenced || data.facts.fencedInstances.instances.includes(pod)
  const per = data.instanceActions?.[pod]
  const capFor = (id: CNPGClusterActionName) =>
    id === 'switchover' ? per?.switchoverTarget : id === 'restartInstance' ? per?.restart : id === 'fence' ? per?.fence : id === 'unfence' ? per?.unfence : data.actions[id]

  const link = (id: CNPGClusterActionName, label: string, extraReason?: string) => {
    const cap = capFor(id) ?? data.actions[id]
    const reason = extraReason ?? (!cap.allowed ? cap.reason ?? 'Not allowed' : undefined)
    return (
      <Tooltip key={id} content={reason}>
        <button
          type="button"
          disabled={!!reason}
          onClick={() => setOpen(id)}
          className="text-accent-text hover:underline disabled:cursor-not-allowed disabled:text-theme-text-disabled disabled:no-underline"
        >
          {label}
        </button>
      </Tooltip>
    )
  }

  return (
    <>
      {!isPrimary && link('switchover', 'Promote')}
      {link('restartInstance', 'Restart')}
      {isFenced
        ? link('unfence', 'Lift fence', allFenced ? 'The whole cluster is fenced; lift it from the cluster menu' : undefined)
        : link('fence', 'Fence')}
      {open && <ClusterActionDialog kind={open} caps={data} namespace={namespace} name={cluster} initialPod={pod} onClose={() => setOpen(null)} />}
    </>
  )
}
