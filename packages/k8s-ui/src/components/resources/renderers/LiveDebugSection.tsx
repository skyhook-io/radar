import { useState } from 'react'
import { Activity, LoaderCircle, Play, Square } from 'lucide-react'
import { Section, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'

export type LiveDebugAspect = 'processes' | 'connections' | 'dns'
export type LiveDebugState = 'starting' | 'running' | 'stopping' | 'complete' | 'stopped' | 'failed' | 'partial'

export interface LiveDebugEndpoint {
  address: string
  port?: number
  protocol?: string
  resource?: { kind?: string; namespace?: string; name?: string }
}

export interface LiveDebugResult {
  aspect: LiveDebugAspect
  capturedAt: string
  captureDurationMs: number
  eventCount: number
  truncated: boolean
  eventLoss: string
  unmatchedQueries?: number
  processes?: Array<{ command: string; pid: number; parentPid?: number; parentCommand?: string; user?: string; uid?: number }>
  connections?: Array<{ protocol: string; state: string; local: LiveDebugEndpoint; remote: LiveDebugEndpoint }>
  dns?: Array<{ name: string; type: string; responseCode: string; count: number; addresses?: string[]; averageLatencyMs?: number; maxLatencyMs?: number; service?: string }>
}

export interface LiveDebugRun {
  id: string
  aspect: LiveDebugAspect
  state: LiveDebugState
  result?: LiveDebugResult
  error?: string
  reason?: string
}

interface LiveDebugSectionProps {
  status: 'installed' | 'unknown'
  statusMessage?: string
  containers: string[]
  selectedContainer: string
  onContainerChange: (container: string) => void
  run?: LiveDebugRun
  error?: string
  onCapture: (aspect: LiveDebugAspect, durationSeconds: number) => void
  onStop: () => void
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

const aspects: Array<{ value: LiveDebugAspect; label: string; description: string }> = [
  { value: 'processes', label: 'Processes', description: 'What is running now' },
  { value: 'connections', label: 'Connections', description: 'Current sockets and peers' },
  { value: 'dns', label: 'DNS', description: 'Answers, failures, and latency' },
]

export function LiveDebugSection({
  status,
  statusMessage,
  containers,
  selectedContainer,
  onContainerChange,
  run,
  error,
  onCapture,
  onStop,
  onNavigate,
}: LiveDebugSectionProps) {
  const [aspect, setAspect] = useState<LiveDebugAspect>('processes')
  const [duration, setDuration] = useState(10)
  const active = run && !['complete', 'stopped', 'failed', 'partial'].includes(run.state)
  const canStop = active && run.aspect === 'dns' && run.state !== 'stopping'
  const preparing = run?.state === 'starting'

  return (
    <Section title="Live Debug" icon={Activity} defaultExpanded>
      {status === 'unknown' ? (
        <div className="card-inner-lg text-xs text-theme-text-secondary">
          <div className="font-medium text-theme-text-primary">Inspektor Gadget could not be verified</div>
          <div className="mt-1">{statusMessage || 'Radar does not have permission to search for the Gadget DaemonSet.'}</div>
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="inline-flex rounded-lg border border-theme-border bg-theme-base p-0.5">
              {aspects.map(item => (
                <button
                  key={item.value}
                  type="button"
                  onClick={() => setAspect(item.value)}
                  disabled={!!active}
                  className={`rounded-md px-2.5 py-1.5 text-xs transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${aspect === item.value ? 'btn-brand-toggle' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'}`}
                  title={item.description}
                >
                  {item.label}
                </button>
              ))}
            </div>
            <div className="flex items-center gap-2">
              {containers.length > 1 && (
                <select
                  value={selectedContainer}
                  onChange={event => onContainerChange(event.target.value)}
                  disabled={!!active}
                  className="rounded-md border border-theme-border bg-theme-base px-2 py-1.5 text-xs text-theme-text-primary"
                  aria-label="Container"
                >
                  {containers.map(container => <option key={container} value={container}>{container}</option>)}
                </select>
              )}
              {aspect === 'dns' && (
                <select
                  value={duration}
                  onChange={event => setDuration(Number(event.target.value))}
                  disabled={!!active}
                  className="rounded-md border border-theme-border bg-theme-base px-2 py-1.5 text-xs text-theme-text-primary"
                  aria-label="Capture duration"
                >
                  <option value={10}>10s</option>
                  <option value={30}>30s</option>
                  <option value={60}>60s</option>
                </select>
              )}
              {canStop ? (
                <button type="button" onClick={onStop} className="btn-brand-muted inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs">
                  <Square className="h-3 w-3" /> Stop
                </button>
              ) : (
                <button
                  type="button"
                  onClick={() => onCapture(aspect, aspect === 'dns' ? duration : 0)}
                  disabled={!!active || !selectedContainer}
                  className="btn-brand inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs disabled:opacity-50"
                >
                  <Play className="h-3 w-3" /> {run?.result && run.aspect === aspect ? 'Run again' : 'Capture'}
                </button>
              )}
            </div>
          </div>

          <div className="text-xs text-theme-text-tertiary">
            {aspects.find(item => item.value === aspect)?.description}. Capture explicitly loads short-lived kernel instrumentation on this Pod&apos;s node, then unloads it.
          </div>

          {(preparing || run?.state === 'running' || run?.state === 'stopping') && (
            <div className="card-inner flex items-center gap-2 text-xs text-theme-text-secondary">
              <LoaderCircle className="h-3.5 w-3.5 animate-spin" />
              {preparing ? 'Preparing the pinned gadget…' : run.state === 'running' ? 'Capturing kernel events…' : 'Capture ready; unloading instrumentation…'}
            </div>
          )}
          {(error || run?.error || run?.reason) && (
            <AlertBanner variant="error" title="Live Debug capture failed" message={error || run?.error || run?.reason} />
          )}
          {run?.result && run.aspect === aspect && <ResultView result={run.result} onNavigate={onNavigate} />}
          {!run && <div className="card-inner py-5 text-center text-xs text-theme-text-tertiary">Choose an answer and capture when you need live runtime evidence.</div>}
        </div>
      )}
    </Section>
  )
}

function ResultView({ result, onNavigate }: { result: LiveDebugResult; onNavigate?: LiveDebugSectionProps['onNavigate'] }) {
  const captured = new Date(result.capturedAt).toLocaleTimeString()
  const noun = result.aspect === 'processes' ? 'process' : result.aspect === 'connections' ? 'socket' : 'event'
  return (
    <div className="card-inner-lg space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-theme-text-tertiary">
        <span>{result.eventCount} {noun}{result.eventCount === 1 ? '' : 's'}</span>
        <span>Captured at {captured}{result.captureDurationMs > 0 ? ` · ${(result.captureDurationMs / 1000).toFixed(1)}s` : ''}</span>
      </div>
      {result.aspect === 'processes' && (
        <div className="max-h-72 overflow-auto table-divide-subtle">
          {(result.processes || []).map(process => (
            <div key={`${process.pid}-${process.command}`} className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-1.5 text-xs">
              <div className="min-w-0"><span className="font-mono text-theme-text-primary">{process.command}</span>{process.user && <span className="ml-2 text-theme-text-tertiary">{process.user}</span>}</div>
              <div className="font-mono text-theme-text-secondary">PID {process.pid}{process.parentPid ? ` · PPID ${process.parentPid}` : ''}</div>
            </div>
          ))}
          {result.processes?.length === 0 && <EmptyResult text="No processes were observed for the pinned container." />}
        </div>
      )}
      {result.aspect === 'connections' && (
        <div className="max-h-72 overflow-auto table-divide-subtle">
          {(result.connections || []).map((connection, index) => (
            <div key={`${connection.local.address}-${connection.local.port}-${connection.remote.address}-${connection.remote.port}-${index}`} className="grid grid-cols-[auto_minmax(0,1fr)] gap-3 py-1.5 text-xs">
              <span className="badge-sm status-neutral">{connection.state}</span>
              <div className="min-w-0 text-theme-text-secondary">
                <EndpointView endpoint={connection.local} onNavigate={onNavigate} />
                <span className="mx-1.5 text-theme-text-tertiary">→</span>
                <EndpointView endpoint={connection.remote} onNavigate={onNavigate} />
              </div>
            </div>
          ))}
          {result.connections?.length === 0 && <EmptyResult text="No current sockets were observed for the pinned container." />}
        </div>
      )}
      {result.aspect === 'dns' && (
        <div className="max-h-72 overflow-auto table-divide-subtle">
          {(result.dns || []).map(group => (
            <div key={`${group.name}-${group.type}-${group.responseCode}`} className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-1.5 text-xs">
              <div className="min-w-0">
                <div className="truncate font-mono text-theme-text-primary" title={group.name}>{group.name}</div>
                <div className="flex min-w-0 items-center gap-1 text-theme-text-tertiary">
                  <span>{group.type} ·</span>
                  {group.service ? <DNSServiceLink service={group.service} onNavigate={onNavigate} /> : <span className="truncate">{group.addresses?.join(', ') || 'no addresses'}</span>}
                </div>
              </div>
              <div className="text-right">
                <Badge severity={group.responseCode === 'Success' ? 'success' : 'error'} size="sm">{group.responseCode || 'Unknown'} × {group.count}</Badge>
                <div className="text-theme-text-tertiary">avg {formatLatency(group.averageLatencyMs)}</div>
              </div>
            </div>
          ))}
          {(result.unmatchedQueries || 0) > 0 && (
            <div className="flex items-center gap-2 py-2 text-xs text-theme-text-secondary">
              <Badge severity="warning" size="sm">{result.unmatchedQueries} unmatched</Badge>
              <span>No response was observed for these queries in this capture window.</span>
            </div>
          )}
          {result.dns?.length === 0 && <EmptyResult text="No DNS responses were observed in this capture window." />}
        </div>
      )}
      {(result.truncated || result.eventLoss) && (
        <div className="text-[11px] text-theme-text-tertiary">
          {result.truncated ? 'Radar capped the captured rows. ' : ''}
          {result.eventLoss === 'unmeasurable' ? 'This Gadget does not expose event-loss telemetry.' : `IG-side event loss: ${result.eventLoss}.`}
        </div>
      )}
    </div>
  )
}

function DNSServiceLink({ service, onNavigate }: { service: string; onNavigate?: LiveDebugSectionProps['onNavigate'] }) {
  const separator = service.indexOf('/')
  if (separator < 1 || separator === service.length - 1) return <span className="truncate">service {service}</span>
  const namespace = service.slice(0, separator)
  const name = service.slice(separator + 1)
  return <ResourceLink name={name} kind="Service" namespace={namespace} label={<span>service {service}</span>} onNavigate={onNavigate} />
}

function EndpointView({ endpoint, onNavigate }: { endpoint: LiveDebugEndpoint; onNavigate?: LiveDebugSectionProps['onNavigate'] }) {
  const address = `${endpoint.address}${endpoint.port ? `:${endpoint.port}` : ''}`
  if (!endpoint.resource?.name || !endpoint.resource.kind) return <span className="font-mono">{address}</span>
  const kind = endpoint.resource.kind === 'svc' ? 'Service' : endpoint.resource.kind === 'pod' ? 'Pod' : endpoint.resource.kind
  return (
    <ResourceLink
      name={endpoint.resource.name}
      kind={kind}
      namespace={endpoint.resource.namespace || ''}
      label={<span>{kind.toLowerCase()} {endpoint.resource.namespace}/{endpoint.resource.name}</span>}
      onNavigate={onNavigate}
    />
  )
}

function EmptyResult({ text }: { text: string }) {
  return <div className="py-5 text-center text-xs text-theme-text-tertiary">{text}</div>
}

function formatLatency(value?: number) {
  if (value === undefined) return 'n/a'
  if (value < 1) return `${Math.round(value * 1000)}µs`
  return `${value.toFixed(value < 10 ? 1 : 0)}ms`
}
