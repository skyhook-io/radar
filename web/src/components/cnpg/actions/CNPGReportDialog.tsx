import { useState } from 'react'
import { ActionConfirmDialog } from '@skyhook-io/k8s-ui'
import { downloadCNPGReport } from '../../../api/cnpg-recovery'
import { useConnection } from '../../../context/ConnectionContext'
import { downloadBlob } from '../../resources/file-browser-utils'

const INCLUDED = [
  'The Cluster, its instance and Job Pods, Jobs and PersistentVolumeClaims',
  'Events about those objects',
  'Backups, ScheduledBackups and Poolers of this cluster, and the ObjectStore it uses',
  'Operator and plugin versions and readiness',
  'The Runtime and Storage snapshots Radar shows (instance manager status, selected exporter metrics)',
  'report.json: what was read, what was skipped and why, and the Secrets referenced (names only)',
]

/**
 * The read-only report bundle, mirroring `kubectl cnpg report cluster`.
 * Secret values are never included; logs and the query text inside them are
 * opt-in.
 */
export function CNPGReportDialog({ namespace, name, onClose }: { namespace: string; name: string; onClose: () => void }) {
  const { connection } = useConnection()
  const [logs, setLogs] = useState(false)
  const [queryText, setQueryText] = useState(false)
  const [tailLines, setTailLines] = useState(1000)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={async () => {
        setBusy(true)
        setError(null)
        try {
          const { blob, filename } = await downloadCNPGReport(namespace, name, { logs, queryText: logs && queryText, tailLines })
          await downloadBlob(blob, filename)
          onClose()
        } catch (e) {
          if (!(e instanceof Error && e.message === 'cancelled')) setError(e instanceof Error ? e.message : String(e))
        } finally {
          setBusy(false)
        }
      }}
      title="Download a report bundle"
      subject={{ kind: 'Cluster', namespace, name }}
      context={connection.context || undefined}
      effect="Read-only: a zip of what this cluster looks like now, for a support ticket or a colleague. Everything is read with your permissions; what you cannot read is listed in report.json instead."
      confirmLabel={busy ? 'Building…' : 'Download'}
      isLoading={busy}
      error={error}
      errorTitle="The report could not be downloaded"
      notes={['Secret values are never included. The bundle is capped at 32 MiB; anything past that is listed as skipped.']}
    >
      <ul className="mb-3 list-disc space-y-0.5 pl-5 text-sm text-theme-text-secondary">
        {INCLUDED.map((i) => <li key={i}>{i}</li>)}
      </ul>
      <div className="space-y-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2">
        <label className="flex items-start gap-2 text-sm text-theme-text-primary">
          <input type="checkbox" className="mt-1" checked={logs} onChange={(e) => setLogs(e.target.checked)} />
          <span>
            Include Pod logs
            <span className="block text-xs text-theme-text-tertiary">Every container of the instance and Job Pods, plus the previous run of restarted containers. Needs get pods/log.</span>
          </span>
        </label>
        {logs && (
          <div className="ml-6 space-y-2">
            <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
              Last
              <select value={tailLines} onChange={(e) => setTailLines(Number(e.target.value))} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm text-theme-text-primary">
                {[200, 1000, 5000, 10000].map((n) => <option key={n} value={n}>{n.toLocaleString()}</option>)}
              </select>
              lines per container (at most 2 MiB each)
            </label>
            <label className="flex items-start gap-2 text-sm text-theme-text-primary">
              <input type="checkbox" className="mt-1" checked={queryText} onChange={(e) => setQueryText(e.target.checked)} />
              <span>
                Keep query text
                <span className="block text-xs text-theme-text-tertiary">
                  Without this, statements and bind parameters in PostgreSQL log records are replaced. Query text can contain personal or secret data.
                </span>
              </span>
            </label>
          </div>
        )}
      </div>
    </ActionConfirmDialog>
  )
}
