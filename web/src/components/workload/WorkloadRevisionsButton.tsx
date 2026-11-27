import { useState } from 'react'
import { RevisionHistoryDialog } from '@skyhook-io/k8s-ui'
import { useWorkloadRevisions } from '../../api/client'
import { kindToPluralWithGroup } from '../../utils/navigation'

type Workload = { kind: string; group?: string; namespace: string; name: string }

export function supportsWorkloadRevisions(kind: string, group?: string) {
  const plural = kindToPluralWithGroup(kind, group ?? '').toLowerCase()
  return (['deployments', 'statefulsets', 'daemonsets'].includes(plural) && (!group || group === 'apps'))
    || (plural === 'rollouts' && group === 'argoproj.io')
}

export function WorkloadRevisionsButton({ kind, group, namespace, name }: Workload) {
  const plural = kindToPluralWithGroup(kind, group ?? '').toLowerCase()
  const [openTarget, setOpenTarget] = useState<string | null>(null)
  if (!supportsWorkloadRevisions(kind, group)) return null
  const target = JSON.stringify([plural, namespace, name])
  return (
    <>
      <button type="button" aria-label={`Revisions for ${name}`} className="text-xs font-medium text-accent-text hover:underline" onClick={() => setOpenTarget(target)}>Revisions</button>
      {openTarget === target && <ReadOnlyRevisions kind={plural} namespace={namespace} name={name} onClose={() => setOpenTarget(null)} />}
    </>
  )
}

function ReadOnlyRevisions({ kind, namespace, name, onClose }: Workload & { onClose: () => void }) {
  const revisions = useWorkloadRevisions(kind, namespace, name)
  return <RevisionHistoryDialog kind={kind} namespace={namespace} name={name} open onClose={onClose}
    revisions={revisions.isError ? undefined : revisions.data} isLoading={revisions.isLoading} error={revisions.error} />
}
