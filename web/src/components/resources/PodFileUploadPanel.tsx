import { Link } from 'react-router-dom'
import { Upload, Loader2 } from 'lucide-react'
import { AlertBanner } from '@skyhook-io/k8s-ui'
import { formatBytes } from '../../utils/format'
import { resourcePath } from '../../utils/navigation'
import { Tooltip } from '../ui/Tooltip'
import { POD_FILE_PERSISTENCE_NOTE, type MountHint } from './pod-file-upload'

export type UploadState =
  | { phase: 'idle' }
  | { phase: 'confirm'; name: string; dir: string; size: number }
  | { phase: 'uploading'; name: string; dir: string; sent: number; total: number }
  | { phase: 'saving'; name: string; dir: string }
  | { phase: 'saved'; path: string; replaced: boolean }
  | { phase: 'error'; name: string; message: string }

export function isUploadBusy(state: UploadState): boolean {
  return state.phase === 'uploading' || state.phase === 'saving' || state.phase === 'confirm'
}

interface UploadButtonProps {
  blocked: MountHint | null
  busy: boolean
  /** The listing for the selected container is still loading. */
  loading?: boolean
  onClick: () => void
}

export function PodFileUploadButton({ blocked, busy, loading = false, onClick }: UploadButtonProps) {
  const disabled = busy || loading || blocked !== null
  const tip = blocked ? blocked.reason
    : busy ? 'An upload is in progress'
    : loading ? 'Loading the listing…'
    : 'Upload a file into this directory (or drop one on the list)'
  return (
    <Tooltip content={tip}>
      <button
        type="button"
        onClick={onClick}
        disabled={disabled}
        aria-disabled={disabled}
        className="btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium mr-2 disabled:opacity-50 disabled:pointer-events-none"
      >
        <Upload className="w-4 h-4" />
        Upload
      </button>
    </Tooltip>
  )
}

interface UploadNoticesProps {
  namespace: string
  blocked: MountHint | null
  onNavigateAway: () => void
}

/** The persistence note, plus why uploads are off here when the pod spec says so. */
export function PodFileUploadNotices({ namespace, blocked, onNavigateAway }: UploadNoticesProps) {
  return (
    <>
      <AlertBanner variant="info" title="Uploads change the live container" message={POD_FILE_PERSISTENCE_NOTE} className="mb-3" />
      {blocked && (
        <AlertBanner
          variant="warning"
          title="Uploads are off in this directory"
          message={blocked.reason}
          className="mb-3"
          action={blocked.source ? (
            <Link
              to={resourcePath({ kind: blocked.source.kind, namespace, name: blocked.source.name })}
              onClick={onNavigateAway}
              className="text-xs text-accent hover:underline"
            >
              Open {blocked.source.kind} {blocked.source.name}
            </Link>
          ) : undefined}
        />
      )}
    </>
  )
}

interface UploadStatusProps {
  state: UploadState
  onConfirmReplace: () => void
  onCancel: () => void
  onDismiss: () => void
}

export function PodFileUploadStatus({ state, onConfirmReplace, onCancel, onDismiss }: UploadStatusProps) {
  switch (state.phase) {
    case 'idle':
      return null
    case 'confirm':
      return (
        <AlertBanner
          variant="warning"
          title={`Replace ${state.name}?`}
          message={`${state.dir === '/' ? '' : state.dir}/${state.name} already exists. Uploading ${formatBytes(state.size)} replaces it in the running container; its permissions and owner are kept.`}
          className="mb-3"
        >
          <div className="flex gap-2 mt-2">
            <button type="button" onClick={onConfirmReplace} className="btn-brand px-3 py-1 text-xs font-medium">Replace</button>
            <button type="button" onClick={onCancel} className="px-3 py-1 text-xs rounded border border-theme-border text-theme-text-secondary hover:bg-theme-hover">Cancel</button>
          </div>
        </AlertBanner>
      )
    case 'uploading': {
      const pct = state.total > 0 ? Math.min(100, Math.round((state.sent / state.total) * 100)) : 0
      return (
        <div className="mb-3 p-3 border border-theme-border rounded-lg bg-theme-surface" role="status">
          <div className="flex items-center justify-between gap-3 text-sm">
            <span className="min-w-0 truncate text-theme-text-primary">Uploading {state.name}</span>
            <span className="shrink-0 text-xs text-theme-text-tertiary">{formatBytes(state.sent)} of {formatBytes(state.total)}</span>
            <button type="button" onClick={onCancel} className="shrink-0 px-2 py-0.5 text-xs rounded border border-theme-border text-theme-text-secondary hover:bg-theme-hover">Cancel</button>
          </div>
          <div className="mt-2 h-1.5 rounded bg-theme-elevated overflow-hidden" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
            <div className="h-full bg-accent" style={{ width: `${pct}%` }} />
          </div>
        </div>
      )
    }
    case 'saving':
      return (
        <div className="mb-3 p-3 border border-theme-border rounded-lg bg-theme-surface flex items-center gap-2 text-sm text-theme-text-primary" role="status">
          <Loader2 className="w-4 h-4 animate-spin shrink-0" />
          <span className="truncate">Saving {state.name} in the container…</span>
        </div>
      )
    case 'saved':
      return (
        <AlertBanner
          variant="success"
          title={state.replaced ? `Replaced ${state.path}` : `Saved ${state.path}`}
          className="mb-3"
          action={<button type="button" onClick={onDismiss} className="text-xs text-theme-text-secondary hover:underline">Dismiss</button>}
        />
      )
    case 'error':
      return (
        <AlertBanner
          variant="error"
          title={`Could not upload ${state.name}`}
          message={state.message}
          className="mb-3"
          action={<button type="button" onClick={onDismiss} className="text-xs text-theme-text-secondary hover:underline">Dismiss</button>}
        />
      )
  }
}
