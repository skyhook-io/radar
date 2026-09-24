import type { ReactNode } from 'react'
import { Activity, GitBranch } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { AlertBanner, ConditionsSection, type ConditionTone, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { isRayObservationStale } from '../resource-utils-ray'

export interface RayServiceRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  runtimeEvidence?: Partial<Record<'active' | 'pending', ReactNode>>
}

const visibleEntries = 20

export function serveStateSeverity(state?: string): BadgeSeverity {
  if (state === 'RUNNING' || state === 'HEALTHY') return 'success'
  if (state === 'DEPLOY_FAILED' || state === 'UNHEALTHY') return 'error'
  if (['DEPLOYING', 'UPDATING', 'UPSCALING', 'DOWNSCALING', 'DELETING'].includes(state ?? '')) return 'warning'
  return 'neutral'
}

function orderedStatuses(statuses: Record<string, any> = {}) {
  const rank = (status?: string) => {
    const severity = serveStateSeverity(status)
    return severity === 'error' ? 0 : severity === 'neutral' ? 1 : severity === 'warning' ? 2 : 3
  }
  const entryRank = (entry: any) => Math.min(rank(entry.status), ...Object.values(entry.serveDeploymentStatuses ?? {}).map((deployment: any) => rank(deployment.status)))
  return Object.entries(statuses).sort(([a, av], [b, bv]) => entryRank(av) - entryRank(bv) || a.localeCompare(b))
}

export function rayServiceConditionTone(condition: any): ConditionTone {
  if (!['True', 'False'].includes(condition.status)) return 'unknown'
  if (condition.type === 'Ready') return condition.status === 'True' ? 'ok' : 'warning'
  if (['UpgradeInProgress', 'RollbackInProgress', 'Suspending'].includes(condition.type)) return condition.status === 'True' ? 'warning' : 'unknown'
  return 'unknown'
}

