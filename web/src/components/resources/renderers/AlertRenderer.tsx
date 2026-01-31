import { Bell, AlertTriangle, Filter, Send } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'
import { GitOpsStatusBadge } from '../../gitops'
import { fluxConditionsToGitOpsStatus, type FluxCondition } from '../../../types/gitops'

interface AlertRendererProps {
  data: any
}

export function AlertRenderer({ data }: AlertRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = (status.conditions || []) as FluxCondition[]

  // Convert to unified GitOps status
  const gitOpsStatus = fluxConditionsToGitOpsStatus(conditions, spec.suspend === true)

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  if (gitOpsStatus.suspended) {
    problems.push({ color: 'yellow', message: 'Alert is suspended' })
  }

  if (gitOpsStatus.health === 'Degraded' && gitOpsStatus.message) {
    problems.push({ color: 'red', message: gitOpsStatus.message })
  }

  // Event sources
  const eventSources = spec.eventSources || []
  const eventSeverity = spec.eventSeverity || 'info'
  const inclusionList = spec.inclusionList || []
  const exclusionList = spec.exclusionList || []

  return (
    <>
      {/* Problem alerts */}
      {problems.map((problem, i) => (
        <div
          key={i}
          className={clsx(
            'mb-4 p-3 border rounded-lg',
            problem.color === 'red'
              ? 'bg-red-500/10 border-red-500/30'
              : 'bg-yellow-500/10 border-yellow-500/30'
          )}
        >
          <div className="flex items-start gap-2">
            <AlertTriangle
              className={clsx(
                'w-4 h-4 mt-0.5 shrink-0',
                problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
              )}
            />
            <div className="flex-1 min-w-0">
              <div
                className={clsx(
                  'text-sm font-medium',
                  problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
                )}
              >
                {problem.color === 'red' ? 'Issue Detected' : 'Warning'}
              </div>
              <div
                className={clsx(
                  'text-xs mt-1',
                  problem.color === 'red' ? 'text-red-300/80' : 'text-yellow-300/80'
                )}
              >
                {problem.message}
              </div>
            </div>
          </div>
        </div>
      ))}

      {/* Status section */}
      <Section title="Status">
        <GitOpsStatusBadge status={gitOpsStatus} showHealth={false} />
      </Section>

      {/* Provider section */}
      <Section title="Provider" icon={Send}>
        <PropertyList>
          <Property label="Provider Ref" value={spec.providerRef?.name} />
          <Property
            label="Event Severity"
            value={
              <span className={clsx(
                'px-2 py-0.5 rounded text-xs font-medium',
                eventSeverity === 'error' ? 'bg-red-500/20 text-red-400' :
                eventSeverity === 'warning' ? 'bg-yellow-500/20 text-yellow-400' :
                'bg-blue-500/20 text-blue-400'
              )}>
                {eventSeverity}
              </span>
            }
          />
          {spec.summary && <Property label="Summary" value={spec.summary} />}
        </PropertyList>
      </Section>

      {/* Event Sources section */}
      {eventSources.length > 0 && (
        <Section title={`Event Sources (${eventSources.length})`} icon={Bell}>
          <div className="space-y-2">
            {eventSources.map((source: any, idx: number) => (
              <div
                key={idx}
                className="p-2 bg-theme-elevated/30 rounded text-sm"
              >
                <div className="flex items-center gap-2">
                  <span className="text-theme-text-tertiary">{source.kind}</span>
                  <span className="text-theme-text-primary">{source.name || '*'}</span>
                </div>
                {source.namespace && (
                  <div className="text-xs text-theme-text-tertiary mt-1">
                    Namespace: {source.namespace === '*' ? 'All' : source.namespace}
                  </div>
                )}
                {source.matchLabels && Object.keys(source.matchLabels).length > 0 && (
                  <div className="text-xs text-theme-text-tertiary mt-1">
                    Labels: {Object.entries(source.matchLabels).map(([k, v]) => `${k}=${v}`).join(', ')}
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Filters section */}
      {(inclusionList.length > 0 || exclusionList.length > 0) && (
        <Section title="Filters" icon={Filter} defaultExpanded={false}>
          <PropertyList>
            {inclusionList.length > 0 && (
              <Property
                label="Include"
                value={inclusionList.join(', ')}
              />
            )}
            {exclusionList.length > 0 && (
              <Property
                label="Exclude"
                value={exclusionList.join(', ')}
              />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Additional Info */}
      {status.observedGeneration !== undefined && (
        <Section title="Additional Info" defaultExpanded={false}>
          <PropertyList>
            <Property label="Observed Generation" value={status.observedGeneration} />
          </PropertyList>
        </Section>
      )}

      {/* Conditions section */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
