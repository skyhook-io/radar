import { Bell } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'
import {
  getPrometheusRuleGroups,
  getPrometheusRuleTotalRules,
  getPrometheusRuleGroupCount,
} from '../resource-utils-prometheus'

interface PrometheusRuleRendererProps {
  data: any
}

export function PrometheusRuleRenderer({ data }: PrometheusRuleRendererProps) {
  const groups = getPrometheusRuleGroups(data)
  const totalRules = getPrometheusRuleTotalRules(data)
  const totalAlerts = groups.reduce((sum, g) => sum + g.alertCount, 0)
  const totalRecords = groups.reduce((sum, g) => sum + g.recordCount, 0)
  const conditions = data.status?.conditions

  return (
    <>
      <Section title="PrometheusRule" icon={Bell}>
        <PropertyList>
          <Property label="Groups" value={String(getPrometheusRuleGroupCount(data))} />
          <Property label="Total Rules" value={String(totalRules)} />
          <Property label="Alert Rules" value={String(totalAlerts)} />
          <Property label="Recording Rules" value={String(totalRecords)} />
        </PropertyList>
      </Section>

      {groups.length > 0 && (
        <Section title={`Rule Groups (${groups.length})`} defaultExpanded>
          <div className="space-y-2">
            {groups.map((group, i) => (
              <div key={i} className="card-inner text-sm">
                <div className="flex items-center justify-between">
                  <span className="text-theme-text-primary font-medium">{group.name}</span>
                  <div className="flex items-center gap-2">
                    {group.interval && (
                      <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                        {group.interval}
                      </span>
                    )}
                    <span className="text-xs text-theme-text-tertiary">
                      {group.ruleCount} rule{group.ruleCount !== 1 ? 's' : ''}
                    </span>
                  </div>
                </div>
                <div className="text-xs text-theme-text-secondary mt-1 flex gap-3">
                  {group.alertCount > 0 && <span>{group.alertCount} alert{group.alertCount !== 1 ? 's' : ''}</span>}
                  {group.recordCount > 0 && <span>{group.recordCount} recording</span>}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
