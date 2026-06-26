import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Activity, Loader2, X, ChevronDown, Maximize2 } from 'lucide-react'
import { clsx } from 'clsx'
import { apiFetch } from '../../api/client'
import { apiUrl } from '../../api/config'
import { Tooltip } from '../ui/Tooltip'

// A port is "probe-able" only if it plausibly speaks HTTP — probing a raw TCP
// port (Postgres, Redis) with a GET returns noise, so we don't offer it there
// (that's the local-client TCP path's job). Heuristic over name/appProtocol/number.
const HTTP_PORT_NUMBERS = new Set([80, 443, 8080, 8443, 8000, 8081, 3000, 5000, 9090, 9091, 9093, 9100, 15000, 15090])
const HTTP_NAME_RE = /(^|[-_])(http|https|web|ui|console|dashboard|metrics|api|admin)([-_]|$)/i

export function isHttpishPort(port: number, name?: string, appProtocol?: string, protocol?: string): boolean {
  // HTTP rides TCP — a UDP port is never a GET target (e.g. statsd "metrics-udp").
  if ((protocol || '').toUpperCase() === 'UDP') return false
  const proto = (appProtocol || '').toLowerCase()
  if (proto === 'http' || proto === 'https' || proto === 'http2') return true
  if (proto && proto !== 'tcp') {
    // explicit non-HTTP appProtocol (grpc, redis, postgres, …) → not a GET target
    return false
  }
  if (name && HTTP_NAME_RE.test(name)) return true
  return HTTP_PORT_NUMBERS.has(port)
}

export function defaultScheme(port: number, name?: string, appProtocol?: string): 'http' | 'https' {
  if ((appProtocol || '').toLowerCase() === 'https') return 'https'
  if (port === 443 || port === 8443) return 'https'
  if (name && /https/i.test(name)) return 'https'
  return 'http'
}

interface ProbeResult {
  status: number
  durationMs: number
  headers: Record<string, string>
  body: string
  truncated: boolean
  bodyBytes: number
  error?: string
}

function statusTone(status: number): string {
  if (status >= 200 && status < 300) return 'text-emerald-400'
  if (status >= 300 && status < 400) return 'text-blue-400'
  if (status >= 400 && status < 500) return 'text-amber-400'
  return 'text-red-400'
}

// Small toggle button rendered in a port row's action slot. The panel itself
// renders inline within the port card (see ProbePanel), not as an overlay.
export function ProbeButton({ active, onClick }: { active: boolean; onClick: () => void }) {
  return (
    <Tooltip content="Probe this endpoint — GET from inside the cluster">
      <button
        onClick={(e) => { e.stopPropagation(); onClick() }}
        aria-expanded={active}
        className={clsx(
          'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs transition-colors',
          active ? 'bg-accent-muted text-blue-400' : 'bg-theme-elevated hover:bg-accent-muted',
        )}
      >
        Probe
        <Activity className="w-3 h-3" />
      </button>
    </Tooltip>
  )
}

function VerdictLine({
  result,
  showHeaders,
  onToggleHeaders,
}: {
  result: ProbeResult
  showHeaders: boolean
  onToggleHeaders: () => void
}) {
  return (
    <div className="flex items-center gap-3 text-xs">
      <span className={clsx('font-mono font-semibold', statusTone(result.status))}>{result.status}</span>
      <span className="text-theme-text-tertiary">{result.durationMs} ms</span>
      <span className="text-theme-text-tertiary">{result.bodyBytes.toLocaleString()} bytes{result.truncated ? ' (truncated)' : ''}</span>
      <button
        type="button"
        onClick={onToggleHeaders}
        className="ml-auto flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary"
      >
        Headers <ChevronDown className={clsx('w-3 h-3 transition-transform', showHeaders && 'rotate-180')} />
      </button>
    </div>
  )
}

// Roomy response viewer. The narrow drawer can't show a 27 KB /metrics body
// readably (wide lines wrap into mush), so the full body opens here: wider than
// the drawer, monospace, no-wrap with horizontal scroll. Triggered on demand —
// the request + verdict stay inline in the port card.
function ProbeResponseSheet({
  serviceName,
  port,
  scheme,
  path,
  result,
  onClose,
}: {
  serviceName: string
  port: number
  scheme: string
  path: string
  result: ProbeResult
  onClose: () => void
}) {
  const [showHeaders, setShowHeaders] = useState(false)
  return (
    <div className="fixed inset-0 z-50" onClick={(e) => e.stopPropagation()}>
      <div className="absolute inset-0 bg-black/40" onClick={onClose} />
      <div className="absolute right-0 top-0 h-full w-[min(820px,92vw)] bg-theme-surface border-l border-theme-border shadow-2xl flex flex-col">
        <div className="flex items-center justify-between gap-3 p-4 border-b border-theme-border">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <Activity className="w-4 h-4 text-blue-400 shrink-0" />
              <h3 className="text-sm font-semibold text-theme-text-primary truncate">Probe response</h3>
            </div>
            <div className="text-xs text-theme-text-tertiary font-mono mt-0.5 truncate">
              GET {scheme}://{serviceName}:{port}{path}
            </div>
          </div>
          <button onClick={onClose} aria-label="Close" className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0">
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="px-4 py-2 border-b border-theme-border">
          <VerdictLine result={result} showHeaders={showHeaders} onToggleHeaders={() => setShowHeaders((v) => !v)} />
        </div>

        {showHeaders && (
          <pre className="text-xs bg-theme-base m-4 mb-0 rounded p-3 overflow-auto max-h-48 text-theme-text-secondary font-mono whitespace-pre">
            {Object.entries(result.headers).map(([k, v]) => `${k}: ${v}`).join('\n') || '(no headers)'}
          </pre>
        )}

        {result.error ? (
          <div className="m-4 text-sm text-amber-400 bg-amber-500/10 border border-amber-500/30 rounded px-3 py-2">
            {result.error}
          </div>
        ) : (
          <pre className="flex-1 text-xs bg-theme-base m-4 rounded p-3 overflow-auto text-theme-text-primary font-mono whitespace-pre">
            {result.body || '(empty response body)'}
          </pre>
        )}
      </div>
    </div>
  )
}

