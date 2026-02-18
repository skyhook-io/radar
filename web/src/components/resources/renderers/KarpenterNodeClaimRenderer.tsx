import { Server, Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../drawer-components'
import {
  getNodeClaimStatus,
  getNodeClaimInstanceType,
  getNodeClaimNodeName,
  getNodeClaimCapacity,
  getNodeClaimNodePoolRef,
} from '../resource-utils-karpenter'

interface KarpenterNodeClaimRendererProps {
  data: any
}

export function KarpenterNodeClaimRenderer({ data }: KarpenterNodeClaimRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const claimStatus = getNodeClaimStatus(data)
  const isNotReady = claimStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const capacity = getNodeClaimCapacity(data)

  // Provisioning steps for timeline
  const steps = [
    { type: 'Initialized', label: 'Initialized' },
    { type: 'Launched', label: 'Launched' },
    { type: 'Registered', label: 'Registered' },
    { type: 'Ready', label: 'Ready' },
  ]

  return (
    <>
      {/* Problem alert */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="NodeClaim Not Ready"
          message={readyCond?.message || 'The NodeClaim is not in a ready state.'}
        />
      )}

      {/* Instance Info */}
      <Section title="Instance" icon={Server}>
        <PropertyList>
          <Property label="Instance Type" value={getNodeClaimInstanceType(data)} />
          <Property label="Node Name" value={getNodeClaimNodeName(data)} />
          <Property label="NodePool" value={getNodeClaimNodePoolRef(data)} />
          {status.imageID && <Property label="Image ID" value={status.imageID} />}
        </PropertyList>
      </Section>

      {/* Capacity */}
      {Object.keys(capacity).length > 0 && (
        <Section title="Capacity" icon={Cpu}>
          <PropertyList>
            {capacity.cpu && <Property label="CPU" value={capacity.cpu} />}
            {capacity.memory && <Property label="Memory" value={capacity.memory} />}
            {capacity.pods && <Property label="Pods" value={capacity.pods} />}
            {capacity['ephemeral-storage'] && <Property label="Ephemeral Storage" value={capacity['ephemeral-storage']} />}
          </PropertyList>
        </Section>
      )}

      {/* Provisioning Timeline */}
      <Section title="Provisioning" defaultExpanded>
        <div className="space-y-1">
          {steps.map((step, index) => {
            const cond = conditions.find((c: any) => c.type === step.type)
            const isComplete = cond?.status === 'True'
            const isFailed = cond?.status === 'False'
            const isPending = !cond

            // Find the current step (first non-True)
            const currentStepIndex = steps.findIndex((s) => {
              const c = conditions.find((c: any) => c.type === s.type)
              return c?.status !== 'True'
            })
            const isCurrent = index === currentStepIndex

            return (
              <div
                key={step.type}
                className={clsx(
                  'flex items-center gap-2 px-2 py-1.5 rounded text-sm',
                  isCurrent && 'bg-blue-500/10 border border-blue-500/30',
                  isComplete && 'opacity-80',
                  isPending && !isCurrent && 'opacity-50'
                )}
              >
                <span
                  className={clsx(
                    'w-5 h-5 rounded-full flex items-center justify-center text-xs shrink-0',
                    isComplete && 'bg-green-500/20 text-green-400',
                    isCurrent && 'bg-blue-500/20 text-blue-400',
                    isFailed && 'bg-red-500/20 text-red-400',
                    isPending && !isCurrent && 'bg-gray-500/20 text-gray-400'
                  )}
                >
                  {isComplete ? '\u2713' : isFailed ? '\u2717' : isCurrent ? '\u25CF' : '\u25CB'}
                </span>
                <span className="text-theme-text-tertiary text-xs w-4 shrink-0">{index}</span>
                <span
                  className={clsx(
                    'text-sm',
                    isCurrent ? 'text-theme-text-primary font-medium' : 'text-theme-text-secondary'
                  )}
                >
                  {step.label}
                </span>
              </div>
            )
          })}
        </div>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
