import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { Collapse, ConfirmDialog, Disclosure, Input } from '@skyhook-io/k8s-ui'
import { Check, ExternalLink } from 'lucide-react'
import {
  getOpenObserveState,
  removeOpenObserveConnection,
  setupOpenObserve,
  type OpenObserveConnection,
  type OpenObserveLabelValue,
  type OpenObserveMatch,
  type OpenObserveSetupNotes,
  type OpenObserveSetupRequest,
  type OpenObserveState,
} from '../../api/openobserveSetup'
import { familyLabel } from './openobserveStatus'

const SERVICE_ACCOUNT_DOCS = 'https://openobserve.ai/docs/user-guide/identity-and-access-management/service-accounts/'
const inputClass = 'block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500'

interface Draft {
  baseUrl: string
  org: string
  user: string
  token: string
  caCertFile: string
  clusterLabel: string
}

const draftFrom = (connection: OpenObserveConnection | null): Draft => ({
  baseUrl: connection?.baseUrl ?? '',
  org: connection?.org ?? 'default',
  user: connection?.user ?? '',
  token: '',
  caCertFile: connection?.caCertFile ?? '',
  clusterLabel: connection?.clusterLabel ?? '',
})

const sameTarget = (a: Draft, b: Draft) =>
  a.baseUrl.trim() === b.baseUrl.trim() && a.org.trim() === b.org.trim() && a.user.trim() === b.user.trim()

const sameDraft = (a: Draft, b: Draft) =>
  (Object.keys(a) as (keyof Draft)[]).every((key) => a[key] === b[key])

type Result =
  | { kind: 'saved'; notes?: OpenObserveSetupNotes; reloadFailed?: boolean }
  | { kind: 'confirm'; reason?: string; suggestion: OpenObserveLabelValue; body: OpenObserveSetupRequest }
  | { kind: 'not_found'; reason?: string; names: OpenObserveLabelValue[]; uid?: string }
  | { kind: 'error'; message: string }

const pair = (value: OpenObserveLabelValue) => `${value.label}=${value.value}`

export const matchSummary = (match: OpenObserveMatch) =>
  `Matched by ${match.by === 'id' ? 'cluster ID' : 'confirmed name'} (${pair(match)})`

const connectionLine = (connection: OpenObserveConnection) =>
  `${connection.baseUrl} · organization ${connection.org} · user ${connection.user}`

