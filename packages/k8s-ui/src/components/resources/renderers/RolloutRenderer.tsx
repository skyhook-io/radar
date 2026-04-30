import { Server, GitBranch, AlertTriangle, Network } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, PodTemplateSection } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import { BADGE_INACTIVE } from '../../../utils/badge-colors'

interface RolloutRendererProps {
  data: any
}

export function RolloutRenderer({ data }: RolloutRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const phase = status.phase || 'Unknown'
  const conditions = status.conditions || []

  const canaryStrategy = spec.strategy?.canary
  const blueGreenStrategy = spec.strategy?.blueGreen
  const isCanary = !!canaryStrategy
  const steps = canaryStrategy?.steps || []
  const currentStepIndex = status.currentStepIndex
  const trafficRouting = canaryStrategy?.trafficRouting

  // Detect traffic routing provider
  const trafficProvider = trafficRouting
    ? (() => {
        if (trafficRouting.istio) return { name: 'Istio', details: trafficRouting.istio }
        if (trafficRouting.nginx) return { name: 'Nginx', details: trafficRouting.nginx }
        if (trafficRouting.alb) return { name: 'ALB', details: trafficRouting.alb }
        if (trafficRouting.smi) return { name: 'SMI', details: trafficRouting.smi }
        if (trafficRouting.traefik) return { name: 'Traefik', details: trafficRouting.traefik }
        if (trafficRouting.ambassador) return { name: 'Ambassador', details: trafficRouting.ambassador }
        return null
      })()
    : null

  // Problem detection
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []

  // Aborted rollout detection — must come before generic paused check
  if (status.abort === true) {
    problems.push({ color: 'red', message: status.message || 'Rollout was aborted' })
  }

  if (phase === 'Degraded') {
    problems.push({ color: 'red', message: status.message || 'Rollout is degraded' })
  }

  const progressDeadlineCond = conditions.find(
    (c: any) => c.type === 'Progressing' && c.status === 'False' && c.reason === 'ProgressDeadlineExceeded'
  )
  if (progressDeadlineCond) {
    problems.push({ color: 'red', message: progressDeadlineCond.message || 'Progress deadline exceeded' })
  }

  const invalidSpecCond = conditions.find(
    (c: any) => c.type === 'InvalidSpec' && c.status === 'True'
  )
  if (invalidSpecCond) {
    problems.push({ color: 'red', message: invalidSpecCond.message || 'Invalid rollout spec' })
  }

  // Pause conditions — show specific reasons instead of generic "Rollout is paused"
  const pauseConditions: Array<{ reason: string; startTime?: string }> = status.pauseConditions || []
  if (phase === 'Paused' && !status.abort) {
    if (pauseConditions.length > 0) {
      const reasons = pauseConditions.map((pc: any) => {
        const reason = pc.reason || 'Unknown'
        const since = pc.startTime ? ` (since ${formatAge(pc.startTime)})` : ''
        return `${reason}${since}`
      })
      problems.push({ color: 'yellow', message: `Rollout is paused: ${reasons.join('; ')}` })
    } else {
      problems.push({ color: 'yellow', message: 'Rollout is paused' })
    }
  }

  // Phase badge color
  const phaseColor = (() => {
    switch (phase) {
      case 'Healthy': return 'status-healthy'
      case 'Paused': return 'status-degraded'
      case 'Progressing': return 'status-degraded'
      case 'Degraded': return 'status-unhealthy'
      default: return 'status-unknown'
    }
  })()

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
      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property
            label="Phase"
            value={
              <span className={clsx('badge', phaseColor)}>
                {phase}
              </span>
            }
          />
          <Property label="Replicas" value={`${status.availableReplicas || 0}/${spec.replicas || 0}`} />
          <Property label="Updated" value={status.updatedReplicas} />
          <Property label="Current Pod Hash" value={status.currentPodHash} />
          <Property label="Stable RS" value={status.stableRS} />
          <Property label="Message" value={status.message} />
        </PropertyList>
      </Section>

      {/* Strategy section */}
      <Section title="Strategy" icon={GitBranch}>
        <PropertyList>
          {isCanary ? (
            <>
              <Property label="Strategy" value="Canary" />
              {steps.length > 0 && currentStepIndex !== undefined && (
                <Property label="Current Step" value={`${currentStepIndex}/${steps.length}`} />
              )}
              {status.canary?.weights?.canary?.weight !== undefined && (
                <Property label="Canary Weight" value={`${status.canary.weights.canary.weight}%`} />
              )}
              {status.canary?.weights?.stable?.weight !== undefined && (
                <Property label="Stable Weight" value={`${status.canary.weights.stable.weight}%`} />
              )}
              <Property label="Canary Service" value={canaryStrategy.canaryService} />
              <Property label="Stable Service" value={canaryStrategy.stableService} />
            </>
          ) : blueGreenStrategy ? (
            <>
              <Property label="Strategy" value="Blue-Green" />
              <Property label="Active Service" value={blueGreenStrategy.activeService} />
              <Property label="Preview Service" value={blueGreenStrategy.previewService} />
              <Property
                label="Auto Promote"
                value={
                  blueGreenStrategy.autoPromotionEnabled !== undefined
                    ? String(blueGreenStrategy.autoPromotionEnabled)
                    : undefined
                }
              />
            </>
          ) : (
            <Property label="Strategy" value="Unknown" />
          )}
        </PropertyList>
      </Section>

      {/* Traffic Routing */}
      {trafficProvider && (
        <Section title="Traffic Routing" icon={Network}>
          <PropertyList>
            <Property
              label="Provider"
              value={
                <span className="badge-sm status-neutral">{trafficProvider.name}</span>
              }
            />
            {trafficProvider.name === 'Istio' && (
              <>
                {trafficProvider.details.virtualService?.name && (
                  <Property label="VirtualService" value={trafficProvider.details.virtualService.name} />
                )}
                {trafficProvider.details.destinationRule?.name && (
                  <Property label="DestinationRule" value={trafficProvider.details.destinationRule.name} />
                )}
              </>
            )}
            {trafficProvider.name === 'ALB' && (
              <>
                {trafficProvider.details.ingress && (
                  <Property label="Ingress" value={trafficProvider.details.ingress} />
                )}
                {trafficProvider.details.servicePort != null && (
                  <Property label="Service Port" value={trafficProvider.details.servicePort} />
                )}
              </>
            )}
            {trafficProvider.name === 'Nginx' && (
              <>
                {trafficProvider.details.stableIngress && (
                  <Property label="Stable Ingress" value={trafficProvider.details.stableIngress} />
                )}
                {trafficProvider.details.additionalIngressAnnotations && (
                  <Property
                    label="Annotations"
                    value={Object.keys(trafficProvider.details.additionalIngressAnnotations).length + ' annotations'}
                  />
                )}
              </>
            )}
            {trafficProvider.name === 'SMI' && (
              <>
                {trafficProvider.details.rootService && (
                  <Property label="Root Service" value={trafficProvider.details.rootService} />
                )}
                {trafficProvider.details.trafficSplitName && (
                  <Property label="TrafficSplit" value={trafficProvider.details.trafficSplitName} />
                )}
              </>
            )}
            {trafficProvider.name === 'Traefik' && (
              <>
                {trafficProvider.details.weightedTraefikServiceName && (
                  <Property label="Weighted Service" value={trafficProvider.details.weightedTraefikServiceName} />
                )}
              </>
            )}
            {trafficProvider.name === 'Ambassador' && (
              <>
                {trafficProvider.details.mappings && (
                  <Property label="Mappings" value={trafficProvider.details.mappings.join(', ')} />
                )}
              </>
            )}
          </PropertyList>
        </Section>
      )}

      {/* Canary Steps visual */}
      {isCanary && steps.length > 0 && (
        <Section title={`Canary Steps (${steps.length})`} defaultExpanded>
          <div className="space-y-1">
            {steps.map((step: any, index: number) => {
              const isCompleted = currentStepIndex !== undefined && index < currentStepIndex
              const isCurrent = currentStepIndex !== undefined && index === currentStepIndex
              const isPending = currentStepIndex === undefined || index > currentStepIndex

              const stepLabel = (() => {
                if (step.setWeight !== undefined) return `Set Weight: ${step.setWeight}%`
                if (step.pause !== undefined) {
                  if (step.pause.duration) return `Pause: ${step.pause.duration}`
                  return 'Pause: manual'
                }
                // Handle other step types generically
                const key = Object.keys(step)[0]
                if (key) return `${key}: ${JSON.stringify(step[key])}`
                return 'Unknown step'
              })()

              return (
                <div
                  key={index}
                  className={clsx(
                    'flex items-center gap-2 px-2 py-1.5 rounded text-sm',
                    isCurrent && 'bg-blue-500/10 border border-blue-500/30',
                    isCompleted && 'opacity-80',
                    isPending && 'opacity-50'
                  )}
                >
                  {/* Status indicator */}
                  <span
                    className={clsx(
                      'w-5 h-5 rounded-full flex items-center justify-center text-xs shrink-0',
                      isCompleted && 'bg-green-500/20 text-green-400',
                      isCurrent && 'bg-blue-500/20 text-blue-400',
                      isPending && BADGE_INACTIVE
                    )}
                  >
                    {isCompleted ? '\u2713' : isCurrent ? '\u25CF' : '\u25CB'}
                  </span>

                  {/* Step index */}
                  <span className="text-theme-text-tertiary text-xs w-4 shrink-0">{index}</span>

                  {/* Step label */}
                  <span
                    className={clsx(
                      'text-sm',
                      isCurrent ? 'text-theme-text-primary font-medium' : 'text-theme-text-secondary'
                    )}
                  >
                    {stepLabel}
                  </span>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Pod Template section (collapsed by default) */}
      <Section title="Pod Template" defaultExpanded={false}>
        <PodTemplateSection template={spec.template} />
      </Section>

      {/* Conditions section */}
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
