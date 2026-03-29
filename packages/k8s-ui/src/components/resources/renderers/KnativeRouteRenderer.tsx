import { Route } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface KnativeRouteRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KnativeRouteRenderer({ data, onNavigate }: KnativeRouteRendererProps) {
  const status = getKnativeConditionStatus(data)
  const traffic = data.status?.traffic || data.spec?.traffic || []
  const ns = data.metadata?.namespace || ''
  const url = data.status?.url || data.status?.address?.url

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Route" />

      <Section title="Overview" icon={Route} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="URL" value={url ? (
            <a href={url} target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:text-blue-300 hover:underline break-all">
              {url}
            </a>
          ) : undefined} />
        </PropertyList>
      </Section>

      {traffic.length > 0 && (
        <Section title={`Traffic Targets (${traffic.length})`} defaultExpanded>
          <div className="space-y-2">
            {traffic.map((t: any, i: number) => (
              <div key={i} className="flex items-center gap-2 text-sm">
                <div className="flex items-center gap-1 w-16 shrink-0">
                  <div className="flex-1 h-1.5 bg-theme-hover rounded-full overflow-hidden">
                    <div
                      className="h-full bg-blue-500 rounded-full"
                      style={{ width: `${t.percent || 0}%` }}
                    />
                  </div>
                  <span className="text-theme-text-secondary font-medium text-xs">{t.percent || 0}%</span>
                </div>
                {t.revisionName ? (
                  <ResourceLink
                    name={t.revisionName}
                    kind="revisions"
                    namespace={ns}
                    onNavigate={onNavigate}
                  />
                ) : (
                  <span className="text-theme-text-secondary">{t.configurationName || (t.latestRevision ? '@latest' : '-')}</span>
                )}
                {t.tag && (
                  <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary">
                    tag: {t.tag}
                  </span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