function NativeState({ name, data }: { name: string; data: any }) {
  return <>
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
      <span className="break-words text-sm font-medium text-theme-text-primary">{name}</span>
      <Badge severity={serveStateSeverity(data.status)} size="sm">{data.status || 'Not reported'}</Badge>
    </div>
    {data.message && <p className="mt-1 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{data.message}</p>}
  </>
}

function ServeApplications({ slot }: { slot: any }) {
  const apps = orderedStatuses(slot.applicationStatuses)
  return <div className="mt-3 space-y-3">
    <p className="text-xs text-theme-text-tertiary">Application states are snapshots reported by KubeRay, not a live Serve dashboard query.</p>
    {apps.length === 0 ? <p className="text-sm text-theme-text-secondary">Application status is not reported for this revision. During a NewCluster upgrade, the active application map can be cleared while its endpoint still serves requests.</p>
      : apps.slice(0, visibleEntries).map(([name, app]) => {
        const deployments = orderedStatuses(app.serveDeploymentStatuses)
        return <div className="card-inner min-w-0" key={name}>
          <NativeState name={name} data={app} />
          <div className="mt-3 space-y-2 border-t border-theme-border pt-2">
            <p className="text-xs text-theme-text-tertiary">Serve deployments{deployments.length > 0 ? ` (${deployments.length})` : ''}</p>
            {deployments.slice(0, visibleEntries).map(([deployment, state]) => <div className="min-w-0" key={deployment}><NativeState name={deployment} data={state} /></div>)}
            {deployments.length === 0 && <p className="text-sm text-theme-text-secondary">Deployment status is not reported.</p>}
            {deployments.length > visibleEntries && <p className="text-xs text-theme-text-tertiary">Showing {visibleEntries} of {deployments.length} deployments, failures first. See YAML for the full list.</p>}
          </div>
        </div>
      })}
    {apps.length > visibleEntries && <p className="text-xs text-theme-text-tertiary">Showing {visibleEntries} of {apps.length} applications, failures first. See YAML for the full list.</p>}
  </div>
}

export function RayServiceRenderer({ data, onNavigate, runtimeEvidence }: RayServiceRendererProps) {
  const status = data.status ?? {}
  const conditions = status.conditions ?? []
  const generation = data.metadata?.generation
  const suspended = data.spec?.suspend === true && conditions.some((condition: any) => condition.type === 'Suspended' && condition.status === 'True')
  const behind = isRayObservationStale(generation, status.observedGeneration)
  const stale = behind && !suspended
  const ready = conditions.find((condition: any) => condition.type === 'Ready')
  const readyLabel = ready?.status === 'True' ? 'Ready' : ready?.status === 'False' ? 'Not ready' : ready ? 'Unknown' : 'Not reported'
  const slots = (['active', 'pending'] as const).filter(slot => status[`${slot}ServiceStatus`]?.rayClusterName)
  return <>
    {stale && <AlertBanner variant="info" title="Reported state describes an earlier generation" message={`RayService generation ${generation}; controller observed ${status.observedGeneration}. The snapshots below may not reflect the latest specification.`} />}
    {behind && suspended && <p className="mb-3 text-sm text-theme-text-secondary">Reconciliation is paused while suspended; the controller may retain an earlier observed generation.</p>}
    <Section title="Serving and Rollout" icon={Activity} defaultExpanded>
      <PropertyList>
        <Property label="Proxy Readiness" value={<Badge severity={stale ? 'neutral' : ready?.status === 'True' ? 'success' : ready?.status === 'False' ? 'warning' : 'neutral'}>{readyLabel}{stale ? ' (stale)' : ''}</Badge>} />
        {ready?.reason && <Property label="Readiness Reason" value={ready.reason} />}
        {status.numServeEndpoints != null && <Property label="Reported Serve Endpoints" value={status.numServeEndpoints} />}
        <Property label="Suspension Requested" value={data.spec?.suspend === true ? 'Yes' : 'No'} />
        <Property label="Declared Upgrade Strategy" value={data.spec?.upgradeStrategy?.type || 'Not specified'} />
        {conditions.filter((condition: any) => ['UpgradeInProgress', 'RollbackInProgress', 'Suspending', 'Suspended'].includes(condition.type) && condition.status === 'True').map((condition: any) => <Property key={condition.type} label={condition.type} value={<Badge severity={stale ? 'neutral' : condition.type === 'Suspended' ? 'info' : 'warning'}>{condition.reason || 'True'}{stale ? ' (stale)' : ''}</Badge>} />)}
      </PropertyList>
      {ready?.message && <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{ready.message}</p>}
      <p className="mt-2 text-xs text-theme-text-tertiary">Ready means Serve proxy endpoints are reported, not that every application or revision is healthy. Endpoint counts span revisions. Suspension is complete only when the controller reports Suspended.</p>
      {status.observedGeneration == null && <p className="mt-2 text-xs text-theme-text-tertiary">The controller has not reported an observed generation.</p>}
    </Section>
    {slots.length === 0 && <Section title="Runtime Revisions" icon={GitBranch} defaultExpanded><p className="text-sm text-theme-text-secondary">No named runtime revision is reported.</p></Section>}
    {slots.map(role => {
      const slot = status[`${role}ServiceStatus`]
      return <Section key={role} title={`${role === 'active' ? 'Active' : 'Pending'} Revision`} icon={GitBranch} defaultExpanded>
        <div className="min-w-0 break-all [&_button]:text-left"><ResourceLink kind="rayclusters" group="ray.io" namespace={data.metadata?.namespace} name={slot.rayClusterName} onNavigate={onNavigate} /></div>
        {runtimeEvidence?.[role] ?? <p className="mt-2 text-sm text-theme-text-secondary">Open the RayCluster for its directly observed runtime status.</p>}
        {(slot.targetCapacity != null || slot.trafficRoutedPercent != null) && <div className="mt-3">
          <PropertyList>
            {slot.targetCapacity != null && <Property label="Serve Target Capacity" value={`${slot.targetCapacity}%`} />}
            {slot.trafficRoutedPercent != null && <Property label="Configured Traffic Share" value={`${slot.trafficRoutedPercent}%`} />}
          </PropertyList>
          <p className="mt-1 text-xs text-theme-text-tertiary">Target capacity scales Serve replica targets. Traffic share is configured route weight, not measured requests.</p>
        </div>}
        <ServeApplications slot={slot} />
      </Section>
    })}
    <ConditionsSection conditions={conditions} getConditionTone={condition => stale ? 'unknown' : rayServiceConditionTone(condition)} defaultExpanded />
  </>
}
