import { Cpu } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import {
  getScaledObjectStatus,
  getScaledObjectTarget,
  getScaledObjectReplicas,
  getScaledObjectTriggers,
  getScaledObjectHpaName,
  getScaledObjectLastActiveTime,
  getScaledObjectPollingInterval,
  getScaledObjectCooldownPeriod,
} from '../resource-utils-keda'

interface KedaScaledObjectRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KedaScaledObjectRenderer({ data, onNavigate }: KedaScaledObjectRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const soStatus = getScaledObjectStatus(data)
  const triggers = getScaledObjectTriggers(data)
  const fallback = data.spec?.fallback

  // Problem detection
  const isPaused = soStatus.text === 'Paused'
  const isFallback = soStatus.text === 'Fallback'
  const isNotReady = soStatus.level === 'unhealthy'

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const fallbackCond = conditions.find((c: any) => c.type === 'Fallback')

  return (
    <>
      {/* Problem alerts */}
      {isFallback && (
        <AlertBanner
          variant="error"
          title="Fallback Active"
          message={fallbackCond?.message || 'KEDA is using fallback replicas because triggers are failing.'}
        />
      )}
      {isNotReady && !isFallback && (
        <AlertBanner
          variant="error"
          title="ScaledObject Not Ready"
          message={readyCond?.message || 'The ScaledObject is not in a ready state.'}
        />
      )}
      {isPaused && (
        <AlertBanner
          variant="warning"
          title="Scaling Paused"
          message="Autoscaling is paused via annotation."
        />
      )}

      {/* Scaling section */}
      <Section title="Scaling" icon={Cpu}>
        <PropertyList>
          <Property label="Target" value={(() => {
            const target = data.spec?.scaleTargetRef
            if (target?.name) {
              return (
                <ResourceLink
                  name={target.name}
                  kind={kindToPlural(target.kind || 'Deployment')}
                  namespace={data.metadata?.namespace || ''}
                  label={getScaledObjectTarget(data)}
                  onNavigate={onNavigate}
                />
              )
            }
            return getScaledObjectTarget(data)
          })()} />
          <Property label="Replicas" value={getScaledObjectReplicas(data)} />
          <Property label="Polling Interval" value={`${getScaledObjectPollingInterval(data)}s`} />
          <Property label="Cooldown Period" value={`${getScaledObjectCooldownPeriod(data)}s`} />
          <Property label="Generated HPA" value={(() => {
            const hpaName = getScaledObjectHpaName(data)
            if (hpaName && hpaName !== '-') {
              return <ResourceLink name={hpaName} kind="horizontalpodautoscalers" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} />
            }
            return hpaName
          })()} />
          <Property label="Last Active" value={getScaledObjectLastActiveTime(data)} />
        </PropertyList>
        {fallback && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Fallback</div>
            <PropertyList>
              <Property label="Failure Threshold" value={String(fallback.failureThreshold ?? '-')} />
              <Property label="Replicas" value={String(fallback.replicas ?? '-')} />
            </PropertyList>
          </div>
        )}
      </Section>

      {/* Triggers section */}
      {triggers.length > 0 && (
        <Section title={`Triggers (${triggers.length})`} defaultExpanded>
          <div className="space-y-2">
            {triggers.map((trigger, i) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-theme-text-primary font-medium">{trigger.type}</span>
                  {trigger.name && (
                    <span className="text-theme-text-tertiary">({trigger.name})</span>
                  )}
                  {trigger.authenticationRef && (
                    <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                      auth: {trigger.authenticationRef.name}
                    </span>
                  )}
                </div>
                {trigger.metadata && Object.keys(trigger.metadata).length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {Object.entries(trigger.metadata).map(([k, v]) => (
                      <span key={k} className="badge-sm bg-theme-hover text-theme-text-secondary">
                        {k}: {v}
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
