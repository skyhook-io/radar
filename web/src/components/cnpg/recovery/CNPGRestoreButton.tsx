import { useState } from 'react'
import { History } from 'lucide-react'
import { Tooltip } from '@skyhook-io/k8s-ui'
import { CNPGRestoreDialog, type CNPGRestoreEntry } from './CNPGRestoreDialog'

/** Header action on a Backup or ObjectStore: restore into a new Cluster. */
export function CNPGRestoreButton({ namespace, entry, disabledReason, compact }: { namespace: string; entry: CNPGRestoreEntry; disabledReason?: string; compact?: boolean }) {
  const [open, setOpen] = useState(false)
  const label = entry.kind === 'backup' ? 'Restore from this backup' : 'Restore a cluster from this store'
  return (
    <>
      <Tooltip content={disabledReason ?? `${label} into a new Cluster`} position="bottom">
        <button
          type="button"
          disabled={!!disabledReason}
          onClick={() => setOpen(true)}
          className="inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          <History className="h-3.5 w-3.5" />
          {!compact && label}
        </button>
      </Tooltip>
      {open && <CNPGRestoreDialog namespace={namespace} entry={entry} onClose={() => setOpen(false)} />}
    </>
  )
}