export function OpenObserveSetup({
  state,
  locked = false,
  status,
  onStateChange,
  onChanged,
  onUsePrometheus,
  onDirtyChange,
  onBusyChange,
}: {
  state: OpenObserveState
  locked?: boolean
  status?: ReactNode
  onStateChange: (state: OpenObserveState) => void
  onChanged: () => void
  onUsePrometheus: () => Promise<void>
  onDirtyChange?: (dirty: boolean) => void
  onBusyChange?: (busy: boolean) => void
}) {
  const id = useId()
  const { connection, cluster } = state
  const match = cluster.match
  const [editing, setEditing] = useState(false)
  const baseline = useMemo(() => draftFrom(connection), [connection])
  const [draft, setDraft] = useState<Draft>(baseline)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<Result | null>(null)
  const [confirmingRemove, setConfirmingRemove] = useState(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const showForm = !locked && (!connection || editing)
  // A token from an environment variable is kept only for the same address,
  // organization and user; the server never sends a saved secret elsewhere.
  const envToken = !!connection && connection.tokenSource === 'env' && sameTarget(draft, baseline)
  const dirty = showForm && !sameDraft(draft, baseline)
  useEffect(() => onDirtyChange?.(dirty), [dirty, onDirtyChange])
  useEffect(() => onBusyChange?.(busy), [busy, onBusyChange])
  useEffect(() => () => {
    onDirtyChange?.(false)
    onBusyChange?.(false)
  }, [onDirtyChange, onBusyChange])

  const update = (changes: Partial<Draft>) => {
    setDraft((value) => ({ ...value, ...changes }))
    if (result?.kind === 'confirm') setResult(null)
  }

  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    try {
      await action()
    } catch (error) {
      if (mounted.current) setResult({ kind: 'error', message: error instanceof Error ? error.message : String(error) })
    } finally {
      if (mounted.current) setBusy(false)
    }
  }

  const formBody = (): OpenObserveSetupRequest => ({
    baseUrl: draft.baseUrl.trim(),
    org: draft.org.trim(),
    user: draft.user.trim(),
    caCertFile: draft.caCertFile.trim(),
    clusterLabel: draft.clusterLabel.trim(),
    ...(draft.token && !envToken ? { token: draft.token } : {}),
  })

  const submit = (body: OpenObserveSetupRequest) => run(async () => {
    setResult(null)
    const response = await setupOpenObserve(cluster.uid ? { ...body, clusterUid: cluster.uid } : body)
    if (!mounted.current) return
    if (response.outcome === 'saved') {
      setResult({ kind: 'saved', notes: response.notes })
      setEditing(false)
      onChanged()
      let next: OpenObserveState
      try {
        next = await getOpenObserveState()
      } catch {
        if (mounted.current) setResult({ kind: 'saved', notes: response.notes, reloadFailed: true })
        return
      }
      if (!mounted.current) return
      setDraft(draftFrom(next.connection))
      onStateChange(next)
    } else if (response.outcome === 'confirm' && response.suggestion) {
      setResult({ kind: 'confirm', reason: response.reason, suggestion: response.suggestion, body })
    } else {
      setResult({ kind: 'not_found', reason: response.reason, names: response.clusterNames ?? [], uid: response.clusterUid ?? cluster.uid })
    }
  })

  const testAndSave = () => {
    if (result?.kind === 'confirm') void submit({ ...result.body, confirm: result.suggestion })
    else void submit(showForm ? formBody() : { useSaved: true })
  }

  const removeAll = () => {
    setConfirmingRemove(false)
    void run(async () => {
      setResult(null)
      const next = await removeOpenObserveConnection()
      if (!mounted.current) return
      setEditing(false)
      setDraft(draftFrom(next.connection))
      onStateChange(next)
      onChanged()
    })
  }

  const startEditing = () => {
    setDraft(baseline)
    setResult(null)
    setEditing(true)
  }
  const cancelEditing = () => {
    setDraft(baseline)
    setResult(null)
    setEditing(false)
  }

  const shared = connection && connection.clusters.length > 1
    ? <p className="text-xs text-theme-text-secondary">Shared by {connection.clusters.length} clusters. Changes apply to all of them.</p>
    : null
  const contexts = connection?.clusters.map((c) => c.context).filter(Boolean) ?? []

  const statusRow = status ? <div className="flex flex-wrap items-center gap-x-3 gap-y-1">{status}</div> : null

  if (locked) {
    return (
      <div className="space-y-2 text-sm">
        {statusRow}
        {match && <p className="text-theme-text-primary">{matchSummary(match)}</p>}
        {connection
          ? <p className="break-words text-xs text-theme-text-secondary">{connectionLine(connection)}</p>
          : <p className="text-xs text-theme-text-secondary">No OpenObserve connection saved.</p>}
      </div>
    )
  }

  const notes = result?.kind === 'saved' ? [
    result.notes?.collector && 'Data comes from the OpenObserve collector.',
    // The live status line repeats the warning, version and coverage notes
    // once the match settles; only say them here when that line isn't shown.
    !status && result.notes?.missing?.length && `No data for: ${result.notes.missing.map(familyLabel).join(', ')}.`,
    !status && result.notes?.warning,
    !status && result.notes?.versionWarning,
    result.reloadFailed && "Saved, but Radar couldn't reload this page. Reopen Settings to see the match.",
  ].filter((note): note is string => !!note) : []

  const outcome = result && result.kind !== 'saved' ? (
    result.kind === 'error' ? (
      <p role="alert" className="text-xs text-semantic-error">{result.message}</p>
    ) : result.kind === 'confirm' ? (
      <p role="status" className="text-xs text-warning-text">
        {result.reason ?? `OpenObserve has metrics for ${pair(result.suggestion)}, matched only by name.`}
      </p>
    ) : (
      <div role="status" className="space-y-1 text-xs text-theme-text-secondary">
        {result.reason && <p className="text-warning-text">{result.reason}</p>}
        <p>
          {result.names.length
            ? `OpenObserve has metrics for: ${result.names.map(pair).join(', ')}.`
            : 'OpenObserve has no cluster metrics yet.'}
        </p>
        <p>If OpenObserve names clusters with another label, set it under Advanced.</p>
        {result.uid && <p>To match by ID, add the label radar_cluster_uid={result.uid} to this cluster’s metrics.</p>}
      </div>
    )
  ) : null

  const saveButton = (
    <button type="button" onClick={testAndSave} disabled={busy} className="btn-brand min-w-28 px-3 py-2 text-xs">
      {busy ? 'Testing…' : result?.kind === 'confirm' ? `Confirm '${result.suggestion.value}' and save` : 'Test and save'}
    </button>
  )

  return (
    <fieldset disabled={busy} aria-label="OpenObserve connection settings" className="min-w-0 space-y-4">
      {statusRow}
      {cluster.error && <p className="text-xs text-warning-text">{cluster.error}</p>}
      {showForm ? (
        <div className="space-y-4">
          {shared}
          <div className="space-y-1">
            <label htmlFor={`${id}-url`} className="block text-sm font-medium text-theme-text-primary">Address</label>
            <Input id={`${id}-url`} value={draft.baseUrl} onChange={(e) => update({ baseUrl: e.target.value })} placeholder="https://openobserve.example.com" spellCheck={false} className={inputClass} />
          </div>
          <div className="space-y-1">
            <label htmlFor={`${id}-org`} className="block text-sm font-medium text-theme-text-primary">Organization</label>
            <Input id={`${id}-org`} value={draft.org} onChange={(e) => update({ org: e.target.value })} spellCheck={false} className={inputClass} />
          </div>
          <div className="space-y-1">
            <label htmlFor={`${id}-user`} className="block text-sm font-medium text-theme-text-primary">User or service account email</label>
            <Input id={`${id}-user`} value={draft.user} onChange={(e) => update({ user: e.target.value })} autoComplete="off" spellCheck={false} className={inputClass} />
            <a href={SERVICE_ACCOUNT_DOCS} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline">
              Create a service account in OpenObserve
              <ExternalLink aria-hidden="true" className="h-3 w-3" />
            </a>
          </div>
          <div className="space-y-1">
            <label htmlFor={`${id}-token`} className="block text-sm font-medium text-theme-text-primary">Token</label>
            {envToken ? (
              <p id={`${id}-token`} className="text-sm text-theme-text-secondary">Token from ${connection.tokenEnv}</p>
            ) : (
              <Input
                id={`${id}-token`}
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                value={draft.token}
                onChange={(e) => update({ token: e.target.value })}
                placeholder={connection?.tokenSource === 'saved' && sameTarget(draft, baseline) ? 'Saved — leave empty to keep' : undefined}
                className={inputClass}
              />
            )}
            {connection && connection.tokenSource !== 'none' && !sameTarget(draft, baseline) && (
              <p className="text-xs text-theme-text-secondary">A new address, organization or user needs its own token.</p>
            )}
            <p className="text-xs text-theme-text-tertiary">Saved in ~/.radar/config.json, readable only by you.</p>
          </div>
          <Disclosure
            summary="Advanced"
            summaryClassName="text-sm font-medium text-theme-text-secondary"
            defaultOpen={!!(baseline.caCertFile || baseline.clusterLabel)}
          >
            <div className="space-y-4 pt-3">
              <div className="space-y-1">
                <label htmlFor={`${id}-ca`} className="block text-sm font-medium text-theme-text-primary">CA certificate file</label>
                <Input id={`${id}-ca`} value={draft.caCertFile} onChange={(e) => update({ caCertFile: e.target.value })} placeholder="/path/to/ca.pem" spellCheck={false} className={inputClass} />
              </div>
              <div className="space-y-1">
                <label htmlFor={`${id}-label`} className="block text-sm font-medium text-theme-text-primary">Cluster label</label>
                <p className="text-xs text-theme-text-tertiary">Only when OpenObserve names clusters with a label Radar doesn’t know.</p>
                <Input id={`${id}-label`} value={draft.clusterLabel} onChange={(e) => update({ clusterLabel: e.target.value })} spellCheck={false} className={inputClass} />
              </div>
            </div>
          </Disclosure>
        </div>
      ) : match && connection ? (
        <div className="card-inner-lg space-y-2">
          <p className="text-sm text-theme-text-primary">{matchSummary(match)}</p>
          <p className="break-words text-xs text-theme-text-secondary">{connectionLine(connection)}</p>
          {shared}
        </div>
      ) : connection ? (
        <p className="break-words text-sm text-theme-text-secondary">
          Use the saved OpenObserve connection ({connection.baseUrl}, user {connection.user})
        </p>
      ) : null}
      <div className="space-y-2 border-t border-theme-border pt-4">
        <div className="flex min-h-8 flex-wrap items-center gap-3">
          {(showForm || !match) && saveButton}
          {connection && !editing && (
            <button type="button" onClick={startEditing} className="text-xs font-medium text-accent-text hover:underline">Edit connection</button>
          )}
          {editing && (
            <button type="button" onClick={cancelEditing} className="text-xs text-theme-text-secondary hover:underline">Cancel</button>
          )}
          {match && !editing && (
            <button type="button" onClick={() => void run(async () => { setResult(null); await onUsePrometheus() })} className="text-xs font-medium text-accent-text hover:underline">
              Use Prometheus for this cluster
            </button>
          )}
          {result?.kind === 'saved' && (
            <span role="status" className="flex items-center gap-1.5 text-xs text-theme-text-secondary">
              <Check aria-hidden="true" className="h-3.5 w-3.5 text-[var(--color-success-dark)] dark:text-[var(--color-success-light)]" />
              Saved
            </span>
          )}
        </div>
        <Collapse open={notes.length > 0}>
          <ul className="space-y-0.5 text-xs text-theme-text-secondary">
            {notes.map((note) => <li key={note}>{note}</li>)}
          </ul>
        </Collapse>
        <Collapse open={!!outcome}>{outcome}</Collapse>
        {connection && !editing && (
          <button type="button" onClick={() => setConfirmingRemove(true)} className="self-start text-xs font-medium text-semantic-error hover:underline">
            Remove saved OpenObserve connection
          </button>
        )}
      </div>
      <ConfirmDialog
        open={confirmingRemove}
        onClose={() => setConfirmingRemove(false)}
        onConfirm={removeAll}
        title="Remove the saved OpenObserve connection?"
        message={contexts.length
          ? `These clusters go back to their own Prometheus: ${contexts.join(', ')}.`
          : 'No cluster reads from it yet.'}
        confirmLabel="Remove"
        variant="warning"
        showWarning={false}
      />
    </fieldset>
  )
}
