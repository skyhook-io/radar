import { Badge, RadarUpgradeNote } from '@skyhook-io/k8s-ui'
import { describeLokiLabels, useLokiStatus } from '../../api/loki'
import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'

/**
 * Read-only Loki status for installations whose integrations are set by the
 * operator (--loki-url, --loki-header) rather than saved per cluster here.
 */
export function LokiStatusCard({ active }: { active: boolean }) {
  const { data, error } = useLokiStatus(active)
  if (error instanceof RadarFeatureUnsupportedError) return <RadarUpgradeNote requirement={error.radarUpgrade} />
  if (error) return <p className="text-sm text-theme-text-secondary">Status unavailable: {(error as Error).message}</p>
  if (!data) return <p className="text-sm text-theme-text-tertiary">Checking…</p>
  const status = !data.configured ? 'Off' : data.connected ? 'Connected' : data.discovering ? 'Discovering…' : 'Not connected'
  return (
    <div className="space-y-1.5 text-sm text-theme-text-secondary">
      <p>Set by this installation’s operator with <code>--loki-url</code> and <code>--loki-header</code>; without a URL Radar finds Loki in the cluster.</p>
      <div className="flex flex-wrap items-center gap-2">
        <Badge severity={data.connected ? 'success' : data.configured && !data.discovering ? 'warning' : 'neutral'}>{status}</Badge>
        {data.address && <span className="text-theme-text-tertiary">{data.address}</span>}
      </div>
      {data.labels && <p className="text-xs text-theme-text-tertiary">Stream labels: {describeLokiLabels(data.labels)}</p>}
      {(data.historyReason || (!data.connected && data.error)) && <p className="text-xs text-theme-text-tertiary">{data.historyReason || data.error}</p>}
    </div>
  )
}
