import { useState } from 'react'
import { ActionConfirmDialog, PaneLoader } from '@skyhook-io/k8s-ui'
import { cnpgActionErrorCode, useCNPGAction } from '../../../api/cnpg'
import { actionCompleted, actionOutcomeLocked } from '../../../api/actions'
import { useCNPGDestroyPlan } from '../../../api/cnpg-sessions'
import { useToast } from '../../ui/Toast'
import { trackCNPGOperation } from '../operations/store'
import { cnpgDestroyBlocker } from './actionModel'

/**
 * `kubectl cnpg destroy` for one standby: its volumes are deleted (or kept,
 * detached), then its Pod and its Jobs. The operator replaces it with a new
 * instance under a new name. Binds the Pod and volume identities reviewed.
 */
export function CNPGDestroyInstanceDialog({
  namespace,
  cluster,
  pod,
  onClose,
  onFenceFirst,
}: {
  namespace: string
  cluster: string
  pod: string
  onClose: () => void
  /** Opens the fence dialog for this instance; destroy requires the fence. */
  onFenceFirst?: () => void
}) {
  const plan = useCNPGDestroyPlan(namespace, cluster, pod)
  const mutation = useCNPGAction('clusters', namespace, cluster)
  const { showSuccess } = useToast()
  const [keep, setKeep] = useState(false)
  const data = plan.data
  const cap = data ? (keep ? data.actions.keep : data.actions.delete) : undefined
  const pvcNames = data?.pvcs.map((p) => p.name) ?? []
  const partial = cnpgActionErrorCode(mutation.error) === 'partial'
  const fenced = !!data && (data.facts.fencedInstances.all || data.facts.fencedInstances.instances.includes(pod))
  const completed = actionCompleted(mutation.error)
  const fenceOffered = !!data && !fenced && !!onFenceFirst

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (!data) return
        mutation.mutate(
          {
            action: 'destroyInstance',
            request: {
              reviewedContext: data.context,
              uid: data.uid,
              facts: data.facts as unknown as Record<string, unknown>,
              params: { pod, podUID: data.podUID, keepPVC: keep, pvcs: data.pvcs.map((p) => ({ name: p.name, uid: p.uid })) },
            },
          },
          {
            onSuccess: (r) => {
              showSuccess(r.message)
              trackCNPGOperation({
                kind: 'destroyInstance',
                label: `Destroy ${pod}`,
                context: data.context,
                namespace,
                cluster,
                clusterUID: data.uid,
                target: { name: pod, uid: data.podUID },
                baseline: { instances: data.facts.instances.map((i) => i.pod) },
              })
              onClose()
            },
          },
        )
      }}
      title={`Destroy instance ${pod}?`}
      subject={{ kind: 'Cluster', namespace, name: cluster }}
      context={data?.context}
      effect={
        keep
          ? `Deletes the Pod ${pod} and detaches its volumes from the cluster, keeping their data. The operator then creates a new instance with a new name and fresh volumes, which joins as a standby.`
          : `Deletes the Pod ${pod} and its volumes${pvcNames.length ? ` (${pvcNames.join(', ')})` : ''}; their data is gone. The operator then creates a new instance with a new name and fresh volumes, which joins as a standby.`
      }
      disruptive
      typedConfirmation={pod}
      confirmLabel={keep ? 'Destroy, keep volumes' : 'Destroy instance'}
      writes={
        data
          ? [
              ...data.pvcs.map((p) =>
                keep && p.owned
                  ? { summary: `update PersistentVolumeClaim ${namespace}/${p.name}`, detail: `ownerReferences: Cluster ${cluster} removed\nmetadata.annotations["cnpg.io/pvcStatus"] = "detached"` }
                  : keep
                    ? { summary: `leave PersistentVolumeClaim ${namespace}/${p.name}`, detail: 'already detached' }
                    : { summary: `delete PersistentVolumeClaim ${namespace}/${p.name}`, detail: `preconditions.uid = ${p.uid}` },
              ),
              { summary: `delete Pod ${namespace}/${pod}`, detail: data.podUID ? `preconditions.uid = ${data.podUID}` : 'the Pod no longer exists' },
              { summary: `delete Jobs labelled cnpg.io/instanceName=${pod}`, detail: data.jobsReadable ? (data.jobs.length ? data.jobs.join('\n') : 'none now') : 'not readable now; listed again when the action runs' },
              { summary: `patch Cluster ${namespace}/${cluster}`, detail: `metadata.annotations["cnpg.io/fencedInstances"]: ${pod} removed (unless fenced with ["*"])` },
            ]
          : []
      }
      notes={[
        'The volumes are handled before the Pod, as kubectl cnpg destroy does, so the operator never recreates this Pod on them.',
        fenceOffered
          ? `Once ${pod} is destroyed its name is removed from the fence (a fence on every instance, ["*"], stays).`
          : `${pod} must be fenced first; its name is removed from the fence once it is destroyed (a fence on every instance, ["*"], stays).`,
        'Use this for a standby that cannot rejoin (for example after a failed pg_rewind or a corrupted volume). A plain restart deletes only the Pod and keeps the volumes attached.',
        keep ? 'Kept volumes stay in the namespace, no longer owned by the cluster; delete them yourself once you no longer need them.' : null,
      ].filter(Boolean) as string[]}
      disabledReason={
        plan.isLoading
          ? 'Reading the instance’s volumes…'
          : !data
            ? plan.error instanceof Error ? plan.error.message : 'The destroy plan could not be read'
            : cnpgDestroyBlocker(cap, pod, fenceOffered)
      }
      guardSatisfied={!!cap?.allowed}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={actionOutcomeLocked(mutation.error)}
      outcomeTitle={partial ? 'Only part of this took effect' : undefined}
    >
      {partial && completed.length > 0 && (
        <div className="rounded-lg border border-theme-border bg-theme-elevated p-3 text-sm">
          <div className="mb-1 text-xs text-theme-text-secondary">Already done before it stopped</div>
          <ul className="space-y-0.5">
            {completed.map((c) => (
              <li key={c} className="font-mono text-xs text-theme-text-primary">
                {c}
              </li>
            ))}
          </ul>
          <div className="mt-2 text-xs text-theme-text-tertiary">Check the instance and its volumes before doing anything else; confirming again is disabled.</div>
        </div>
      )}
      {plan.isLoading && <PaneLoader label="Reading the instance’s volumes…" className="h-16" />}
      {fenceOffered && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
          <span>{pod} must be fenced before it is destroyed: the operator never promotes a fenced instance, so a failover cannot make it primary meanwhile.</span>
          <button type="button" onClick={onFenceFirst} className="btn-brand rounded-lg px-3 py-1.5 text-sm font-medium">
            Fence {pod} first
          </button>
        </div>
      )}
      {data && (
        <div className="space-y-3">
          <div>
            <div className="mb-1 text-xs text-theme-text-secondary">Volumes of {pod}</div>
            {!data.pvcsReadable ? (
              <div className="text-sm text-theme-text-tertiary">Not readable: {data.pvcReason}</div>
            ) : data.pvcs.length === 0 ? (
              <div className="text-sm text-theme-text-tertiary">None found for this instance.</div>
            ) : (
              <ul className="space-y-0.5 text-sm">
                {data.pvcs.map((p) => (
                  <li key={p.uid} className="font-mono text-xs">
                    {p.name}
                    <span className="font-sans text-theme-text-tertiary">
                      {' · '}
                      {[p.role, p.tablespace, p.capacity, p.storageClass, p.detached ? 'detached earlier' : null].filter(Boolean).join(' · ')}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs text-theme-text-secondary">Volumes</legend>
            <label className="flex items-start gap-2 text-sm">
              <input type="radio" name="cnpg-destroy-pvc" checked={!keep} onChange={() => setKeep(false)} className="mt-1" />
              <span>
                Delete them <span className="text-theme-text-tertiary">— the data on this standby is lost; the primary and the other standbys are unaffected</span>
              </span>
            </label>
            <label className="flex items-start gap-2 text-sm">
              <input type="radio" name="cnpg-destroy-pvc" checked={keep} onChange={() => setKeep(true)} className="mt-1" />
              <span>
                Keep them, detached <span className="text-theme-text-tertiary">(--keep-pvc) — for inspecting the old data; they still use storage</span>
              </span>
            </label>
          </fieldset>
        </div>
      )}
    </ActionConfirmDialog>
  )
}
