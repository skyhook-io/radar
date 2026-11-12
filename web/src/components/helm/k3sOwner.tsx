import { Server } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { NavigateToResource } from '../../utils/navigation'

// observed: the caller may be unable to read a HelmChartConfig that exists,
// so an absent one is not stated as missing.
export function k3sManagedText(owner: { chart: string; config?: string }, opts: { observed?: boolean } = {}): string {
  const head = `Managed by k3s. HelmChart ${owner.chart} reinstalls this release from its own chart, so an upgrade or rollback here is reverted.`
  if (owner.config) return `${head} Change values in HelmChartConfig ${owner.config}.`
  if (opts.observed) return `${head} Change values in HelmChartConfig ${owner.chart} (create it if it does not exist).`
  return `${head} Create a HelmChartConfig ${owner.chart} to change its values.`
}

// A k3s ref "namespace/name" as a drawer target for its kind.
function k3sTarget(ref: string, kind: 'helmcharts' | 'helmchartconfigs') {
  const [namespace, name] = ref.split('/')
  return { kind, group: 'helm.cattle.io', namespace: namespace ?? '', name: name ?? '' }
}

export function K3sManagedNote({ owner, observed, onNavigateToResource }: { owner: { chart: string; config?: string }; observed?: boolean; onNavigateToResource?: NavigateToResource }) {
  return (
    <Tooltip content={k3sManagedText(owner, { observed })} wrapperClassName="mt-1">
      <span className="inline-flex flex-wrap items-center gap-1 rounded border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-400">
        <Server className="w-3 h-3" />
        Managed by k3s ·
        <button type="button" className="hover:underline" onClick={() => onNavigateToResource?.(k3sTarget(owner.chart, 'helmcharts'))}>HelmChart {owner.chart}</button>
        {owner.config && <>
          ·
          <button type="button" className="hover:underline" onClick={() => onNavigateToResource?.(k3sTarget(owner.config!, 'helmchartconfigs'))}>HelmChartConfig {owner.config}</button>
        </>}
      </span>
    </Tooltip>
  )
}
