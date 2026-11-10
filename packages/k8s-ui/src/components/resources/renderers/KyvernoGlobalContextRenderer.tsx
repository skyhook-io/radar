import { Globe, RefreshCw } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { HEALTH_BADGE_COLORS } from '../../../utils/badge-colors'
import { healthColors } from '../resource-utils'
import type { StatusBadge } from '../resource-utils'
import { TimeValue } from '../../ui/ScheduleValue'

type Nav = (ref: { kind: string; namespace: string; name: string; group?: string }) => void

/**
 * Whether the entry has ever produced data.
 *
 * Kyverno records success as `status.lastRefreshTime` and records failure as
 * nothing at all — there is no message, no condition, no reason. So the only
 * honest reading is "it has refreshed" versus "it has never refreshed", and the
 * age of the entry is what turns the second into a diagnosis. Inventing a cause
 * for a silent failure would be worse than naming the silence.
 */
export function getKyvernoContextRefresh(resource: any): {
  refreshed: boolean
  at?: string
  level: 'healthy' | 'unhealthy' | 'unknown'
} {
  const at = resource?.status?.lastRefreshTime
  if (typeof at === 'string' && at !== '') return { refreshed: true, at, level: 'healthy' }
  // Young enough that the first refresh may simply not have happened yet.
  const created = resource?.metadata?.creationTimestamp
  const ageMs = created ? Date.now() - new Date(created).getTime() : 0
  return { refreshed: false, level: ageMs > 2 * 60 * 1000 ? 'unhealthy' : 'unknown' }
}

/**
 * GlobalContextEntry — external data a policy reads while it evaluates.
 *
 * It matters because a policy referencing an entry that never resolves fails at
 * evaluation time, and the failure surfaces as a policy error with no obvious
 * cause. This page is where that cause lives.
 */
export function KyvernoGlobalContextRenderer({ data, onNavigate }: { data: any; onNavigate?: Nav }) {
  const spec = data?.spec ?? {}
  const api = spec.apiCall
  const cm = spec.configMap
  const refresh = getKyvernoContextRefresh(data)

  return (
    <>
      {refresh.level === 'unhealthy' && (
        <AlertBanner
          variant="error"
          title="This entry has never refreshed"
          // Deliberately not a diagnosis: Kyverno records no reason for a failed
          // refresh, so the page reports the fact and where to look.
          message="Kyverno records no reason when a refresh fails. Any policy referencing this entry will error while it evaluates. Check the source below is reachable and that Kyverno may read it."
        />
      )}

      <Section title="Refresh" icon={RefreshCw} defaultExpanded>
        <PropertyList>
          <Property
            label="State"
            value={
              <span
                className={clsx('badge', HEALTH_BADGE_COLORS[refresh.level as keyof typeof HEALTH_BADGE_COLORS])}
              >
                {refresh.refreshed ? 'Refreshed' : refresh.level === 'unhealthy' ? 'Never refreshed' : 'Not refreshed yet'}
              </span>
            }
          />
          {refresh.at && <Property label="Last Refreshed" value={<TimeValue timestamp={refresh.at} />} />}
          {api?.refreshInterval && <Property label="Interval" value={api.refreshInterval} />}
          {api?.retryLimit != null && <Property label="Retry Limit" value={String(api.retryLimit)} />}
        </PropertyList>
        {!refresh.refreshed && refresh.level === 'unknown' && (
          <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
            Created moments ago — the first refresh may not have run yet.
          </div>
        )}
      </Section>

      <Section title="Source" icon={Globe} defaultExpanded>
        {api ? (
          <PropertyList>
            <Property label="Kind" value="Kubernetes API call" />
            <Property label="Method" value={api.method || 'GET'} />
            <Property label="Path" value={<span className="font-mono text-xs">{api.urlPath || '-'}</span>} />
            {api.jmesPath && (
              <Property label="Filter" value={<span className="font-mono text-xs">{api.jmesPath}</span>} />
            )}
          </PropertyList>
        ) : cm ? (
          <PropertyList>
            <Property label="Kind" value="ConfigMap" />
            <Property
              label="ConfigMap"
              value={
                <ResourceLink
                  name={cm.name}
                  kind="configmaps"
                  namespace={cm.namespace ?? ''}
                  onNavigate={onNavigate}
                />
              }
            />
            {cm.namespace && <Property label="Namespace" value={cm.namespace} />}
          </PropertyList>
        ) : (
          <div className="text-sm text-theme-text-tertiary">
            No API call or ConfigMap source is declared, so this entry can never produce data.
          </div>
        )}
      </Section>
    </>
  )
}

/** List badge for GlobalContextEntry, mirroring the detail page's three states. */
export function getKyvernoGlobalContextStatus(resource: any): StatusBadge {
  const r = getKyvernoContextRefresh(resource)
  if (r.refreshed) return { text: 'Refreshed', color: healthColors.healthy, level: 'healthy' }
  if (r.level === 'unhealthy') {
    return { text: 'Never Refreshed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Pending', color: healthColors.unknown, level: 'unknown' }
}
