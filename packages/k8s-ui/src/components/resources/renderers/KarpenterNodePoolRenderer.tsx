import { Server, Settings, Shield, Cpu, Tag, BarChart3 } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import {
  getNodePoolStatus,
  getNodePoolNodeClassRef,
  getNodePoolDisruptionPolicy,
  getNodePoolRequirements,
  getNodePoolWeight,
} from '../resource-utils-karpenter'

function formatCpuCores(value: unknown): string {
  const quantity = String(value)
  if (quantity.endsWith('m')) {
    const millis = parseInt(quantity, 10)
    if (!isNaN(millis)) return String(millis / 1000)
  }
  return quantity
}


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
  const templateTaints = spec.template?.spec?.taints || []
  const templateStartupTaints = spec.template?.spec?.startupTaints || []
  const statusResources = status.resources || {}

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

      {/* Resource Usage — from status.resources vs spec.limits */}
      {(statusResources.cpu || statusResources.memory) && (
        <Section title="Resource Usage" icon={BarChart3} defaultExpanded>
          <PropertyList>
            {statusResources.cpu && (
              <Property
                label="CPU"
                value={`${formatCpuCores(statusResources.cpu)}${spec.limits?.cpu ? ` / ${formatCpuCores(spec.limits.cpu)}` : ''}`}
              />
            )}
            {statusResources.memory && (
              <Property
                label="Memory"
                value={`${statusResources.memory}${spec.limits?.memory ? ` / ${spec.limits.memory}` : ''}`}
              />
            )}
          </PropertyList>
        </Section>
      )}

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

      {/* Template Taints */}
      {templateTaints.length > 0 && (
        <Section title={`Template Taints (${templateTaints.length})`} icon={Shield} defaultExpanded>
          <div className="flex flex-wrap gap-1">
            {templateTaints.map((taint: any, i: number) => (
              <span
                key={i}
                className="badge-sm bg-theme-hover text-theme-text-secondary"
              >
                {taint.key}={taint.value || ''}:{taint.effect || ''}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Template Startup Taints */}
      {templateStartupTaints.length > 0 && (
        <Section title={`Startup Taints (${templateStartupTaints.length})`} icon={Shield} defaultExpanded>
          <div className="flex flex-wrap gap-1">
            {templateStartupTaints.map((taint: any, i: number) => (
              <span
                key={i}
                className="badge-sm bg-theme-hover text-theme-text-secondary"
              >
                {taint.key}={taint.value || ''}:{taint.effect || ''}
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
