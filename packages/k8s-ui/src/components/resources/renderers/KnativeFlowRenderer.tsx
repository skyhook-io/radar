import { ListOrdered, GitFork } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface RendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function RefDisplay({ ref: destRef, ns, onNavigate }: { ref: any; ns: string; onNavigate?: RendererProps['onNavigate'] }) {
  if (!destRef) return <span className="text-theme-text-tertiary">-</span>

  if (destRef.ref) {
    return (
      <ResourceLink
        name={destRef.ref.name}
        kind={kindToPlural(destRef.ref.kind || 'Service')}
        namespace={destRef.ref.namespace || ns}
        onNavigate={onNavigate}
      />
    )
  }

  if (destRef.uri) {
    return <span className="text-theme-text-primary break-all text-xs">{destRef.uri}</span>
  }

  return <span className="text-theme-text-tertiary">-</span>
}

// ============================================================================
// Sequence
// ============================================================================

export function SequenceRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const steps = spec.steps || []
  const reply = spec.reply

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Sequence" />

      <Section title="Overview" icon={ListOrdered} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Steps" value={String(steps.length)} />
          {reply && (
            <Property label="Reply" value={
              <RefDisplay ref={reply} ns={ns} onNavigate={onNavigate} />
            } />
          )}
        </PropertyList>
      </Section>

      {steps.length > 0 && (
        <Section title={`Steps (${steps.length})`} defaultExpanded>
          <div className="space-y-2">
            {steps.map((step: any, i: number) => (
              <div key={i} className="card-inner flex items-center gap-2">
                <span className="text-xs font-medium text-theme-text-tertiary w-6 shrink-0">#{i + 1}</span>
                <RefDisplay ref={step} ns={ns} onNavigate={onNavigate} />
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// Parallel
// ============================================================================

export function ParallelRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const branches = spec.branches || []
  const reply = spec.reply

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Parallel" />

      <Section title="Overview" icon={GitFork} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Branches" value={String(branches.length)} />
          {reply && (
            <Property label="Reply" value={
              <RefDisplay ref={reply} ns={ns} onNavigate={onNavigate} />
            } />
          )}
        </PropertyList>
      </Section>

      {branches.length > 0 && (
        <Section title={`Branches (${branches.length})`} defaultExpanded>
          <div className="space-y-3">
            {branches.map((branch: any, i: number) => (
              <div key={i} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary mb-2">Branch {i + 1}</div>
                <PropertyList>
                  {branch.filter && (
                    <Property label="Filter" value={
                      <RefDisplay ref={branch.filter} ns={ns} onNavigate={onNavigate} />
                    } />
                  )}
                  <Property label="Subscriber" value={
                    <RefDisplay ref={branch.subscriber} ns={ns} onNavigate={onNavigate} />
                  } />
                  {branch.reply && (
                    <Property label="Reply" value={
                      <RefDisplay ref={branch.reply} ns={ns} onNavigate={onNavigate} />
                    } />
                  )}
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
