import { formatMemoryBytes } from '@skyhook-io/k8s-ui/utils/format'
import { isForbiddenError, useAutoPromConnect, useCloudRole, usePrometheusPVCUsage, usePrometheusStatus } from '../../api/client'
import { useNavCustomization } from '../../context/NavCustomization'

export function PVCUsageBar({ namespace, name }: { namespace: string; name: string }) {
  // PVC detail can be the first Prometheus-backed surface a user opens; without
  // this, the gauge silently stays hidden until they open a workload metrics tab.
  useAutoPromConnect()
  const { canAtLeast, isLoading: roleLoading } = useCloudRole()
  const settingsAvailable = !useNavCustomization().embedded
  const canConfigure = settingsAvailable && !roleLoading && canAtLeast('owner')
  const { data: status, error: statusError } = usePrometheusStatus()
  const isConnected = status?.connected === true
  const { data: usage, error } = usePrometheusPVCUsage(namespace, name, isConnected)

  let unavailable: string | undefined
  if (isForbiddenError(error)) {
    unavailable = "You don't have access to usage metrics for this PVC."
  } else if (isForbiddenError(statusError)) {
    unavailable = 'Metrics access denied.'
  } else if (statusError) {
    unavailable = 'Could not check the metrics connection.'
  } else if (!status) {
    unavailable = 'Checking metrics availability…'
  } else if (status.discovering) {
    unavailable = 'Discovering Prometheus…'
  } else if (!isConnected) {
    unavailable = 'Prometheus is not connected. Used space is unknown.'
  } else if (error || usage?.status === 'query_failed') {
    unavailable = 'The usage query failed. Used space is unknown.'
  } else if (!usage) {
    unavailable = 'Loading usage measurements…'
  } else if (usage.status === 'no_series') {
    unavailable = 'No usage measurements reported for this volume.'
  } else if (usage.status === 'invalid_data') {
    unavailable = 'Volume usage measurements are invalid. Used space is unknown.'
  } else if (!usage.hasData) {
    unavailable = 'Usage measurements are unavailable.'
  }

  const denied = isForbiddenError(error) || isForbiddenError(statusError)
  const waiting = !error && !statusError && (!status || status.discovering || (isConnected && !usage))
  const guidance = denied
    ? 'Ask your operator to review your metrics access.'
    : !canConfigure && !roleLoading
      ? !isConnected || statusError
        ? 'Ask your operator to check the metrics connection for this cluster.'
        : 'Ask your operator to check metrics availability for this volume.'
      : undefined

  if (unavailable || !usage) {
    return (
      <section aria-label="PVC usage" className="rounded-lg border border-theme-border bg-theme-surface/30 p-3">
        <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide mb-1">Usage</div>
        <p className="text-sm text-theme-text-tertiary">{unavailable}</p>
        {!waiting && (
          canConfigure && !denied ? (
            <button
              type="button"
              className="mt-2 text-xs text-accent hover:underline"
              onClick={() => window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'prometheus' } }))}
            >Configure metrics</button>
          ) : guidance ? (
            <p className="mt-2 text-xs text-theme-text-tertiary">{guidance}</p>
          ) : null
        )}
      </section>
    )
  }

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

