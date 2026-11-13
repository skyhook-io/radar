import { Section, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import type { ResourceRef } from '../../../types'
import type { LoggingPodFlow, LoggingPodShipping } from '../../../types/core'
import { LOGGING_GROUP } from '../resource-utils-logging'

const KIND_LABELS: Record<string, string> = { clusterflows: 'ClusterFlows', flows: 'Flows', clusteroutputs: 'ClusterOutputs', outputs: 'Outputs', loggings: 'Loggings' }
const PLURALS: Record<string, string> = { ClusterFlow: 'clusterflows', Flow: 'flows', ClusterOutput: 'clusteroutputs', Output: 'outputs' }

export interface PodLogShippingProps {
  data: LoggingPodShipping | null
  loading: boolean
  error: Error | null
  onNavigate?: (ref: ResourceRef) => void
}

// Why a selecting Flow delivers nothing, from what the server reported.
function nowhereReason(flow: LoggingPodFlow): string {
  if (flow.active === false) return 'the Flow is inactive'
  if (flow.outputs.length === 0) return 'the Flow names no output'
  return 'no output it names exists and is active'
}

function FlowLine({ flow, onNavigate }: { flow: LoggingPodFlow; onNavigate?: (ref: ResourceRef) => void }) {
  return (
    <div className="space-y-0.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <ResourceLink name={flow.name} kind={PLURALS[flow.kind] ?? 'flows'} namespace={flow.namespace} group={LOGGING_GROUP} label={`${flow.kind} ${flow.name}`} onNavigate={onNavigate} />
        {flow.selected && <Badge severity="success" size="sm">selected</Badge>}
        {flow.uncertain && <Badge severity="warning" size="sm">may be selected</Badge>}
        {flow.ignored && <Badge severity="neutral" size="sm">ignored by the operator</Badge>}
        {flow.outputs.map((o) => (
          <span key={`${o.kind}/${o.name}`} className="inline-flex items-center gap-1 text-theme-text-secondary">
            → <ResourceLink name={o.name} kind={PLURALS[o.kind] ?? 'outputs'} namespace={o.namespace ?? ''} group={LOGGING_GROUP} label={o.type ? `${o.name} (${o.type})` : o.name} onNavigate={o.found && o.namespace ? onNavigate : null} />
            {!o.found && <Badge severity="neutral" size="sm">not found</Badge>}
            {o.found && o.active === false && <Badge severity="error" size="sm">inactive</Badge>}
            {o.found && o.active === undefined && <Badge severity="neutral" size="sm">state not reported</Badge>}
          </span>
        ))}
      </div>
      {flow.selected && flow.hasFilters && <div className="text-xs text-theme-text-tertiary">This Flow filters records; some lines may be dropped.</div>}
    </div>
  )
}

/** Which Logging operator Flows forward this pod's logs, or why none does. */
export function PodLogShippingSection({ data, loading, error, onNavigate }: PodLogShippingProps) {
  if (data && !data.installed) return null
  if (!data) {
    return (
      <Section title="Log shipping" defaultExpanded={true}>
        {error
          ? <AlertBanner variant="warning" title="Could not read log shipping" message={error.message} className="mb-0" />
          : <div className="text-sm text-theme-text-tertiary">{loading ? 'Reading Flows…' : 'No answer yet.'}</div>}
      </Section>
    )
  }
  const counted = data.flows.filter((f) => !f.ignored)
  const delivering = counted.filter((f) => f.selected && f.delivers === true)
  const maybe = counted.filter((f) => f.uncertain || (f.selected && f.delivers === undefined))
  const nowhere = counted.filter((f) => f.selected && f.delivers === false)
  const excluded = counted.filter((f) => f.excluded)
  const closest = counted.filter((f) => !f.selected && !f.uncertain && !f.excluded)
    .sort((a, b) => a.unmet.length - b.unmet.length || a.name.localeCompare(b.name))[0]
  const unread = data.readErrors.map((k) => KIND_LABELS[k] ?? k)
  const line = (f: LoggingPodFlow) => <FlowLine key={`${f.kind}/${f.namespace}/${f.name}`} flow={f} onNavigate={onNavigate} />
  return (
    <Section title="Log shipping" defaultExpanded={true}>
      <div className="space-y-2 text-sm">
        {unread.length > 0 && <AlertBanner variant="info" title={`Radar could not read ${unread.join(', ')}`} message="Whether this pod's logs are shipped can't be told from what Radar read. In-cluster, the chart collects Logging operator kinds only with rbac.crdGroups.logging enabled." className="mb-0" />}
        {delivering.length > 0 && (
          <div className="space-y-1">
            <div className="text-theme-text-secondary">Shipped by</div>
            {delivering.map(line)}
          </div>
        )}
        {maybe.length > 0 && (
          <div className="space-y-1">
            <div className="text-theme-text-secondary">May be shipped by</div>
            {maybe.map(line)}
          </div>
        )}
        {nowhere.map((f) => (
          <div key={`n-${f.kind}/${f.namespace}/${f.name}`} className="space-y-0.5">
            <div className="text-theme-text-primary">Selected, but its logs go nowhere: {nowhereReason(f)}.</div>
            {line(f)}
          </div>
        ))}
        {delivering.length + maybe.length + nowhere.length === 0 && unread.length === 0 && (
          <div className="space-y-1">
            <div className="text-theme-text-primary">Not shipped: no Flow selects this pod. Its logs are lost with the pod.</div>
            {excluded.map((f) => <div key={`x-${f.namespace}/${f.name}`} className="text-theme-text-secondary">Excluded by {f.name}</div>)}
            {closest && (
              <div className="text-theme-text-secondary">
                Closest match: {closest.name}{closest.unmet.length > 0 && <> needs {closest.unmet.join(' and ')}</>}
              </div>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}
