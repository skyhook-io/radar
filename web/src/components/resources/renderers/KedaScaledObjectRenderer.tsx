import { Cpu } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../drawer-components'
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
}

export function KedaScaledObjectRenderer({ data }: KedaScaledObjectRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const soStatus = getScaledObjectStatus(data)
  const triggers = getScaledObjectTriggers(data)

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
          <Property label="Target" value={getScaledObjectTarget(data)} />
          <Property label="Replicas" value={getScaledObjectReplicas(data)} />
          <Property label="Polling Interval" value={`${getScaledObjectPollingInterval(data)}s`} />
          <Property label="Cooldown Period" value={`${getScaledObjectCooldownPeriod(data)}s`} />
          <Property label="Generated HPA" value={getScaledObjectHpaName(data)} />
          <Property label="Last Active" value={getScaledObjectLastActiveTime(data)} />
        </PropertyList>
      </Section>

      {/* Triggers section */}
      {triggers.length > 0 && (
        <Section title={`Triggers (${triggers.length})`} defaultExpanded>
          <div className="space-y-2">
            {triggers.map((trigger, i) => (
              <div key={i} className="bg-theme-elevated/30 rounded p-2">
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-theme-text-primary font-medium">{trigger.type}</span>
                  {trigger.name && (
                    <span className="text-theme-text-tertiary">({trigger.name})</span>
                  )}
                </div>
                {trigger.metadata && Object.keys(trigger.metadata).length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {Object.entries(trigger.metadata).map(([k, v]) => (
                      <span key={k} className="px-1.5 py-0.5 bg-theme-hover rounded text-xs text-theme-text-secondary">
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
