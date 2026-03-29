import { Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'

interface VPARendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function formatResource(value: string | undefined): string {
  if (!value) return '-'
  return value
}

export function VPARenderer({ data, onNavigate }: VPARendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const targetRef = spec.targetRef || {}
  const updatePolicy = spec.updatePolicy || {}
  const resourcePolicy = spec.resourcePolicy || {}
  const containerPolicies = resourcePolicy.containerPolicies || []
  const recommendations = status.recommendation?.containerRecommendations || []
  const conditions = status.conditions || []

  const updateMode = updatePolicy.updateMode || 'Auto'
  const isOff = updateMode === 'Off'

  // Problem detection
  const hasRecommendation = conditions.some((c: any) => c.type === 'RecommendationProvided' && c.status === 'True')
  const noRecommendation = conditions.some((c: any) => c.type === 'RecommendationProvided' && c.status === 'False')
  const configUnsupported = conditions.some((c: any) => c.type === 'ConfigUnsupported' && c.status === 'True')
  const lowConfidence = conditions.some((c: any) => c.type === 'LowConfidence' && c.status === 'True')

  return (
    <>
      {/* Problem alerts */}
      {configUnsupported && (
        <AlertBanner
          variant="error"
          title="Configuration Unsupported"
          message={conditions.find((c: any) => c.type === 'ConfigUnsupported')?.message}
        />
      )}

      {noRecommendation && !configUnsupported && (
        <AlertBanner
          variant="warning"
          title="No Recommendations"
          message={conditions.find((c: any) => c.type === 'RecommendationProvided')?.message || 'VPA has not produced recommendations yet — insufficient metrics data'}
        />
      )}

      {lowConfidence && hasRecommendation && (
        <AlertBanner
          variant="info"
          title="Low Confidence"
          message="Recommendations are based on limited data — consider waiting for more metrics"
        />
      )}

      {/* Target & Mode */}
      <Section title="Configuration" icon={Cpu}>
        <PropertyList>
          <Property label="Target" value={
            targetRef.name ? (
              <ResourceLink
                name={targetRef.name}
                kind={kindToPlural(targetRef.kind || 'Deployment')}
                namespace={data.metadata?.namespace || ''}
                label={`${targetRef.kind}/${targetRef.name}`}
                onNavigate={onNavigate}
              />
            ) : undefined
          } />
          <Property label="Update Mode" value={
            <span className={clsx(
              updateMode === 'Auto' && 'text-green-400',
              updateMode === 'Recreate' && 'text-yellow-400',
              updateMode === 'Initial' && 'text-blue-400',
              updateMode === 'Off' && 'text-theme-text-tertiary',
            )}>
              {updateMode}
            </span>
          } />
          {isOff && (
            <Property label="" value={
              <span className="text-xs text-theme-text-tertiary">Recommendation-only mode — no automatic updates</span>
            } />
          )}
        </PropertyList>
      </Section>

      {/* Recommendations */}
      {recommendations.length > 0 && (
        <Section title="Recommendations" defaultExpanded>
          <div className="space-y-3">
            {recommendations.map((rec: any) => (
              <div key={rec.containerName} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary mb-2">{rec.containerName}</div>
                <div className="overflow-x-auto">
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="text-theme-text-tertiary">
                        <th className="text-left py-1 pr-3 font-medium w-24"></th>
                        <th className="text-right py-1 px-2 font-medium">CPU</th>
                        <th className="text-right py-1 px-2 font-medium">Memory</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr>
                        <td className="py-1 pr-3 text-theme-text-secondary">Target</td>
                        <td className="text-right py-1 px-2 text-green-400 font-medium">{formatResource(rec.target?.cpu)}</td>
                        <td className="text-right py-1 px-2 text-green-400 font-medium">{formatResource(rec.target?.memory)}</td>
                      </tr>
                      <tr>
                        <td className="py-1 pr-3 text-theme-text-secondary">Lower Bound</td>
                        <td className="text-right py-1 px-2 text-theme-text-secondary">{formatResource(rec.lowerBound?.cpu)}</td>
                        <td className="text-right py-1 px-2 text-theme-text-secondary">{formatResource(rec.lowerBound?.memory)}</td>
                      </tr>
                      <tr>
                        <td className="py-1 pr-3 text-theme-text-secondary">Upper Bound</td>
                        <td className="text-right py-1 px-2 text-theme-text-secondary">{formatResource(rec.upperBound?.cpu)}</td>
                        <td className="text-right py-1 px-2 text-theme-text-secondary">{formatResource(rec.upperBound?.memory)}</td>
                      </tr>
                      {rec.uncappedTarget && (
                        <tr>
                          <td className="py-1 pr-3 text-theme-text-tertiary">Uncapped</td>
                          <td className="text-right py-1 px-2 text-theme-text-tertiary">{formatResource(rec.uncappedTarget?.cpu)}</td>
                          <td className="text-right py-1 px-2 text-theme-text-tertiary">{formatResource(rec.uncappedTarget?.memory)}</td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Resource Policy */}
      {containerPolicies.length > 0 && (
        <Section title="Resource Policy" defaultExpanded>
          <div className="space-y-3">
            {containerPolicies.map((policy: any, i: number) => (
              <div key={i} className="card-inner-lg">
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">{policy.containerName || '*'}</span>
                  {policy.mode && (
                    <span className={clsx(
                      'px-1.5 py-0.5 rounded text-[10px] font-medium',
                      policy.mode === 'Off' ? 'bg-theme-hover text-theme-text-tertiary' : 'bg-blue-500/20 text-blue-400'
                    )}>
                      {policy.mode}
                    </span>
                  )}
                </div>
                <PropertyList>
                  {policy.minAllowed && (
                    <Property label="Min Allowed" value={
                      <span className="text-xs">
                        {policy.minAllowed.cpu && `CPU: ${policy.minAllowed.cpu}`}
                        {policy.minAllowed.cpu && policy.minAllowed.memory && ' · '}
                        {policy.minAllowed.memory && `Mem: ${policy.minAllowed.memory}`}
                      </span>
                    } />
                  )}
                  {policy.maxAllowed && (
                    <Property label="Max Allowed" value={
                      <span className="text-xs">
                        {policy.maxAllowed.cpu && `CPU: ${policy.maxAllowed.cpu}`}
                        {policy.maxAllowed.cpu && policy.maxAllowed.memory && ' · '}
                        {policy.maxAllowed.memory && `Mem: ${policy.maxAllowed.memory}`}
                      </span>
                    } />
                  )}
                  {policy.controlledResources && (
                    <Property label="Controlled" value={policy.controlledResources.join(', ')} />
                  )}
                  {policy.controlledValues && (
                    <Property label="Controlled Values" value={policy.controlledValues} />
                  )}
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