// Inline probe: request form + verdict + a short body peek, rendered in the
// drawer flow (inside the port card). The full body opens in ProbeResponseSheet
// so a large response never bloats the drawer.
export function ProbePanel({
  namespace,
  serviceName,
  port,
  initialScheme,
  onClose,
}: {
  namespace: string
  serviceName: string
  port: number
  initialScheme: 'http' | 'https'
  onClose: () => void
}) {
  const [scheme, setScheme] = useState<'http' | 'https'>(initialScheme)
  const [path, setPath] = useState('/')
  const [showHeaders, setShowHeaders] = useState(false)
  const [sheetOpen, setSheetOpen] = useState(false)
  // The path that was actually sent — so the sheet header reflects the response, not edits-in-progress.
  const [sentPath, setSentPath] = useState('/')
  const [sentScheme, setSentScheme] = useState<'http' | 'https'>(initialScheme)

  const probe = useMutation<ProbeResult>({
    mutationFn: async () => {
      setSentPath(path)
      setSentScheme(scheme)
      const res = await apiFetch(apiUrl('/probe/service'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ namespace, name: serviceName, port: String(port), scheme, path }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data?.error || `Request failed (${res.status})`)
      return data as ProbeResult
    },
  })

  const result = probe.data

  return (
    <div className="mt-3 pt-3 border-t border-theme-border space-y-2" onClick={(e) => e.stopPropagation()}>
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-1.5 text-xs font-medium text-theme-text-secondary">
          <Activity className="w-3.5 h-3.5 text-blue-400" />
          Probe — GET from inside the cluster
        </span>
        <button
          onClick={onClose}
          aria-label="Close probe"
          className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>

      <form className="flex items-stretch gap-2" onSubmit={(e) => { e.preventDefault(); probe.mutate() }}>
        <select
          value={scheme}
          onChange={(e) => setScheme(e.target.value as 'http' | 'https')}
          className="bg-theme-base border border-theme-border rounded px-2 py-1 text-xs text-theme-text-primary font-mono"
          aria-label="Scheme"
        >
          <option value="http">http</option>
          <option value="https">https</option>
        </select>
        <input
          type="text"
          value={path}
          onChange={(e) => setPath(e.target.value)}
          placeholder="/healthz"
          aria-label="Request path"
          className="flex-1 min-w-0 bg-theme-base border border-theme-border rounded px-2 py-1 text-xs text-theme-text-primary font-mono"
        />
        <button
          type="submit"
          disabled={probe.isPending}
          className="shrink-0 px-3 py-1 btn-brand text-xs rounded-lg flex items-center gap-1.5 disabled:opacity-50"
        >
          {probe.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Activity className="w-3.5 h-3.5" />}
          Send
        </button>
      </form>

      {probe.isError && (
        <div className="text-xs text-red-400 bg-red-500/10 border border-red-500/30 rounded px-2 py-1.5">
          {(probe.error as Error).message}
        </div>
      )}

      {result && (
        <div className="space-y-2">
          <VerdictLine result={result} showHeaders={showHeaders} onToggleHeaders={() => setShowHeaders((v) => !v)} />

          {result.error && (
            <div className="text-xs text-amber-400 bg-amber-500/10 border border-amber-500/30 rounded px-2 py-1.5">
              {result.error}
            </div>
          )}

          {showHeaders && (
            <pre className="text-xs bg-theme-base rounded p-2 overflow-auto max-h-32 text-theme-text-secondary font-mono whitespace-pre">
              {Object.entries(result.headers).map(([k, v]) => `${k}: ${v}`).join('\n') || '(no headers)'}
            </pre>
          )}

          {!result.error && (
            <>
              {/* Short peek — bounded so a big body never takes over the drawer. */}
              <pre className="text-xs bg-theme-base rounded p-2 overflow-hidden max-h-24 text-theme-text-primary font-mono whitespace-pre-wrap break-words">
                {result.body || '(empty response body)'}
              </pre>
              {result.body && (
                <button
                  type="button"
                  onClick={() => setSheetOpen(true)}
                  className="flex items-center gap-1.5 text-xs text-blue-400 hover:text-blue-300"
                >
                  <Maximize2 className="w-3 h-3" />
                  View full response
                </button>
              )}
            </>
          )}
        </div>
      )}

      {sheetOpen && result && (
        <ProbeResponseSheet
          serviceName={serviceName}
          port={port}
          scheme={sentScheme}
          path={sentPath}
          result={result}
          onClose={() => setSheetOpen(false)}
        />
      )}
    </div>
  )
}
