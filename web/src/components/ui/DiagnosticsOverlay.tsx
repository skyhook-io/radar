import { useEffect, useState, useCallback } from 'react'
import { X, Copy, Check } from 'lucide-react'
import { clsx } from 'clsx'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { useDiagnostics } from '../../api/client'
import type { DiagnosticsSnapshot, DiagMetricsSourceHealth, DiagDropRecord } from '../../api/client'

interface DiagnosticsOverlayProps {
  onClose: () => void
  isOpen?: boolean
}

export function DiagnosticsOverlay({ onClose, isOpen = true }: DiagnosticsOverlayProps) {
  const { data, isLoading, error } = useDiagnostics(true)
  const [copied, setCopied] = useState<'json' | 'formatted' | null>(null)

  // Close on Escape (capture phase)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', handler, true)
    return () => document.removeEventListener('keydown', handler, true)
  }, [onClose])

  const copyToClipboard = useCallback(async (type: 'json' | 'formatted') => {
    if (!data) return
    const text = type === 'json'
      ? JSON.stringify(data, null, 2)
      : formatForGitHub(data)
    await navigator.clipboard.writeText(text)
    setCopied(type)
    setTimeout(() => setCopied(null), 2000)
  }, [data])

  return (
    <div className="fixed inset-0 z-[100] flex items-start justify-center pt-[8vh]">
      {/* Backdrop */}
      <div
        className={clsx(
          'absolute inset-0 bg-theme-base/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0'
        )}
        onClick={onClose}
      />

      {/* Panel */}
      <div className={clsx(
        'relative w-full max-w-2xl mx-4 bg-theme-surface border border-theme-border rounded-xl shadow-2xl overflow-hidden flex flex-col max-h-[84vh]',
        TRANSITION_PANEL,
        isOpen ? 'opacity-100 scale-100 translate-y-0' : 'opacity-0 scale-[0.97] translate-y-3'
      )}>
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-3">
            <h2 className="text-sm font-semibold text-theme-text-primary">Diagnostics</h2>
            {data && (
              <span className="text-xs text-theme-text-tertiary">
                v{data.radarVersion} &middot; up {data.uptime}
              </span>
            )}
          </div>
          <button onClick={onClose} className="p-1 rounded-md text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated/50">
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Content */}
        <div className="overflow-y-auto flex-1 px-5 py-4 space-y-4">
          {isLoading && (
            <div className="text-sm text-theme-text-tertiary text-center py-8">Loading diagnostics...</div>
          )}
          {error && (
            <div className="text-sm text-red-400 text-center py-8">Failed to load diagnostics: {(error as Error).message}</div>
          )}
          {data && (
            <>
              <ConnectionSection data={data} />
              <ClusterSection data={data} />
              <CacheSection data={data} />
              <MetricsSection data={data} />
              <EventPipelineSection data={data} />
              <InformersSection data={data} />
              <PrometheusSection data={data} />
              <TrafficSection data={data} />
              <RuntimeSection data={data} />
              <ConfigSection data={data} />
              {data.errors && data.errors.length > 0 && (
                <Section title="Collection Errors" warn>
                  {data.errors.map((e, i) => <Row key={i} label={`Error ${i + 1}`} value={e} />)}
                </Section>
              )}
            </>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center gap-2 px-5 py-3 border-t border-theme-border shrink-0">
          <CopyButton label="Copy for GitHub" onClick={() => copyToClipboard('formatted')} copied={copied === 'formatted'} />
          <CopyButton label="Copy Raw JSON" onClick={() => copyToClipboard('json')} copied={copied === 'json'} />
        </div>
      </div>
    </div>
  )
}

// --- Section components ---

function Section({ title, children, warn }: { title: string; children: React.ReactNode; warn?: boolean }) {
  return (
    <div className={clsx(
      'rounded-lg border px-3.5 py-2.5',
      warn ? 'border-yellow-500/30 bg-yellow-500/5' : 'border-theme-border-light bg-theme-elevated/20'
    )}>
      <h3 className="text-[11px] font-semibold text-theme-text-tertiary uppercase tracking-wider mb-1.5">{title}</h3>
      <div className="space-y-0.5">{children}</div>
    </div>
  )
}

