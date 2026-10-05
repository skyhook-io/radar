import { useState } from 'react'
import { useCNPGClusterCapabilities, type CNPGClusterActionName } from '../../../api/cnpg'
import { type ActionCapability, capabilityReason } from '../../../api/actions'
import { ClusterActionDialog } from './CNPGClusterActions'
import { CNPGDestroyInstanceDialog } from './CNPGDestroyInstanceDialog'
import { useOpenCNPGPsql } from './useOpenCNPGPsql'
import { Tooltip } from '@skyhook-io/k8s-ui'

const LINK = 'text-accent-text hover:underline aria-disabled:cursor-not-allowed aria-disabled:text-theme-text-disabled aria-disabled:no-underline'

/** Per-instance operations shown on a replication row. */
export function CNPGInstanceActions({ namespace, cluster, pod }: { namespace: string; cluster: string; pod: string }) {
  const caps = useCNPGClusterCapabilities(namespace, cluster)
  const [open, setOpen] = useState<CNPGClusterActionName | null>(null)
  const [blockedAction, setBlockedAction] = useState<CNPGClusterActionName | 'psql' | 'destroy'>()
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

  const button = (key: CNPGClusterActionName | 'psql' | 'destroy', label: string, reason: string | undefined, onClick: () => void) => (
    <Tooltip key={key} content={reason}>
      <button type="button" aria-disabled={!!reason} onClick={() => { if (reason) setBlockedAction(key); else { setBlockedAction(undefined); onClick() } }} className={LINK}>
        {label}
      </button>
    </Tooltip>
  )
  const link = (id: CNPGClusterActionName, label: string, extraReason?: string) => {
    const cap = capFor(id) ?? data.actions[id]
    return button(id, label, extraReason ?? capabilityReason(cap), () => setOpen(id))
  }
  const reasonOf = (cap: ActionCapability | undefined) => (!cap ? 'Not available for this instance' : capabilityReason(cap))

  const blockedReason = blockedAction === 'psql' ? reasonOf(per?.psql) : blockedAction === 'destroy' ? (isFenced ? reasonOf(per?.destroy) : undefined) : blockedAction === 'unfence' && allFenced ? 'The whole cluster is fenced; lift it from the cluster menu' : blockedAction ? capabilityReason(capFor(blockedAction) ?? data.actions[blockedAction]) : undefined

  return (
    <>
      {button('psql', 'psql', reasonOf(per?.psql), () => openPsql(namespace, pod, isPrimary))}
      {!isPrimary && link('switchover', 'Promote')}
      {link('restartInstance', 'Restart')}
      {isFenced
        ? link('unfence', 'Lift fence', allFenced ? 'The whole cluster is fenced; lift it from the cluster menu' : undefined)
        : link('fence', 'Fence')}
      {/* Unfenced, the dialog opens anyway: it explains the fence and offers it. */}
      {!isPrimary && button('destroy', 'Destroy…', isFenced ? reasonOf(per?.destroy) : undefined, () => setDestroying(true))}
      {blockedReason && <div role="status" className="basis-full text-xs text-theme-text-secondary">{blockedReason}</div>}
      {open && <ClusterActionDialog kind={open} caps={data} namespace={namespace} name={cluster} initialPod={pod} onClose={() => setOpen(null)} />}
      {destroying && (
        <CNPGDestroyInstanceDialog
          namespace={namespace}
          cluster={cluster}
          pod={pod}
          onClose={() => setDestroying(false)}
          onFenceFirst={
            per?.fence?.allowed
              ? () => {
                  setDestroying(false)
                  setOpen('fence')
                }
              : undefined
          }
        />
      )}
    </>
  )
}
