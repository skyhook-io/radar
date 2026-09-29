import { useMemo, useState } from 'react'
import yaml from 'yaml'
import { ActionConfirmDialog } from '@skyhook-io/k8s-ui'
import { useResource } from '../../../api/client'
import { useCNPGWorkspace } from '../../../api/cnpg'
import { CreateResourceDialog } from '../../shared/CreateResourceDialog'
import { buildRestoreManifest, restoreSourcesFor, type RestoreSource } from './actionModel'

function sourceLabel(s: RestoreSource): string {
  switch (s.kind) {
    case 'objectStore':
      return `ObjectStore ${s.objectStore} · server ${s.serverName}`
    case 'inTree':
      return `Barman object store (in-tree) · server ${s.serverName}`
    default:
      return `Backup ${s.backup}`
  }
}

/**
 * Restore builds a new Cluster manifest and hands it to Radar's create flow,
 * which previews it with a server dry-run and creates it strictly (a name
 * that already exists fails instead of updating that object).
 */
export function CNPGRestoreDialog({ namespace, name, onClose }: { namespace: string; name: string; onClose: () => void }) {
  const { data: cluster } = useResource<any>('clusters', namespace, name, 'postgresql.cnpg.io')
  const workspace = useCNPGWorkspace([namespace])
  const sources = useMemo(() => (cluster ? restoreSourcesFor(cluster, workspace.data?.objects.backups ?? []) : []), [cluster, workspace.data])
  const [sourceIdx, setSourceIdx] = useState(0)
  const [newName, setNewName] = useState(`${name}-restore`)
  const [targetTime, setTargetTime] = useState('')
  const [manifest, setManifest] = useState<string | null>(null)
  const source = sources[sourceIdx]

  if (manifest) {
    return <CreateResourceDialog open onClose={onClose} initialYaml={manifest} initialMode="create" title={`Restore ${name} into a new cluster`} />
  }

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (!cluster || !source) return
        const m = buildRestoreManifest(cluster, source, newName, targetTime && source.kind !== 'backup' ? new Date(targetTime).toISOString() : undefined)
        setManifest(
          `# Restores ${name} into a new cluster. Review before creating:\n` +
            `# - the new cluster has no WAL archiving or backups until you add them;\n` +
            `#   do not point it at ${name}'s backup location.\n` +
            `# - the dry-run checks the manifest, not whether the backups are readable.\n` +
            yaml.stringify(m),
        )
      }}
      title={`Restore ${name} to a new cluster?`}
      subject={{ kind: 'Cluster', namespace, name }}
      effect="Creates a new Cluster that bootstraps from this cluster's backups. The source cluster is not touched."
      confirmLabel="Review manifest"
      disabledReason={
        !cluster ? 'Loading the cluster…' : sources.length === 0 ? 'This cluster has no backup destination and no completed Backup to restore from.' : !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(newName) ? 'The new name must be a valid Kubernetes object name.' : undefined
      }
    >
      <div className="grid grid-cols-[8rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
        <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-source">Restore from</label>
        <select id="cnpg-restore-source" value={sourceIdx} onChange={(e) => setSourceIdx(Number(e.target.value))} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm">
          {sources.map((s, i) => <option key={i} value={i}>{sourceLabel(s)}</option>)}
        </select>
        <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-name">New cluster</label>
        <input id="cnpg-restore-name" value={newName} onChange={(e) => setNewName(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 font-mono text-sm" />
        <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-time">Point in time</label>
        <div>
          <input
            id="cnpg-restore-time"
            type="datetime-local"
            value={targetTime}
            disabled={source?.kind === 'backup'}
            onChange={(e) => setTargetTime(e.target.value)}
            className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm disabled:opacity-50"
          />
          <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
            {source?.kind === 'backup' ? 'A named Backup restores to its end point.' : 'Local time; empty restores to the latest archived WAL.'}
          </div>
        </div>
      </div>
    </ActionConfirmDialog>
  )
}