function Row({ label, value, warn }: { label: string; value: React.ReactNode; warn?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-4 text-xs">
      <span className="text-theme-text-secondary shrink-0">{label}</span>
      <span className={clsx(
        'text-right truncate',
        warn ? 'text-yellow-400' : 'text-theme-text-primary'
      )}>{value}</span>
    </div>
  )
}

function ConnectionSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.connection) return null
  const c = data.connection
  const warn = c.state !== 'connected'
  return (
    <Section title="Connection" warn={warn}>
      <Row label="State" value={c.state} warn={warn} />
      <Row label="Context" value={c.context} />
      {c.clusterName && <Row label="Cluster" value={c.clusterName} />}
      {c.error && <Row label="Error" value={c.error} warn />}
      {c.errorType && <Row label="Error Type" value={c.errorType} warn />}
    </Section>
  )
}

function ClusterSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.cluster) return null
  const c = data.cluster
  return (
    <Section title="Cluster">
      <Row label="Platform" value={c.platform} />
      <Row label="Kubernetes" value={c.kubernetesVersion} />
      <Row label="Nodes" value={c.nodeCount} />
      <Row label="Namespaces" value={c.namespaceCount} />
      {c.inCluster && <Row label="In-Cluster" value="Yes" />}
    </Section>
  )
}

function CacheSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.cache) return null
  return (
    <Section title="Cache">
      <Row label="Total Resources" value={data.cache.totalResources.toLocaleString()} />
      <Row label="Watched Kinds" value={Object.keys(data.cache.resourceCounts).length} />
    </Section>
  )
}

function MetricsSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.metrics) return null
  const pod = data.metrics.podMetrics
  const node = data.metrics.nodeMetrics
  const warn = pod.consecutiveErrors > 0 || node.consecutiveErrors > 0
  return (
    <Section title="Metrics Collection" warn={warn}>
      <MetricsSourceRow label="Pod Metrics" source={pod} />
      <MetricsSourceRow label="Node Metrics" source={node} />
    </Section>
  )
}

function MetricsSourceRow({ label, source }: { label: string; source: DiagMetricsSourceHealth }) {
  const status = source.collecting ? 'collecting' : source.consecutiveErrors > 0 ? `${source.consecutiveErrors} errors` : 'idle'
  const warn = source.consecutiveErrors > 0
  return (
    <>
      <Row label={label} value={`${status} (${source.trackedCount} tracked)`} warn={warn} />
      {source.lastError && <Row label={`  Last Error`} value={source.lastError} warn />}
    </>
  )
}

function EventPipelineSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.eventPipeline) return null
  const ep = data.eventPipeline
  const totalDropped = Object.values(ep.dropped).reduce((a, b) => a + b, 0)
  const totalReceived = Object.values(ep.received).reduce((a, b) => a + b, 0)
  const warn = totalDropped > 0
  return (
    <Section title="Event Pipeline" warn={warn}>
      <Row label="Total Received" value={totalReceived.toLocaleString()} />
      <Row label="Total Dropped" value={totalDropped.toLocaleString()} warn={warn} />
      <Row label="Uptime" value={ep.uptime} />
      {ep.recentDrops && ep.recentDrops.length > 0 && (
        <div className="mt-1.5 pt-1.5 border-t border-theme-border-light">
          <span className="text-[10px] text-theme-text-tertiary uppercase">Recent Drops ({ep.recentDrops.length})</span>
          {ep.recentDrops.slice(0, 5).map((d: DiagDropRecord, i: number) => (
            <Row key={i} label={`${d.kind}/${d.name}`} value={d.reason} warn />
          ))}
        </div>
      )}
    </Section>
  )
}

function InformersSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.informers) return null
  const inf = data.informers
  return (
    <Section title="Informers">
      <Row label="Typed" value={inf.typedCount} />
      <Row label="Dynamic (CRDs)" value={inf.dynamicCount} />
      {inf.watchedCRDs && inf.watchedCRDs.length > 0 && (
        <Row label="Watched CRDs" value={inf.watchedCRDs.join(', ')} />
      )}
    </Section>
  )
}

function PrometheusSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.prometheus) return null
  const p = data.prometheus
  const warn = !p.connected
  return (
    <Section title="Prometheus" warn={warn}>
      <Row label="Connected" value={p.connected ? 'Yes' : 'No'} warn={warn} />
      {p.address && <Row label="Address" value={p.address} />}
      {p.serviceName && <Row label="Service" value={`${p.serviceNamespace}/${p.serviceName}`} />}
    </Section>
  )
}

function TrafficSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.traffic) return null
  const t = data.traffic
  return (
    <Section title="Traffic">
      <Row label="Active Source" value={t.activeSource || 'none'} />
      {t.detected && t.detected.length > 0 && <Row label="Detected" value={t.detected.join(', ')} />}
      {t.notDetected && t.notDetected.length > 0 && <Row label="Not Detected" value={t.notDetected.join(', ')} />}
    </Section>
  )
}

function RuntimeSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.runtime) return null
  const rt = data.runtime
  return (
    <Section title="Runtime">
      <Row label="Heap" value={`${rt.heapMB.toFixed(1)} MB (${rt.heapObjectsK.toFixed(1)}K objects)`} />
      <Row label="Goroutines" value={rt.goroutines} />
      <Row label="CPUs" value={rt.numCPU} />
      {data.sse && <Row label="SSE Clients" value={data.sse.connectedClients} />}
    </Section>
  )
}

function ConfigSection({ data }: { data: DiagnosticsSnapshot }) {
  if (!data.config) return null
  const cfg = data.config
  return (
    <Section title="Config">
      <Row label="Port" value={cfg.port} />
      <Row label="Dev Mode" value={cfg.devMode ? 'Yes' : 'No'} />
      {cfg.namespace && <Row label="Namespace Filter" value={cfg.namespace} />}
      <Row label="Timeline Storage" value={cfg.timelineStorage} />
      <Row label="History Limit" value={cfg.historyLimit.toLocaleString()} />
      <Row label="MCP Enabled" value={cfg.mcpEnabled ? 'Yes' : 'No'} />
      <Row label="Prometheus URL" value={cfg.hasPrometheusURL ? 'Set' : 'Auto-discover'} />
    </Section>
  )
}

// --- Copy button ---

function CopyButton({ label, onClick, copied }: { label: string; onClick: () => void; copied: boolean }) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        'flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md transition-colors',
        copied
          ? 'bg-green-500/20 text-green-400'
          : 'bg-theme-elevated text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated/80'
      )}
    >
      {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
      {copied ? 'Copied!' : label}
    </button>
  )
}

// --- GitHub-friendly formatting ---

