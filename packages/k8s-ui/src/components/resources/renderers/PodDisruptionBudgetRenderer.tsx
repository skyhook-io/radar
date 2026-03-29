import { Shield, Activity } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList, AlertBanner } from '../../ui/drawer-components'

interface PodDisruptionBudgetRendererProps {
  data: any
}

export function PodDisruptionBudgetRenderer({ data }: PodDisruptionBudgetRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const matchLabels = spec.selector?.matchLabels || {}
  const hasSelector = Object.keys(matchLabels).length > 0

  // Determine budget type
  const hasMaxUnavailable = spec.maxUnavailable !== undefined && spec.maxUnavailable !== null
  const hasMinAvailable = spec.minAvailable !== undefined && spec.minAvailable !== null
  const budgetType = hasMaxUnavailable ? 'Max Unavailable' : hasMinAvailable ? 'Min Available' : undefined
  const budgetValue = hasMaxUnavailable ? spec.maxUnavailable : hasMinAvailable ? spec.minAvailable : undefined

  // Problem detection
  const disruptionsAllowed = status.disruptionsAllowed
  const expectedPods = status.expectedPods
  const currentHealthy = status.currentHealthy
  const desiredHealthy = status.desiredHealthy

  const noDisruptionsAllowed = disruptionsAllowed === 0 && expectedPods > 0
  const insufficientHealthy = currentHealthy !== undefined && desiredHealthy !== undefined && currentHealthy < desiredHealthy

  return (
    <>
      {/* Problem alerts */}
      {insufficientHealthy && (
        <AlertBanner
          variant="error"
          title="Issues Detected"
          message={`Insufficient healthy pods (${currentHealthy} healthy, ${desiredHealthy} desired)`}
        />
      )}

      {noDisruptionsAllowed && !insufficientHealthy && (
        <AlertBanner
          variant="warning"
          title="Issues Detected"
          message="No disruptions currently allowed"
        />
      )}

      <Section title="Budget" icon={Shield}>
        <PropertyList>
          <Property label="Budget Type" value={budgetType} />
          <Property label="Budget Value" value={budgetValue !== undefined ? String(budgetValue) : undefined} />
          <Property
            label="Disruptions"
            value={
              disruptionsAllowed !== undefined ? (
                <span className={clsx(
                  disruptionsAllowed > 0 ? 'text-green-400' : 'text-red-400'
                )}>
                  {disruptionsAllowed} allowed
                </span>
              ) : undefined
            }
          />
          <Property label="Eviction Policy" value={spec.unhealthyPodEvictionPolicy} />
        </PropertyList>
      </Section>

      <Section title="Pod Status" icon={Activity}>
        <PropertyList>
          <Property
            label="Current Healthy"
            value={
              currentHealthy !== undefined ? (
                <span className={clsx(
                  desiredHealthy !== undefined && currentHealthy >= desiredHealthy
                    ? 'text-green-400'
                    : 'text-red-400'
                )}>
                  {currentHealthy}
                </span>
              ) : undefined
            }
          />
          <Property label="Desired Healthy" value={desiredHealthy} />
          <Property label="Expected Pods" value={expectedPods} />
        </PropertyList>
        {currentHealthy !== undefined && expectedPods !== undefined && expectedPods > 0 && (
          <div className="mt-3 card-inner text-sm">
            <div className="flex items-center justify-between">
              <span className="text-theme-text-secondary">Health</span>
              <span className={clsx(
                desiredHealthy !== undefined && currentHealthy >= desiredHealthy
                  ? 'text-green-400'
                  : 'text-red-400'
              )}>
                {currentHealthy}/{expectedPods} healthy
              </span>
            </div>
            <div className="mt-2 h-2 bg-theme-hover rounded overflow-hidden">
              <div
                className={clsx(
                  'h-full transition-all',
                  desiredHealthy !== undefined && currentHealthy >= desiredHealthy
                    ? 'bg-green-500'
                    : 'bg-red-500'
                )}
                style={{ width: `${Math.min(100, (currentHealthy / expectedPods) * 100)}%` }}
              />
            </div>
          </div>
        )}
      </Section>

      <Section title="Selector">
        {hasSelector ? (
          <KeyValueBadgeList items={matchLabels} />
        ) : (
          <div className="text-sm text-theme-text-tertiary">All pods in namespace</div>
        )}
      </Section>

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
