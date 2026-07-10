import { useState, useEffect, useLayoutEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useMutation } from '@tanstack/react-query'
import { Activity, Loader2, X, ChevronDown, Maximize2, Copy, Check } from 'lucide-react'
import { clsx } from 'clsx'
import { apiFetch } from '../../api/client'
import { apiUrl } from '../../api/config'
import { Tooltip } from '../ui/Tooltip'
import { Input } from '@skyhook-io/k8s-ui'

// A port is "curl-able" only if it plausibly speaks HTTP — probing a raw TCP
// port (Postgres, Redis) with a GET returns noise, so we don't offer it there
// (that's the local-client TCP path's job). Heuristic over name/appProtocol/number.
const HTTP_PORT_NUMBERS = new Set([80, 443, 8080, 8443, 8000, 8081, 3000, 5000, 9090, 9091, 9093, 9100, 15000, 15090])
const HTTP_NAME_RE = /(^|[-_])(http|https|web|ui|console|dashboard|metrics|api|admin)([-_]|$)/i

// Common metrics port numbers — used to decide which quick-path chips make sense.
const METRICS_PORT_NUMBERS = new Set([9090, 9091, 9093, 9100, 9153, 2112, 8888])

function isMetricsPort(port: number, name?: string, appProtocol?: string): boolean {
  if ((appProtocol || '').toLowerCase().includes('metric')) return true
  if (name && /metric/i.test(name)) return true
  return METRICS_PORT_NUMBERS.has(port)
}

// Default request path for a port. Only metrics ports get a non-root default —
// /metrics is a near-deterministic convention there. We deliberately don't
// pre-fill or suggest health paths (/healthz etc.): those are genuine guesses
// (apps vary: /health, /actuator/health, /-/healthy …) and a suggestion that
// 404s reads as the tool being wrong. The honest one-click-health feature is to
// derive paths from the backing pod's liveness/readiness probes — a follow-up.
export function defaultPathForPort(port: number, name?: string, appProtocol?: string): string {
  return isMetricsPort(port, name, appProtocol) ? '/metrics' : '/'
}

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

interface CurlResult {
  status: number
  statusText: string
  durationMs: number
  headers: Record<string, string>
  body: string
  truncated: boolean
  bodyBytes: number
  error?: string
}

function statusTextTone(status: number): string {
  if (status >= 200 && status < 300) return 'text-emerald-400'
  if (status >= 300 && status < 400) return 'text-blue-400'
  if (status >= 400 && status < 500) return 'text-amber-400'
  return 'text-red-400'
}

function statusDotTone(status: number): string {
  if (status >= 200 && status < 300) return 'bg-emerald-400'
  if (status >= 300 && status < 400) return 'bg-blue-400'
  if (status >= 400 && status < 500) return 'bg-amber-400'
  return 'bg-red-400'
}

// Make the body readable per content type: pretty-print JSON, label everything
// else (HTML / Prometheus / XML / …) so the operator knows what they're looking at.
function formatBody(result: CurlResult): { text: string; label: string } {
  const body = result.body
  const ct = (result.headers['Content-Type'] || result.headers['content-type'] || '').toLowerCase()
  const looksJson = ct.includes('json') || /^\s*[[{]/.test(body)
  if (looksJson) {
    let text = body
    if (!result.truncated) {
      try { text = JSON.stringify(JSON.parse(body), null, 2) } catch { /* leave raw */ }
    }
    return { text, label: 'JSON' }
  }
  if (ct.includes('html')) return { text: body, label: 'HTML' }
  if (body.startsWith('# HELP') || body.startsWith('# TYPE') || ct.includes('openmetrics')) {
    return { text: body, label: 'Prometheus' }
  }
  if (ct.includes('xml')) return { text: body, label: 'XML' }
  const short = ct ? (ct.split(';')[0].split('/').pop() || 'text') : 'text'
  return { text: body, label: short }
}

function CopyButton({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        navigator.clipboard?.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        }).catch(() => {})
      }}
      className={clsx('inline-flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary', className)}
    >
      {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}

// Small toggle button rendered in a port row's action slot. The panel itself
// renders inline within the port card (see CurlPanel), not as an overlay.
export function CurlButton({ active, onClick }: { active: boolean; onClick: () => void }) {
  return (
    <Tooltip content="Curl this endpoint — GET from inside the cluster">
      <button
        onClick={(e) => { e.stopPropagation(); onClick() }}
        aria-expanded={active}
        className={clsx(
          'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs transition-colors',
          active ? 'bg-accent-muted text-blue-400' : 'bg-theme-elevated hover:bg-accent-muted',
        )}
      >
        Curl
        {/* Disclosure caret: signals this expands an inline panel rather than firing a request. */}
        <ChevronDown className={clsx('w-3 h-3 transition-transform', active && 'rotate-180')} />
      </button>
    </Tooltip>
  )
}

