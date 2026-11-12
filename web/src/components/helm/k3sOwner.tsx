import { Server } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { NavigateToResource } from '../../utils/navigation'

export type HelmControllerOwner = { tool: 'flux'; ref: string } | { tool: 'k3s'; chart: string; config?: string }

// Who reinstalls this release from its own definition, so a Helm change here
// is reverted. Flux wins if both are set (the server already prefers it).
export function helmControllerOwner(r: { managedByFluxHelmRelease?: string; managedByK3sHelmChart?: string; k3sHelmChartConfig?: string }): HelmControllerOwner | null {
  if (r.managedByFluxHelmRelease) return { tool: 'flux', ref: r.managedByFluxHelmRelease }
  if (r.managedByK3sHelmChart) return { tool: 'k3s', chart: r.managedByK3sHelmChart, ...(r.k3sHelmChartConfig ? { config: r.k3sHelmChartConfig } : {}) }
  return null
}

export function k3sManagedText(owner: { chart: string; config?: string }): string {
  const head = `Managed by k3s. HelmChart ${owner.chart} reinstalls this release from its own chart, so an upgrade or rollback here is reverted.`
  return owner.config ? `${head} Change values in HelmChartConfig ${owner.config}.` : `${head} Create a HelmChartConfig ${owner.chart} to change its values.`
}

// A k3s ref "namespace/name" as a drawer target for its kind.
function k3sTarget(ref: string, kind: 'helmcharts' | 'helmchartconfigs') {
  const [namespace, name] = ref.split('/')
  return { kind, group: 'helm.cattle.io', namespace: namespace ?? '', name: name ?? '' }
}

export function K3sManagedNote({ owner, onNavigateToResource }: { owner: { chart: string; config?: string }; onNavigateToResource?: NavigateToResource }) {
  return (
    <Tooltip content={k3sManagedText(owner)} wrapperClassName="mt-1">
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
