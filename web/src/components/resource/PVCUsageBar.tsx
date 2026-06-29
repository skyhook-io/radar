import { formatMemoryBytes } from '@skyhook-io/k8s-ui/utils/format'
import { useAutoPromConnect, usePrometheusPVCUsage, usePrometheusStatus } from '../../api/client'

/**
 * PVCUsageBar — single-line capacity gauge derived from kubelet_volume_stats_*.
 *
 * Hidden silently when:
 *  - Prometheus isn't connected
 *  - The CSI driver doesn't implement NodeGetVolumeStats
 *  - Prometheus isn't scraping kubelet endpoints (notably GMP default config)
 *
 * Operators get nothing rather than a "no data" message that'd look like Radar
 * is broken — the absence is information enough.
 */
export function PVCUsageBar({ namespace, name }: { namespace: string; name: string }) {
  // PVC detail can be the first Prometheus-backed surface a user opens; without
  // this, the gauge silently stays hidden until they open a workload metrics tab.
  useAutoPromConnect()
  const { data: status } = usePrometheusStatus()
  const isConnected = status?.connected === true
  const { data: usage } = usePrometheusPVCUsage(namespace, name, isConnected)

  if (!usage || !usage.hasData) return null

  const pct = Math.max(0, Math.min(1, usage.ratio))
  const usedLabel = formatMemoryBytes(usage.used)
  const capLabel = formatMemoryBytes(usage.capacity)
  const pctLabel = `${(pct * 100).toFixed(0)}%`

  // Tone: green well under, amber > 75%, red > 90%. PVCs fill silently — the
  // top tone is justified because the consequence (write failures) is severe.
  const tone = pct >= 0.9 ? 'critical' : pct >= 0.75 ? 'warning' : 'ok'
  const barColor =
    tone === 'critical' ? 'bg-red-500' :
    tone === 'warning' ? 'bg-amber-500' :
    'bg-emerald-500'
  // Light/dark-paired text tones — `text-red-400` alone washes out in light
  // mode (Tailwind's 400 stop is calibrated for dark backgrounds).
  const textColor =
    tone === 'critical' ? 'text-red-700 dark:text-red-400' :
    tone === 'warning' ? 'text-amber-700 dark:text-amber-400' :
    'text-theme-text-secondary'

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide">Usage</span>
        <span className={`text-sm font-semibold tabular-nums ${textColor}`}>
          {usedLabel} <span className="text-theme-text-quaternary font-normal">/</span> {capLabel}
          <span className="ml-2 text-theme-text-tertiary text-xs font-normal">({pctLabel})</span>
        </span>
      </div>
      <div className="h-2 rounded-full bg-theme-elevated overflow-hidden">
        <div className={`h-full ${barColor} transition-all`} style={{ width: `${pct * 100}%` }} />
      </div>
    </section>
  )
}

