import { Radio } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getServiceMonitorEndpoints,
  getServiceMonitorJobLabel,
  getServiceMonitorNamespaceSelector,
} from '../resource-utils-prometheus'

interface ServiceMonitorRendererProps {
  data: any
}

export function ServiceMonitorRenderer({ data }: ServiceMonitorRendererProps) {
  const endpoints = getServiceMonitorEndpoints(data)
  const selector = data.spec?.selector?.matchLabels
  const conditions = data.status?.conditions

  return (
    <>
      <Section title="ServiceMonitor" icon={Radio}>
        <PropertyList>
          <Property label="Job Label" value={getServiceMonitorJobLabel(data)} />
          <Property label="Endpoints" value={String(endpoints.length)} />
          <Property label="Namespace Scope" value={getServiceMonitorNamespaceSelector(data)} />
          <Property label="Sample Limit" value={data.spec?.sampleLimit ? String(data.spec.sampleLimit) : undefined} />
        </PropertyList>
      </Section>

      {endpoints.length > 0 && (
        <Section title={`Scrape Endpoints (${endpoints.length})`} defaultExpanded>
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