function formatForGitHub(data: DiagnosticsSnapshot): string {
  const lines: string[] = []
  lines.push(`## Radar Diagnostics`)
  lines.push(``)
  lines.push(`**Version:** ${data.radarVersion} | **Go:** ${data.goVersion} | **OS:** ${data.goos}/${data.goarch} | **Uptime:** ${data.uptime}`)
  lines.push(``)

  if (data.connection) {
    const c = data.connection
    lines.push(`### Connection`)
    lines.push(`- State: \`${c.state}\``)
    lines.push(`- Context: \`${c.context}\``)
    if (c.clusterName) lines.push(`- Cluster: \`${c.clusterName}\``)
    if (c.error) lines.push(`- Error: ${c.error}`)
    if (c.errorType) lines.push(`- Error Type: \`${c.errorType}\``)
    lines.push(``)
  }

  if (data.cluster) {
    const c = data.cluster
    lines.push(`### Cluster`)
    lines.push(`- Platform: \`${c.platform}\` | K8s: \`${c.kubernetesVersion}\` | Nodes: ${c.nodeCount} | Namespaces: ${c.namespaceCount}${c.inCluster ? ' | In-Cluster' : ''}`)
    lines.push(``)
  }

  if (data.cache) {
    lines.push(`### Cache`)
    lines.push(`- Total Resources: ${data.cache.totalResources.toLocaleString()} | Watched Kinds: ${Object.keys(data.cache.resourceCounts).length}`)
    lines.push(``)
  }

  if (data.metrics) {
    const pod = data.metrics.podMetrics
    const node = data.metrics.nodeMetrics
    lines.push(`### Metrics Collection`)
    lines.push(`- Pod: ${pod.collecting ? 'collecting' : 'idle'} (${pod.trackedCount} tracked, ${pod.consecutiveErrors} errors)`)
    lines.push(`- Node: ${node.collecting ? 'collecting' : 'idle'} (${node.trackedCount} tracked, ${node.consecutiveErrors} errors)`)
    if (pod.lastError) lines.push(`- Pod Error: ${pod.lastError}`)
    if (node.lastError) lines.push(`- Node Error: ${node.lastError}`)
    lines.push(``)
  }

  if (data.eventPipeline) {
    const ep = data.eventPipeline
    const totalReceived = Object.values(ep.received).reduce((a, b) => a + b, 0)
    const totalDropped = Object.values(ep.dropped).reduce((a, b) => a + b, 0)
    lines.push(`### Event Pipeline`)
    lines.push(`- Received: ${totalReceived.toLocaleString()} | Dropped: ${totalDropped.toLocaleString()} | Uptime: ${ep.uptime}`)
    if (ep.recentDrops && ep.recentDrops.length > 0) {
      lines.push(`- Recent drops: ${ep.recentDrops.slice(0, 5).map(d => `${d.kind}/${d.name} (${d.reason})`).join(', ')}`)
    }
    lines.push(``)
  }

  if (data.timeline) {
    const t = data.timeline
    lines.push(`### Timeline`)
    lines.push(`- Storage: \`${t.storageType}\` | Events: ${t.totalEvents.toLocaleString()} | Errors: ${t.storeErrors} | Drops: ${t.totalDrops}`)
    lines.push(``)
  }

  if (data.informers) {
    const inf = data.informers
    lines.push(`### Informers`)
    lines.push(`- Typed: ${inf.typedCount} | Dynamic: ${inf.dynamicCount}`)
    if (inf.watchedCRDs && inf.watchedCRDs.length > 0) {
      lines.push(`- CRDs: ${inf.watchedCRDs.join(', ')}`)
    }
    lines.push(``)
  }

  if (data.prometheus) {
    const p = data.prometheus
    lines.push(`### Prometheus`)
    lines.push(`- Connected: ${p.connected ? 'Yes' : 'No'}${p.serviceName ? ` | Service: ${p.serviceNamespace}/${p.serviceName}` : ''}`)
    lines.push(``)
  }

  if (data.traffic) {
    const t = data.traffic
    lines.push(`### Traffic`)
    lines.push(`- Active: \`${t.activeSource || 'none'}\`${t.detected?.length ? ` | Detected: ${t.detected.join(', ')}` : ''}`)
    lines.push(``)
  }

  if (data.runtime) {
    const rt = data.runtime
    lines.push(`### Runtime`)
    lines.push(`- Heap: ${rt.heapMB.toFixed(1)} MB | Objects: ${rt.heapObjectsK.toFixed(1)}K | Goroutines: ${rt.goroutines} | CPUs: ${rt.numCPU}`)
    if (data.sse) lines.push(`- SSE Clients: ${data.sse.connectedClients}`)
    lines.push(``)
  }

  if (data.config) {
    const cfg = data.config
    lines.push(`### Config`)
    lines.push(`- Port: ${cfg.port} | Dev: ${cfg.devMode} | Timeline: \`${cfg.timelineStorage}\` | History: ${cfg.historyLimit} | MCP: ${cfg.mcpEnabled} | Prometheus URL: ${cfg.hasPrometheusURL ? 'manual' : 'auto'}`)
    lines.push(``)
  }

  if (data.errors && data.errors.length > 0) {
    lines.push(`### Collection Errors`)
    for (const e of data.errors) {
      lines.push(`- ${e}`)
    }
    lines.push(``)
  }

  lines.push(`<details><summary>Raw JSON</summary>`)
  lines.push(``)
  lines.push('```json')
  lines.push(JSON.stringify(data, null, 2))
  lines.push('```')
  lines.push(`</details>`)

  return lines.join('\n')
}
