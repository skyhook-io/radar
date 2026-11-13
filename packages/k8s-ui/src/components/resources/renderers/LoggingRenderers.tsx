import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import { LOGGING_GROUP, getLoggingFlowRules, getLoggingOutputAddress, getLoggingOutputType, getLoggingStatus } from '../resource-utils-logging'

interface Props { data: any; onNavigate?: (ref: ResourceRef) => void }

function StatusProperty({ data }: { data: any }) {
  const status = getLoggingStatus(data)
  return <Property label="Operator status" value={<Badge severity={healthToSeverity(status.level)}>{status.text}</Badge>} />
}

// Problems is the operator's own list, shown verbatim; the Issues band shows
// the same problem when it is on, so the banner steps aside then.
function Problems({ data }: { data: any }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const problems: string[] = data?.status?.problems ?? []
  if (problems.length === 0 || operationalIssuesShown) return null
  return <AlertBanner variant="warning" title="The Logging operator reports problems" items={problems} />
}

export function LoggingRenderer({ data }: Props) {
  const checks: Record<string, boolean> = data.status?.configCheckResults ?? {}
  const failedChecks = Object.values(checks).filter((ok) => ok === false).length
  return <>
    <Problems data={data} />
    <Section title="Logging" defaultExpanded>
      <PropertyList>
        <Property label="Control namespace" value={data.spec?.controlNamespace || 'Not set'} />
        <Property label="Logging ref" value={data.spec?.loggingRef || 'Default'} />
        <Property label="Fluentd aggregator" value={data.spec?.fluentd ? 'Configured' : 'Not configured'} />
        <Property label="Fluent Bit collector" value={data.spec?.fluentbit ? 'Configured in this Logging' : 'Not in this Logging (see FluentbitAgents)'} />
        {Object.keys(checks).length > 0 && <Property label="Configuration checks" value={failedChecks > 0 ? `${failedChecks} of ${Object.keys(checks).length} failed` : `${Object.keys(checks).length} passed`} />}
      </PropertyList>
    </Section>
  </>
}

export function FluentbitAgentRenderer({ data }: Props) {
  return <>
    <Problems data={data} />
    <Section title="Fluent Bit agent" defaultExpanded>
      <PropertyList>
        <Property label="Logging ref" value={data.spec?.loggingRef || 'Default'} />
      </PropertyList>
    </Section>
  </>
}

// controlNamespaces is where the host found Loggings; a ClusterFlow outside
// all of them is ignored by the operator. Undefined: the host couldn't tell.
export function LoggingFlowRenderer({ data, onNavigate, controlNamespaces, loggingsUnreadable }: Props & { controlNamespaces?: string[]; loggingsUnreadable?: boolean }) {
  const cluster = data.kind === 'ClusterFlow'
  const ns = data.metadata?.namespace ?? ''
  const rules = getLoggingFlowRules(data)
  const filters: any[] = data.spec?.filters ?? []
  const globalRefs: string[] = data.spec?.globalOutputRefs ?? []
  const localRefs: string[] = [...(data.spec?.localOutputRefs ?? []), ...(data.spec?.outputRefs ?? [])]
  const notCounted = cluster && controlNamespaces !== undefined && !controlNamespaces.includes(ns)
  return <>
    {cluster && loggingsUnreadable && <AlertBanner variant="info" title="Could not read Loggings" message="Radar can't tell whether this ClusterFlow is in a Logging's control namespace, so whether the operator counts it is unknown." />}
    {notCounted && <AlertBanner variant="warning" title="Not counted: outside the Logging control namespace" message="The Logging operator only processes ClusterFlows in a Logging's control namespace; this one forwards nothing." />}
    <Problems data={data} />
    <Section title={cluster ? 'Cluster flow' : 'Flow'} defaultExpanded>
      <PropertyList>
        <StatusProperty data={data} />
        <Property label="Scope" value={cluster ? 'All namespaces (rules may narrow it)' : `Pods in ${ns}`} />
        <Property label="Filters" value={filters.length > 0 ? `${filters.length} (records may be changed or dropped)` : 'None'} />
      </PropertyList>
    </Section>
    <Section title={`Selection rules (${rules.length})`} defaultExpanded>
      {rules.length === 0
        ? <p className="text-sm text-theme-text-secondary">No rules: every pod in scope is selected.</p>
        : <ol className="space-y-1 text-sm">
            {rules.map((r, i) => <li key={i} className="flex flex-wrap items-baseline gap-2">
              <Badge severity={r.exclude ? 'warning' : 'success'} size="sm">{r.exclude ? 'exclude' : 'select'}</Badge>
              <span className="text-theme-text-primary">{r.conditions.length > 0 ? r.conditions.join(' and ') : 'everything'}</span>
            </li>)}
          </ol>}
      <p className="mt-2 text-xs text-theme-text-tertiary">Per container, the first rule whose conditions all hold decides; no matching rule means not selected.</p>
    </Section>
    <Section title="Outputs" defaultExpanded>
      {globalRefs.length + localRefs.length === 0
        ? <p className="text-sm text-theme-text-secondary">No outputs: selected logs go nowhere.</p>
        : <ul className="space-y-1 text-sm">
            {globalRefs.map((name) => <li key={`g-${name}`}><ResourceLink name={name} kind="clusteroutputs" namespace={cluster ? ns : (controlNamespaces?.[0] ?? '')} group={LOGGING_GROUP} label={`ClusterOutput ${name}`} onNavigate={onNavigate} /></li>)}
            {localRefs.map((name) => <li key={`l-${name}`}><ResourceLink name={name} kind="outputs" namespace={ns} group={LOGGING_GROUP} label={`Output ${name}`} onNavigate={onNavigate} /></li>)}
          </ul>}
    </Section>
  </>
}

export function LoggingOutputRenderer({ data }: Props) {
  const type = getLoggingOutputType(data)
  const address = getLoggingOutputAddress(data)
  return <>
    <Problems data={data} />
    <Section title={data.kind === 'ClusterOutput' ? 'Cluster output' : 'Output'} defaultExpanded>
      <PropertyList>
        <StatusProperty data={data} />
        <Property label="Type" value={type ?? 'Not set'} />
        {address && <Property label="Address" value={address} />}
        {data.spec?.loggingRef && <Property label="Logging ref" value={data.spec.loggingRef} />}
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">Other output settings are not shown here; the YAML tab shows them to people allowed to read this object.</p>
    </Section>
  </>
}
