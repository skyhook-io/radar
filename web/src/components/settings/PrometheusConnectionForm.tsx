import { useEffect, useRef, useState } from 'react'
import { Check, Loader2, Plug, Plus, X } from 'lucide-react'
import { Input } from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'
import { prometheusHeadersFromRows } from './settings-state'

export interface PrometheusApplyResult { connected: boolean; address?: string; error?: string }

type ApplyState =
  | { status: 'idle' }
  | { status: 'applying' }
  | { status: 'connected'; address: string }
  | { status: 'unreachable'; error: string } // persisted, but the probe failed
  | { status: 'failed'; error: string }       // request itself failed — nothing saved

type HeaderRow = { key: string; value: string }

export function PrometheusConnectionForm({
  local,
  value,
  onChange,
  configuredHeaderKeys,
  serverManaged,
  headersManaged,
  urlFromFlag,
  onApplied,
  onApply,
  scopeDescription,
  onDirtyChange,
}: {
  local: boolean
  value: string
  onChange: (value: string) => void
  configuredHeaderKeys: string[]
  serverManaged: boolean
  headersManaged: boolean
  urlFromFlag: boolean
  onApplied?: (url: string) => void
  onApply: (url: string, headers?: Record<string, string>) => Promise<PrometheusApplyResult>
  scopeDescription?: string
  onDirtyChange?: (dirty: boolean) => void
}) {
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const [apply, setApply] = useState<ApplyState>({ status: 'idle' })
  // null = not editing headers (preserve what's stored). A non-null array means
  // the user opened the editor; on Apply we send it verbatim, replacing all
  // stored headers (values are write-only, so the server never sends them back).
  const [headerRows, setHeaderRows] = useState<HeaderRow[] | null>(null)
  // Show the server's configured header keys, but let a successful apply override
  // optimistically (config isn't refetched). Derived from the prop — not a
  // mount-time snapshot — so it stays correct as config loads asynchronously.
  const [appliedKeys, setAppliedKeys] = useState<string[] | null>(null)
  const storedKeys = appliedKeys ?? configuredHeaderKeys
  useEffect(() => { onDirtyChange?.(headerRows !== null) }, [headerRows, onDirtyChange])

  const clearStatus = () => {
    if (apply.status !== 'applying') setApply({ status: 'idle' })
  }

  // Footer Reset (and any external edit) clears the URL field without a keystroke;
  // drop a stale "Connected"/"Saved" status so it doesn't describe an emptied field.
  useEffect(() => {
    setApply((s) => (s.status === 'idle' || s.status === 'applying' ? s : { status: 'idle' }))
  }, [value])

  const handleApply = async () => {
    setApply({ status: 'applying' })
    // Decide what to do with headers. undefined = leave them untouched. Only send
    // a replacement when the editor has real content, or {} when the user emptied
    // every row (explicit clear) — blank in-progress rows must NOT wipe stored
    // secrets just because the editor happens to be open for a URL-only change.
    try {
      const editedHeaders = prometheusHeadersFromRows(headerRows)
      const data = await onApply(value.trim(), editedHeaders)
      if (!mounted.current) return
      onApplied?.(value.trim())
      if (editedHeaders !== undefined) {
        setAppliedKeys(Object.keys(editedHeaders).sort())
      }
      if (headerRows !== null) {
        setHeaderRows(null)
      }
      if (data.connected) {
        setApply({ status: 'connected', address: data.address || value.trim() })
      } else {
        setApply({ status: 'unreachable', error: data.error || 'not reachable' })
      }
    } catch (err) {
      if (mounted.current) setApply({ status: 'failed', error: err instanceof Error ? err.message : String(err) })
    }
  }

  return (
    <div>
      <p className="text-xs text-theme-text-tertiary mb-3">
        Connect existing Prometheus-compatible data for resource usage, workload HTTP metrics and rightsizing. Available charts depend on collected metrics.
      </p>
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        Metrics backend URL
      </label>
      <p className="text-xs text-theme-text-tertiary mb-1">
        Base URL reachable from Radar, not your browser — Prometheus, VictoriaMetrics, Thanos or
        Mimir. Include any backend path prefix, but not /api/v1/query. Leave empty for cluster discovery; headers require a URL.
      </p>
      <div className="flex items-center gap-2">
        <Input
          value={value}
          disabled={apply.status === 'applying'}
          onChange={(e) => onChange(e.target.value)}
          placeholder="http://prometheus-server.monitoring:9090"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
        <Tooltip content="Save and apply this connection, then check reachability. Applying clears any workload scope override and resumes automatic identity matching." wrapperClassName="shrink-0">
          <button
            onClick={handleApply}
            disabled={apply.status === 'applying'}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-md disabled:opacity-50"
          >
            {apply.status === 'applying'
              ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
              : <Plug className="w-3.5 h-3.5" />}
            Apply now
          </button>
        </Tooltip>
      </div>
      {apply.status === 'connected' ? (
        <p className="mt-1 flex items-center gap-1 text-xs text-green-600 dark:text-green-400/80">
          <Check className="w-3 h-3 shrink-0" />
          Connected to {apply.address} — applied, no restart needed
        </p>
      ) : apply.status === 'unreachable' ? (
        <p className="mt-1 text-xs text-amber-600 dark:text-amber-400/80">
          Saved, but not reachable: {apply.error}
        </p>
      ) : apply.status === 'failed' ? (
        <p className="mt-1 text-xs text-red-600 dark:text-red-400/80">
          Couldn't apply: {apply.error}
        </p>
      ) : (
        <p className="mt-1 text-xs text-theme-text-tertiary">
          Saves and applies before checking the connection.
        </p>
      )}
      <p className="mt-2 text-xs text-theme-text-tertiary">
        {scopeDescription ?? (local ? 'Saved for this cluster.' : 'Changes affect this Radar installation. Use deployment settings for configuration that survives Pod replacement.')}
        {' Changing servers requires replacing or clearing the saved headers.'}
      </p>
      {serverManaged && (
        <p className="mt-2 text-xs text-theme-text-secondary">
          Server controlled by startup configuration. To set or change the server,
          {local ? ' update the startup flags or environment references and restart Radar.' : ' update the deployment configuration (such as Helm values) and restart Radar.'}
        </p>
      )}
      {urlFromFlag && (
        <p className="mt-1 text-xs text-theme-text-secondary">
          Path edits apply until restart; the URL supplied at launch will then be restored.
        </p>
      )}

      {/* Auth headers — for token / multi-tenant backends (Bearer, X-Scope-OrgID). */}
      <div className="mt-3">
        {headerRows === null ? (
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs text-theme-text-tertiary">
              {storedKeys.length > 0
                ? <>Auth headers: <span className="text-theme-text-secondary">{storedKeys.join(', ')}</span> <span className="text-theme-text-disabled">(values hidden)</span></>
                : 'No auth headers'}
            </span>
            {!headersManaged && <button
              onClick={() => { setHeaderRows([{ key: '', value: '' }]); clearStatus() }}
              disabled={apply.status === 'applying'}
              className="shrink-0 text-xs font-medium text-accent-text hover:underline"
            >
              {storedKeys.length > 0 ? 'Edit headers' : 'Add auth headers'}
            </button>}
          </div>
        ) : (
          <div className="rounded-md border border-theme-border bg-theme-elevated/40 p-2.5 space-y-2">
            {headerRows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  value={row.key}
                  disabled={apply.status === 'applying'}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, key: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="Header (e.g. Authorization)"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <input
                  type="password"
                  value={row.value}
                  disabled={apply.status === 'applying'}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, value: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="Value (e.g. Bearer …)"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <Tooltip content="Remove header" wrapperClassName="shrink-0">
                  <button
                    onClick={() => setHeaderRows((rows) => rows!.filter((_, j) => j !== i))}
                    disabled={apply.status === 'applying'}
                    className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              </div>
            ))}
            <div className="flex items-center justify-between gap-2">
              <button
                onClick={() => setHeaderRows((rows) => [...rows!, { key: '', value: '' }])}
                disabled={apply.status === 'applying'}
                className="flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
              >
                <Plus className="w-3 h-3" /> Add header
              </button>
              <button
                onClick={() => { setHeaderRows(null); clearStatus() }}
                disabled={apply.status === 'applying'}
                className="text-xs text-theme-text-tertiary hover:text-theme-text-primary"
              >
                Cancel
              </button>
            </div>
            <p className="text-xs text-theme-text-tertiary">
              Saved when you click Apply now. Entered headers replace all stored
              ones — values are hidden, so re-enter any you want to keep. Leave
              all rows blank to keep existing headers unchanged. To clear all saved
              headers, remove every row and click Apply now.
            </p>
          </div>
        )}
      </div>
      {headersManaged ? (
        <p className="mt-2 text-xs text-theme-text-secondary">
          Headers are controlled by startup configuration. Change them at their source and restart Radar.
        </p>
      ) : storedKeys.length > 0 && (
        <button
          onClick={() => { setHeaderRows([]); clearStatus() }}
          disabled={apply.status === 'applying'}
          className="mt-2 text-xs text-theme-text-secondary hover:underline"
        >
          Clear saved headers
        </button>
      )}
      {headerRows?.length === 0 && (
        <p className="mt-1 text-xs text-warning-text">Headers will be cleared when you click Apply now.</p>
      )}
    </div>
  )
}