function VerdictLine({
  result,
  showHeaders,
  onToggleHeaders,
}: {
  result: CurlResult
  showHeaders: boolean
  onToggleHeaders: () => void
}) {
  return (
    <div className="flex items-center gap-3 text-xs">
      <span className={clsx('flex items-center gap-1.5 font-mono font-semibold', statusTextTone(result.status))}>
        <span className={clsx('w-1.5 h-1.5 rounded-full', statusDotTone(result.status))} />
        {result.status}{result.statusText ? ` ${result.statusText}` : ''}
      </span>
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
// readably (wide lines wrap into mush), so the full body opens in a centered
// dialog — wide, tall, monospace, no-wrap with its own scroll (decoupled from the
// drawer, so there's no nested-scroll). Triggered on demand; the request + verdict
// stay inline in the port card. Matches the kubectl copy-command dialog pattern.
function CurlResponseDialog({
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
  result: CurlResult
  onClose: () => void
}) {
  const [showHeaders, setShowHeaders] = useState(false)
  const { text, label } = formatBody(result)
  useEffect(() => {
    // Capture + stopPropagation so Escape closes only this dialog, not the drawer
    // behind it (its Escape shortcut listens in the bubble phase).
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [onClose])
  // Portal to <body>: the drawer is a transformed ancestor, which would otherwise
  // trap this position:fixed dialog inside the drawer instead of centering it on
  // the viewport.
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={(e) => e.stopPropagation()}>
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="relative dialog w-full max-w-4xl mx-4 max-h-[85vh] flex flex-col outline-none">
        <div className="flex items-center justify-between gap-3 p-4 border-b border-theme-border">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <Activity className="w-4 h-4 text-blue-400 shrink-0" />
              <h3 className="text-sm font-semibold text-theme-text-primary truncate">Response</h3>
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

        <div className="grid transition-[grid-template-rows] duration-200 ease-out mx-4" style={{ gridTemplateRows: showHeaders ? '1fr' : '0fr' }}>
          <div className="overflow-hidden">
            <pre className="text-xs bg-theme-base mt-4 rounded p-3 overflow-auto max-h-48 text-theme-text-secondary font-mono whitespace-pre">
              {Object.entries(result.headers).map(([k, v]) => `${k}: ${v}`).join('\n') || '(no headers)'}
            </pre>
          </div>
        </div>

        {result.error ? (
          <div className="m-4 text-sm text-amber-400 bg-amber-500/10 border border-amber-500/30 rounded px-3 py-2">
            {result.error}
          </div>
        ) : (
          <div className="flex flex-col min-h-0 flex-1 m-4">
            <div className="flex items-center justify-between mb-1.5">
              <span className="badge-sm bg-theme-elevated text-theme-text-secondary border border-theme-border">{label}</span>
              {result.body && <CopyButton text={text} />}
            </div>
            <pre className="flex-1 text-xs bg-theme-base rounded p-3 overflow-auto text-theme-text-primary font-mono whitespace-pre">
              {text || '(empty response body)'}
            </pre>
          </div>
        )}
      </div>
    </div>,
    document.body,
  )
}

// Inline curl: request form + verdict + a short body peek, rendered in the
// drawer flow (inside the port card). The full body opens in CurlResponseDialog
// so a large response never bloats the drawer.
export function CurlPanel({
  namespace,
  serviceName,
  port,
  initialScheme,
  initialPath,
  open,
  onClose,
}: {
  namespace: string
  serviceName: string
  port: number
  initialScheme: 'http' | 'https'
  initialPath: string
  // Host-controlled: false triggers the collapse animation before the host unmounts.
  open: boolean
  onClose: () => void
}) {
  const [scheme, setScheme] = useState<'http' | 'https'>(initialScheme)
  // Stored WITHOUT the leading slash — the "/" is a fixed, non-deletable prefix
  // glued to the input. Typed/pasted leading slashes are swallowed on change.
  const [path, setPath] = useState(() => initialPath.replace(/^\/+/, ''))
  const fullPath = '/' + path
  const [showHeaders, setShowHeaders] = useState(false)
  const [sheetOpen, setSheetOpen] = useState(false)
  // What was actually sent — so the sheet header / re-renders reflect the response.
  const [sent, setSent] = useState<{ scheme: 'http' | 'https'; path: string }>({ scheme: initialScheme, path: initialPath })
  // Enter animation: mount collapsed, then expand next tick (radar's grid 0fr↔1fr).
  // Combined with the host-controlled `open` prop this gives a symmetric reveal:
  // expand on mount, collapse when the host sets open=false (before it unmounts).
  const [mounted, setMounted] = useState(false)
  useEffect(() => { setMounted(true) }, [])

  const curl = useMutation<CurlResult, Error, { scheme: 'http' | 'https'; path: string }>({
    mutationFn: async (vars) => {
      setSent(vars)
      const res = await apiFetch(apiUrl('/curl/service'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ namespace, name: serviceName, port: String(port), scheme: vars.scheme, path: vars.path }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data?.error || `Request failed (${res.status})`)
      return data as CurlResult
    },
  })

  const result = curl.data
  const peek = result && !result.error ? formatBody(result) : null

  // Only fade + offer "View full response" when the body actually overflows the
  // peek box — a small body that fits has nothing more to show, and a fade over
  // it reads as a rendering glitch.
  const peekRef = useRef<HTMLPreElement>(null)
  const [peekOverflows, setPeekOverflows] = useState(false)
  useLayoutEffect(() => {
    const el = peekRef.current
    setPeekOverflows(!!el && el.scrollHeight > el.clientHeight + 1)
  }, [peek?.text, open])

  return (
    <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: mounted && open ? '1fr' : '0fr' }}>
      <div className="overflow-hidden">
      <div className="mt-3 pt-3 border-t border-theme-border space-y-2" onClick={(e) => e.stopPropagation()}>
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-1.5 text-xs font-medium text-theme-text-secondary">
          <Activity className="w-3.5 h-3.5 text-blue-400" />
          Curl — GET from inside the cluster
        </span>
        <button
          onClick={onClose}
          aria-label="Close"
          className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>

      <form className="flex items-stretch gap-2" onSubmit={(e) => { e.preventDefault(); curl.mutate({ scheme, path: fullPath }) }}>
        <select
          value={scheme}
          onChange={(e) => setScheme(e.target.value as 'http' | 'https')}
          className="bg-theme-base border border-theme-border rounded px-2 py-1 text-xs text-theme-text-primary font-mono"
          aria-label="Scheme"
        >
          <option value="http">http</option>
          <option value="https">https</option>
        </select>
        <div className="flex-1 min-w-0 flex items-center bg-theme-base border border-theme-border rounded px-2 focus-within:border-blue-500">
          <span className="text-xs text-theme-text-tertiary font-mono select-none pointer-events-none">/</span>
          <Input
            value={path}
            onChange={(e) => setPath(e.target.value.replace(/^\/+/, ''))}
            placeholder="healthz"
            aria-label="Request path"
            className="flex-1 min-w-0 bg-transparent border-0 outline-none pl-0.5 py-1 text-xs text-theme-text-primary font-mono"
          />
        </div>
        <button
          type="submit"
          disabled={curl.isPending}
          className="shrink-0 px-3 py-1 btn-brand text-xs rounded-lg flex items-center gap-1.5 disabled:opacity-50"
        >
          {curl.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Activity className="w-3.5 h-3.5" />}
          Send
        </button>
      </form>

      {curl.isError && (
        <div className="text-xs text-red-400 bg-red-500/10 border border-red-500/30 rounded px-2 py-1.5">
          {(curl.error as Error).message}
        </div>
      )}

      {/* Reveal the response with the same grid transition as the panel itself. */}
      <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: result ? '1fr' : '0fr' }}>
        <div className="overflow-hidden">
      {result && (
        <div className="space-y-2 pt-0.5">
          <VerdictLine result={result} showHeaders={showHeaders} onToggleHeaders={() => setShowHeaders((v) => !v)} />

          {result.error && (
            <div className="text-xs text-amber-400 bg-amber-500/10 border border-amber-500/30 rounded px-2 py-1.5">
              {result.error}
            </div>
          )}

          <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: showHeaders ? '1fr' : '0fr' }}>
            <div className="overflow-hidden">
              <pre className="text-xs bg-theme-base rounded p-2 overflow-auto max-h-32 text-theme-text-secondary font-mono whitespace-pre">
                {Object.entries(result.headers).map(([k, v]) => `${k}: ${v}`).join('\n') || '(no headers)'}
              </pre>
            </div>
          </div>

          {peek && (
            <>
              {result.body && (
                <div className="flex items-center justify-between">
                  <span className="badge-sm bg-theme-elevated text-theme-text-secondary border border-theme-border">{peek.label}</span>
                  <CopyButton text={peek.text} />
                </div>
              )}
              {/* Short peek — a bounded teaser, not a scroll surface (the full body
                  has its own scrollable dialog). When the body overflows, a bottom
                  fade signals "more below"; when it fits, no fade and no "view full"
                  (there's nothing more to see). */}
              <div className="relative">
                <pre ref={peekRef} className="text-xs bg-theme-base rounded p-2 overflow-hidden max-h-24 text-theme-text-primary font-mono whitespace-pre-wrap break-words">
                  {peek.text || '(empty response body)'}
                </pre>
                {result.body && peekOverflows && (
                  <div className="pointer-events-none absolute inset-x-0 bottom-0 h-8 rounded-b bg-gradient-to-t from-theme-base to-transparent" />
                )}
              </div>
              {result.body && peekOverflows && (
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
        </div>
      </div>

      {sheetOpen && result && (
        <CurlResponseDialog
          serviceName={serviceName}
          port={port}
          scheme={sent.scheme}
          path={sent.path}
          result={result}
          onClose={() => setSheetOpen(false)}
        />
      )}
      </div>
      </div>
    </div>
  )
}
