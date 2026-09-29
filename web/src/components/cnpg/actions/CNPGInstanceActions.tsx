import { useState } from 'react'
import { useCNPGClusterCapabilities, type CNPGActionCapability, type CNPGClusterActionName } from '../../../api/cnpg'
import { ClusterActionDialog } from './CNPGClusterActions'
import { CNPGDestroyInstanceDialog } from './CNPGDestroyInstanceDialog'
import { useOpenCNPGPsql } from './useOpenCNPGPsql'
import { Tooltip } from '@skyhook-io/k8s-ui'

const LINK = 'text-accent-text hover:underline disabled:cursor-not-allowed disabled:text-theme-text-disabled disabled:no-underline'

/** Per-instance operations shown on a replication row. */
export function CNPGInstanceActions({ namespace, cluster, pod }: { namespace: string; cluster: string; pod: string }) {
  const caps = useCNPGClusterCapabilities(namespace, cluster)
  const [open, setOpen] = useState<CNPGClusterActionName | null>(null)
  const [destroying, setDestroying] = useState(false)
  const openPsql = useOpenCNPGPsql()
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

  const button = (key: string, label: string, reason: string | undefined, onClick: () => void) => (
    <Tooltip key={key} content={reason}>
      <button type="button" disabled={!!reason} onClick={onClick} className={LINK}>
        {label}
      </button>
    </Tooltip>
  )
  const link = (id: CNPGClusterActionName, label: string, extraReason?: string) => {
    const cap = capFor(id) ?? data.actions[id]
    return button(id, label, extraReason ?? (!cap.allowed ? cap.reason ?? 'Not allowed' : undefined), () => setOpen(id))
  }
  const reasonOf = (cap: CNPGActionCapability | undefined) => (!cap ? 'Not available for this instance' : !cap.allowed ? cap.reason ?? 'Not allowed' : undefined)

  return (
    <>
      {button('psql', 'psql', reasonOf(per?.psql), () => openPsql(namespace, pod, isPrimary))}
      {!isPrimary && link('switchover', 'Promote')}
      {link('restartInstance', 'Restart')}
      {isFenced
        ? link('unfence', 'Lift fence', allFenced ? 'The whole cluster is fenced; lift it from the cluster menu' : undefined)
        : link('fence', 'Fence')}
      {!isPrimary && button('destroy', 'Destroy…', reasonOf(per?.destroy), () => setDestroying(true))}
      {open && <ClusterActionDialog kind={open} caps={data} namespace={namespace} name={cluster} initialPod={pod} onClose={() => setOpen(null)} />}
      {destroying && <CNPGDestroyInstanceDialog namespace={namespace} cluster={cluster} pod={pod} onClose={() => setDestroying(false)} />}
    </>
  )
}
