import { Server, Cpu, Settings } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import {
  getNodeClaimStatus,
  getNodeClaimInstanceType,
  getNodeClaimNodeName,
  getNodeClaimCapacity,
  getNodeClaimNodePoolRef,
  getNodeClaimRequirements,
  getNodeClaimNodeClassRef,
  getNodeClaimExpireAfter,
} from '../resource-utils-karpenter'

function formatRelativeTime(isoString: string): string {
  const date = new Date(isoString)
  if (isNaN(date.getTime())) return ''
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

interface KarpenterNodeClaimRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function KarpenterNodeClaimRenderer({ data, onNavigate }: KarpenterNodeClaimRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const claimStatus = getNodeClaimStatus(data)
  const isNotReady = claimStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const capacity = getNodeClaimCapacity(data)
  const requirements = getNodeClaimRequirements(data)
  const nodeClassRef = getNodeClaimNodeClassRef(data)
  const expireAfter = getNodeClaimExpireAfter(data)

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
          {nodeClassRef && (
            <Property
              label="NodeClass"
              value={nodeClassRef.name ? (
                <ResourceLink
                  name={nodeClassRef.name}
                  kind={kindToPlural(nodeClassRef.kind || 'EC2NodeClass')}
                  namespace=""
                  group={nodeClassRef.group}
                  label={`${nodeClassRef.kind || 'EC2NodeClass'}/${nodeClassRef.name}`}
                  onNavigate={onNavigate}
                />
              ) : '-'}
            />
          )}
          {status.imageID && <Property label="Image ID" value={status.imageID} />}
          {expireAfter && <Property label="Expire After" value={expireAfter} />}
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

      {/* Requirements */}
      {requirements.length > 0 && (
        <Section title={`Requirements (${requirements.length})`} icon={Settings}>
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

      {/* Provisioning Timeline */}
      <Section title="Provisioning" defaultExpanded>
        <div className="space-y-1">
          {steps.map((step, index) => {
            const cond = conditions.find((c: any) => c.type === step.type)
            const isComplete = cond?.status === 'True'
            const isFailed = cond?.status === 'False'
            const isPending = !cond
            const transitionTime = cond?.lastTransitionTime

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
                    'text-sm flex-1',
                    isCurrent ? 'text-theme-text-primary font-medium' : 'text-theme-text-secondary'
                  )}
                >
                  {step.label}
                </span>
                {transitionTime && (
                  <span className="text-xs text-theme-text-tertiary shrink-0">
                    {formatRelativeTime(transitionTime)}
                  </span>
                )}
              </div>
            )
          })}
        </div>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
