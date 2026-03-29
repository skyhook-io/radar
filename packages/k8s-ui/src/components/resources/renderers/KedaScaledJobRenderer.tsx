import { Cpu } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getScaledJobStatus,
  getScaledJobTarget,
  getScaledJobStrategy,
  getScaledJobTriggers,
} from '../resource-utils-keda'

interface KedaScaledJobRendererProps {
  data: any
}

export function KedaScaledJobRenderer({ data }: KedaScaledJobRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.conditions || []

  const jobStatus = getScaledJobStatus(data)
  const isNotReady = jobStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const triggers = getScaledJobTriggers(data)

  return (
    <>
      {/* Problem alert */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="ScaledJob Not Ready"
          message={readyCond?.message || 'The ScaledJob is not in a ready state.'}
        />
      )}

      {/* Job Target section */}
      <Section title="Job Scaling" icon={Cpu}>
        <PropertyList>
          <Property label="Job Target" value={getScaledJobTarget(data)} />
          <Property label="Strategy" value={getScaledJobStrategy(data)} />
          {spec.pollingInterval !== undefined && (
            <Property label="Polling Interval" value={`${spec.pollingInterval}s`} />
          )}
          {spec.successfulJobsHistoryLimit !== undefined && (
            <Property label="Success History" value={String(spec.successfulJobsHistoryLimit)} />
          )}
          {spec.failedJobsHistoryLimit !== undefined && (
            <Property label="Failure History" value={String(spec.failedJobsHistoryLimit)} />
          )}
          {spec.maxReplicaCount !== undefined && (
            <Property label="Max Replicas" value={String(spec.maxReplicaCount)} />
          )}
        </PropertyList>
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
