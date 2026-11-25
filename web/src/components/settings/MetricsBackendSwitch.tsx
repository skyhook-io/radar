import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import {
  getOpenObserveState,
  removeOpenObserveCluster,
  type OpenObserveState,
} from '../../api/openobserveSetup'
import { OpenObserveSetup } from './OpenObserveSetup'

type Backend = 'prometheus' | 'openobserve'

const OPTIONS: { value: Backend; title: string; description: string }[] = [
  { value: 'prometheus', title: 'Prometheus-compatible', description: 'Prometheus, VictoriaMetrics, Thanos or Grafana Mimir.' },
  { value: 'openobserve', title: 'OpenObserve', description: 'Read this cluster’s metrics from OpenObserve, filtered to this cluster.' },
]

export interface PrometheusViewProps {
  status?: ReactNode
  onDirtyChange: (dirty: boolean) => void
  onBusyChange: (busy: boolean) => void
}

export function MetricsBackendSwitch({
  locked = false,
  status,
  prometheus,
  onChanged,
  onDirtyChange,
  onBusyChange,
}: {
  locked?: boolean
  status?: ReactNode
  prometheus: (props: PrometheusViewProps) => ReactNode
  onChanged: () => void
  onDirtyChange?: (dirty: boolean) => void
  onBusyChange?: (busy: boolean) => void
}) {
  const [data, setData] = useState<OpenObserveState | null>(null)
  const [loadError, setLoadError] = useState('')
  const [retry, setRetry] = useState(0)
  const [picked, setChoice] = useState<Backend | null>(null)
  const [promDirty, setPromDirty] = useState(false)
  const [promBusy, setPromBusy] = useState(false)
  const [ooDirty, setOoDirty] = useState(false)
  const [ooBusy, setOoBusy] = useState(false)
  const [releasing, setReleasing] = useState(false)
  const [releaseError, setReleaseError] = useState('')
  const busy = promBusy || ooBusy || releasing
  const choice: Backend | null = picked ?? (loadError ? 'prometheus' : null)

  useEffect(() => {
    const controller = new AbortController()
    getOpenObserveState(controller.signal)
      .then((next) => {
        if (controller.signal.aborted) return
        setData(next)
        setLoadError('')
        setChoice((current) => current ?? (next.cluster.match ? 'openobserve' : 'prometheus'))
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return
        setLoadError(error instanceof Error ? error.message : String(error))
      })
    return () => controller.abort()
  }, [retry])

  useEffect(() => onDirtyChange?.(promDirty || ooDirty), [promDirty, ooDirty, onDirtyChange])
  useEffect(() => onBusyChange?.(busy), [busy, onBusyChange])

  // The Prometheus form restores focus in the same commit it reports idle, so
  // the dialog must hear it then: a parent still marked busy keeps the pane inert.
  const reported = useRef({ promDirty: false, ooDirty: false, promBusy: false, ooBusy: false, releasing: false })
  reported.current = { promDirty, ooDirty, promBusy, ooBusy, releasing }
  const promDirtyChange = useCallback((dirty: boolean) => {
    setPromDirty(dirty)
    onDirtyChange?.(dirty || reported.current.ooDirty)
  }, [onDirtyChange])
  const promBusyChange = useCallback((next: boolean) => {
    setPromBusy(next)
    onBusyChange?.(next || reported.current.ooBusy || reported.current.releasing)
  }, [onBusyChange])

  const releaseCluster = useCallback(async () => {
    const next = await removeOpenObserveCluster(data?.cluster.uid)
    setData(next)
    setChoice('prometheus')
    onChanged()
  }, [data?.cluster.uid, onChanged])

  const releaseFromPrometheusView = async () => {
    setReleasing(true)
    setReleaseError('')
    try {
      await releaseCluster()
    } catch (error) {
      setReleaseError(error instanceof Error ? error.message : String(error))
    } finally {
      setReleasing(false)
    }
  }

  const statusRow = status ? <div className="flex flex-wrap items-center gap-x-3 gap-y-1">{status}</div> : null
  const matched = !!data?.cluster.match

  return (
    <div className="space-y-4">
      <div role="radiogroup" aria-label="Metrics backend" className="grid gap-2 sm:grid-cols-2">
        {OPTIONS.map((option) => {
          const selected = choice === option.value
          return (
            <button
              key={option.value}
              type="button"
              role="radio"
              aria-checked={selected}
              disabled={locked || busy || !choice}
              onClick={() => setChoice(option.value)}
              className={clsx(
                'rounded-lg border px-3 py-2 text-left transition-colors',
                selected ? 'border-accent bg-accent-muted' : 'border-theme-border',
                !selected && !locked && !busy && 'hover:bg-theme-hover',
                (locked || busy) && 'cursor-not-allowed'
              )}
            >
              <span className="block text-sm font-medium text-theme-text-primary">{option.title}</span>
              <span className="mt-0.5 block text-xs text-theme-text-tertiary">{option.description}</span>
            </button>
          )
        })}
      </div>
      {!choice && <p role="status" className="text-xs text-theme-text-tertiary">Checking the metrics backend…</p>}
      <div className={clsx('space-y-4', choice !== 'prometheus' && 'hidden')} inert={choice !== 'prometheus' || undefined}>
        {matched && !locked && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
            <span className="text-warning-text">This cluster still reads from OpenObserve.</span>
            <button type="button" disabled={busy} onClick={() => void releaseFromPrometheusView()} className="font-medium text-accent-text hover:underline">
              {releasing ? 'Switching…' : 'Use Prometheus for this cluster'}
            </button>
            {releaseError && <span role="alert" className="basis-full text-semantic-error">{releaseError}</span>}
          </div>
        )}
        {locked
          ? choice === 'prometheus' && statusRow
          : prometheus({ status: choice === 'prometheus' ? status : undefined, onDirtyChange: promDirtyChange, onBusyChange: promBusyChange })}
      </div>
      <div className={clsx(choice !== 'openobserve' && 'hidden')} inert={choice !== 'openobserve' || undefined}>
        {data ? (
          <OpenObserveSetup
            state={data}
            locked={locked}
            status={choice === 'openobserve' ? status : undefined}
            onStateChange={setData}
            onChanged={onChanged}
            onUsePrometheus={releaseCluster}
            onDirtyChange={setOoDirty}
            onBusyChange={setOoBusy}
          />
        ) : loadError ? (
          <p role="alert" className="text-xs text-warning-text">
            Couldn’t load OpenObserve settings: {loadError}{' '}
            <button type="button" className="underline" onClick={() => setRetry((n) => n + 1)}>Retry</button>
          </p>
        ) : null}
      </div>
    </div>
  )
}
