import { useState } from 'react'
import { History } from 'lucide-react'
import { Tooltip } from '@skyhook-io/k8s-ui'
import { CNPGRestoreDialog, type CNPGRestoreEntry } from './CNPGRestoreDialog'

/** Restore into a new Cluster, from a Backup, an ObjectStore or a Cluster's backups. */
export function CNPGRestoreButton({ namespace, entry, disabledReason, compact }: { namespace: string; entry: CNPGRestoreEntry; disabledReason?: string; compact?: boolean }) {
  const [open, setOpen] = useState(false)
  const label = entry.kind === 'backup' ? 'Restore from this backup' : entry.kind === 'cluster' ? 'Restore to a new cluster' : 'Restore a cluster from this store'
  return (
    <>
      <Tooltip content={disabledReason ?? `${label} into a new Cluster`} position="bottom">
        <button
          type="button"
          aria-disabled={!!disabledReason}
          onClick={() => { if (!disabledReason) setOpen(true) }}
          className={`btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium ${disabledReason ? 'cursor-not-allowed opacity-50' : ''}`}
        >
          <History className="h-3.5 w-3.5" />
          {!compact && label}
        </button>
      </Tooltip>
      {open && <CNPGRestoreDialog namespace={namespace} entry={entry} onClose={() => setOpen(false)} />}
    </>
  )
}
