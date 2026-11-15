import { GitBranch, Server } from 'lucide-react'
import type { HelmReleaseController } from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'
import { kindToPluralWithGroup, type NavigateToResource } from '../../utils/navigation'

// The controller of a release. An older Radar sends only the Flux field.
export function releaseControllerOf(r: { controller?: HelmReleaseController; managedByFluxHelmRelease?: string }): HelmReleaseController | undefined {
  if (r.controller) return r.controller
  if (!r.managedByFluxHelmRelease) return undefined
  const [namespace = '', name = ''] = r.managedByFluxHelmRelease.split('/')
  return { tool: 'flux', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', namespace, name }
}

// A controller other than Flux. Flux keeps its own routing (GitOps link,
// route-only upgrade hint); any other gets the generic note, and its upgrade
// hints are hidden because Radar has no view to route an upgrade to.
export function otherController(r: { controller?: HelmReleaseController; managedByFluxHelmRelease?: string }): HelmReleaseController | undefined {
  const c = releaseControllerOf(r)
  return c && c.tool !== 'flux' ? c : undefined
}

// Upgrade hints are hidden for any controller other than Flux, including a k3s
// HelmChart the caller can't read.
export function managedByOtherController(r: { controller?: HelmReleaseController; managedByFluxHelmRelease?: string; managedByK3s?: boolean }): boolean {
  return !!otherController(r) || !!r.managedByK3s
}

export const HIDDEN_K3S_TEXT = "Managed by the k3s/RKE2 helm-controller. The owning HelmChart's namespace isn't available in this view, or Radar couldn't check your access. An upgrade or rollback here is overwritten when its install Job reruns; change the release through its HelmChart."

export function HiddenK3sBadge() {
  return (
    <Tooltip content={HIDDEN_K3S_TEXT}>
      <span className="badge-sm shrink-0 border border-theme-border bg-theme-elevated text-theme-text-secondary">
        <Server className="w-3 h-3" />
        k3s
      </span>
    </Tooltip>
  )
}

const TOOL_LABELS: Record<string, string> = { flux: 'Flux', k3s: 'k3s' }

function controllerLabel(c: HelmReleaseController): string {
  return TOOL_LABELS[c.tool] ?? c.tool
}

const refOf = (o: { namespace: string; name: string }) => `${o.namespace}/${o.name}`

export function controllerManagedText(c: HelmReleaseController): string {
  // helm-controller re-runs its install Job only when the Job's config hash changes.
  const effect = c.tool === 'k3s'
    ? 'an upgrade or rollback here is overwritten the next time helm-controller runs the install Job (when the HelmChart or its HelmChartConfig changes).'
    : `an upgrade or rollback here can be overwritten by that ${c.kind}.`
  const head = `Managed by ${controllerLabel(c)}. ${c.kind} ${refOf(c)} installs this release from its own definition, so ${effect}`
  const v = c.valuesRef
  if (!v) return `${head} Change it through that ${c.kind}.`
  if (v.exists === true) return `${head} Change values in ${v.kind} ${refOf(v)}.`
  if (v.exists === false) return `${head} Create a ${v.kind} ${refOf(v)} to change its values.`
  // Unknown: the read failed, or the caller may not see it.
  return `${head} Change values in ${v.kind} ${refOf(v)} (create it if it does not exist).`
}

export function ReleaseControllerBadge({ controller }: { controller: HelmReleaseController }) {
  const flux = controller.tool === 'flux'
  const tip = flux
    ? `Installed by Flux helm-controller via HelmRelease ${refOf(controller)}. Changes here will be reverted at the next reconcile — manage via the GitOps tab.`
    : controllerManagedText(controller)
  return (
    <Tooltip content={tip}>
      <span className="badge-sm shrink-0 border border-theme-border bg-theme-elevated text-theme-text-secondary">
        {flux ? <GitBranch className="w-3 h-3" /> : <Server className="w-3 h-3" />}
        {controllerLabel(controller)}
      </span>
    </Tooltip>
  )
}

function target(o: { kind: string; group: string; namespace: string; name: string }) {
  return { kind: kindToPluralWithGroup(o.kind, o.group), group: o.group, namespace: o.namespace, name: o.name }
}

// The drawer note for a controller other than Flux (Flux keeps its GitOps link).
export function ReleaseControllerNote({ controller, onNavigateToResource }: { controller: HelmReleaseController; onNavigateToResource?: NavigateToResource }) {
  const v = controller.valuesRef
  return (
    <Tooltip content={controllerManagedText(controller)} wrapperClassName="mt-1">
      <span className="inline-flex flex-wrap items-center gap-1 rounded border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-400">
        <Server className="w-3 h-3" />
        Managed by {controllerLabel(controller)} ·
        <button type="button" className="hover:underline" onClick={() => onNavigateToResource?.(target(controller))}>{controller.kind} {refOf(controller)}</button>
        {v?.exists === true && <>
          ·
          <button type="button" className="hover:underline" onClick={() => onNavigateToResource?.(target(v))}>{v.kind} {refOf(v)}</button>
        </>}
      </span>
    </Tooltip>
  )
}
