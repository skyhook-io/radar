import { useState } from 'react'
import { ActionConfirmDialog, PaneLoader } from '@skyhook-io/k8s-ui'
import { cnpgActionErrorCode, useCNPGAction } from '../../../api/cnpg'
import { useCNPGDestroyPlan } from '../../../api/cnpg-sessions'
import { useToast } from '../../ui/Toast'

/**
 * `kubectl cnpg destroy` for one standby: its volumes are deleted (or kept,
 * detached), then its Pod and its Jobs. The operator replaces it with a new
 * instance under a new name. Binds the Pod and volume identities reviewed.
 */
export function CNPGDestroyInstanceDialog({ namespace, cluster, pod, onClose }: { namespace: string; cluster: string; pod: string; onClose: () => void }) {
  const plan = useCNPGDestroyPlan(namespace, cluster, pod)
  const mutation = useCNPGAction('clusters', namespace, cluster)
  const { showSuccess } = useToast()
  const [keep, setKeep] = useState(false)
  const data = plan.data
  const cap = data ? (keep ? data.actions.keep : data.actions.delete) : undefined
  const pvcNames = data?.pvcs.map((p) => p.name) ?? []

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
            successMessage: '',
          },
          {
            onSuccess: (r) => {
              showSuccess(r.message)
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
            ]
          : []
      }
      notes={[
        'The volumes are handled before the Pod, as kubectl cnpg destroy does, so the operator never recreates this Pod on them.',
        'Use this for a standby that cannot rejoin (for example after a failed pg_rewind or a corrupted volume). A plain restart deletes only the Pod and keeps the volumes attached.',
        keep ? 'Kept volumes stay in the namespace, no longer owned by the cluster; delete them yourself once you no longer need them.' : null,
      ].filter(Boolean) as string[]}
      disabledReason={
        plan.isLoading
          ? 'Reading the instance’s volumes…'
          : !data
            ? plan.error instanceof Error ? plan.error.message : 'The destroy plan could not be read'
            : !cap?.allowed
              ? cap?.reason ?? 'Not allowed'
              : undefined
      }
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={cnpgActionErrorCode(mutation.error) === 'outcome_unknown'}
    >
      {plan.isLoading && <PaneLoader label="Reading the instance’s volumes…" className="h-16" />}
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
