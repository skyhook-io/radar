import { Radio } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getPodMonitorEndpoints,
  getPodMonitorNamespaceSelector,
} from '../resource-utils-prometheus'

interface PodMonitorRendererProps {
  data: any
}

export function PodMonitorRenderer({ data }: PodMonitorRendererProps) {
  const endpoints = getPodMonitorEndpoints(data)
  const selector = data.spec?.selector?.matchLabels
  const conditions = data.status?.conditions

  return (
    <>
      <Section title="PodMonitor" icon={Radio}>
        <PropertyList>
          <Property label="Endpoints" value={String(endpoints.length)} />
          <Property label="Namespace Scope" value={getPodMonitorNamespaceSelector(data)} />
          <Property label="Sample Limit" value={data.spec?.sampleLimit ? String(data.spec.sampleLimit) : undefined} />
        </PropertyList>
      </Section>

      {endpoints.length > 0 && (
        <Section title={`Pod Metrics Endpoints (${endpoints.length})`} defaultExpanded>
          <div className="space-y-2">
            {endpoints.map((ep, i) => (
              <div key={i} className="card-inner text-sm">
                <div className="flex items-center gap-2 flex-wrap">
                  {ep.port && (
                    <span className="text-theme-text-primary font-medium">{ep.port}</span>
                  )}
                  <span className="text-theme-text-secondary">{ep.path}</span>
                  {ep.interval && (
                    <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                      every {ep.interval}
                    </span>
                  )}
                  {ep.scheme && ep.scheme !== 'http' && (
                    <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                      {ep.scheme}
                    </span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {selector && Object.keys(selector).length > 0 && (
        <Section title="Selector">
          <KeyValueBadgeList items={selector} />
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
