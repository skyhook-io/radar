import { Server, Settings, Shield, Cpu, Tag } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import {
  getNodePoolStatus,
  getNodePoolNodeClassRef,
  getNodePoolDisruptionPolicy,
  getNodePoolRequirements,
  getNodePoolWeight,
} from '../resource-utils-karpenter'

interface KarpenterNodePoolRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function KarpenterNodePoolRenderer({ data, onNavigate }: KarpenterNodePoolRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const poolStatus = getNodePoolStatus(data)
  const isNotReady = poolStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const requirements = getNodePoolRequirements(data)
  const weight = getNodePoolWeight(data)
  const disruption = spec.disruption || {}
  const templateLabels = spec.template?.metadata?.labels || {}
  const templateExpireAfter = spec.template?.spec?.expireAfter
  const nodeClassRef = spec.template?.spec?.nodeClassRef

  return (
    <>
      {/* Problem alert */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="NodePool Not Ready"
          message={readyCond?.message || 'The NodePool is not in a ready state.'}
        />
      )}

      {/* NodeClass Reference */}
      <Section title="Node Class" icon={Server}>
        <PropertyList>
          <Property
            label="Reference"
            value={nodeClassRef?.name ? (
              <ResourceLink
                name={nodeClassRef.name}
                kind={kindToPlural(nodeClassRef.kind || 'EC2NodeClass')}
                namespace=""
                group={nodeClassRef.group}
                label={getNodePoolNodeClassRef(data)}
                onNavigate={onNavigate}
              />
            ) : getNodePoolNodeClassRef(data)}
          />
          {nodeClassRef?.group && (
            <Property label="API Group" value={nodeClassRef.group} />
          )}
          {nodeClassRef?.kind && (
            <Property label="Kind" value={nodeClassRef.kind} />
          )}
        </PropertyList>
      </Section>

      {/* Limits */}
      <Section title="Limits" icon={Cpu}>
        <PropertyList>
          {spec.limits?.cpu && <Property label="CPU" value={spec.limits.cpu} />}
          {spec.limits?.memory && <Property label="Memory" value={spec.limits.memory} />}
          {!spec.limits?.cpu && !spec.limits?.memory && (
            <Property label="Limits" value="No limits configured" />
          )}
          {weight !== undefined && <Property label="Weight" value={String(weight)} />}
        </PropertyList>
      </Section>

      {/* Disruption */}
      <Section title="Disruption" icon={Shield}>
        <PropertyList>
          <Property label="Consolidation Policy" value={getNodePoolDisruptionPolicy(data)} />
          {disruption.consolidateAfter && (
            <Property label="Consolidate After" value={disruption.consolidateAfter} />
          )}
          {(disruption.expireAfter || templateExpireAfter) && (
            <Property label="Expire After" value={disruption.expireAfter || templateExpireAfter} />
          )}
        </PropertyList>
        {disruption.budgets && disruption.budgets.length > 0 && (
          <div className="mt-2 space-y-1">
            <div className="text-xs text-theme-text-tertiary font-medium mb-1">Budgets</div>
            {disruption.budgets.map((budget: any, i: number) => (
              <div key={i} className="card-inner text-sm text-theme-text-secondary">
                {budget.nodes && <span>Nodes: {budget.nodes}</span>}
                {budget.schedule && <span className="ml-2">Schedule: {budget.schedule}</span>}
                {budget.duration && <span className="ml-2">Duration: {budget.duration}</span>}
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Template Labels */}
      {Object.keys(templateLabels).length > 0 && (
        <Section title="Template Labels" icon={Tag}>
          <div className="flex flex-wrap gap-1">
            {Object.entries(templateLabels).map(([key, val]) => (
              <span
                key={key}
                className="badge-sm bg-theme-hover text-theme-text-secondary"
              >
                {key}: {String(val)}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Requirements */}
      {requirements.length > 0 && (
        <Section title={`Requirements (${requirements.length})`} icon={Settings} defaultExpanded>
          <div className="space-y-1">
            {requirements.map((req: any, i: number) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-theme-text-primary font-medium">{req.key}</span>
                  <span className="text-theme-text-tertiary">{req.operator}</span>
                </div>
                {req.values && req.values.length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {req.values.map((v: string, vi: number) => (
                      <span key={vi} className="badge-sm bg-theme-hover text-theme-text-secondary">
                        {v}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
