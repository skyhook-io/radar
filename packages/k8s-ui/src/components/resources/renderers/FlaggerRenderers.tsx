import { useState } from 'react'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import { getFlaggerAnalysis, getFlaggerCanaryStatus, getFlaggerStrategy, getFlaggerTargetRef, getFlaggerAutoscalerRef, getFlaggerMetricTemplateRef } from '../resource-utils-flagger'

interface Props { data: any; onNavigate?: (ref: ResourceRef) => void }

export function FlaggerCanaryRenderer({ data, onNavigate }: Props) {
  const state = getFlaggerCanaryStatus(data)
  const analysis = getFlaggerAnalysis(data)
  const target = getFlaggerTargetRef(data)
  const scaler = getFlaggerAutoscalerRef(data)
  const strategy = getFlaggerStrategy(data)
  const weighted = !(analysis.iterations > 0)
  const metrics = analysis.metrics ?? []
  const [showAllMetrics, setShowAllMetrics] = useState(false)
  const [showAllHooks, setShowAllHooks] = useState(false)
  const operationalIssuesShown = useOperationalIssuesShown()
  return <>
    <Section title="Canary Rollout" defaultExpanded>
      <PropertyList>
        <Property label="Rollout state" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Recorded phase" value={data.status?.phase} />
        <Property label="Suspend requested" value={data.spec?.suspend === true ? 'Yes' : 'No'} />
        <Property label="Strategy" value={strategy} />
        <Property label="Canary target" value={target ? <ResourceLink {...target} onNavigate={onNavigate} /> : data.spec?.targetRef?.name} />
        {data.spec?.autoscalerRef && <Property label="Canary autoscaler" value={scaler ? <ResourceLink {...scaler} onNavigate={onNavigate} /> : data.spec.autoscalerRef.name} />}
        {weighted && <Property label="Reported canary weight" value={data.status?.canaryWeight != null ? `${data.status.canaryWeight}%` : 'Not reported'} />}
        <Property label="Reported failed checks" value={data.status?.failedChecks ?? 'Not reported'} />
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">Phase and counters describe Flagger’s last reported rollout, not a live traffic measurement or proof that the latest configuration was reconciled.</p>
      {data.spec?.targetRef?.kind === 'Deployment' && <p className="mt-2 text-xs text-theme-text-tertiary">Flagger normally scales the canary target to zero between analyses. The primary Deployment continues serving; find observed primary resources in Related Resources.</p>}
      {data.spec?.targetRef?.kind === 'DaemonSet' && <p className="mt-2 text-xs text-theme-text-tertiary">Flagger parks the canary DaemonSet with a node selector between analyses. Inspect the primary DaemonSet in Related Resources.</p>}
      {data.status?.phase === 'Failed' && !operationalIssuesShown && <AlertBanner variant="warning" title="Canary rollout failed" message="Inspect this Canary’s events for failed metric checks or webhooks; the primary may still be serving the previous version." />}
    </Section>
    <Section title="Analysis" defaultExpanded>
      <PropertyList>
        <Property label="Interval" value={analysis.interval || '1m (default)'} />
        <Property label="Failure threshold" value={analysis.threshold > 0 ? analysis.threshold : '1 (default)'} />
        <Property label="Skip analysis" value={data.spec?.skipAnalysis === true || (!data.spec?.analysis && !data.spec?.canaryAnalysis) ? 'Yes' : 'No'} />
        {weighted ? <>
          <Property label="Step weight" value={analysis.stepWeights?.length ? analysis.stepWeights.map((weight: number) => `${weight}%`).join(', ') : analysis.stepWeight != null ? `${analysis.stepWeight}%` : 'Not specified'} />
          <Property label="Maximum weight" value={analysis.maxWeight != null ? `${analysis.maxWeight}%` : 'Not specified'} />
        </> : <Property label="Configured iterations" value={analysis.iterations} />}
        {!weighted && <Property label="Reported iteration" value={data.status?.iterations ?? 'Not reported'} />}
      </PropertyList>
    </Section>
    {metrics.length > 0 && <Section title={`Analysis Metrics (${metrics.length})`} defaultExpanded>
      <div className="space-y-3">{(showAllMetrics ? metrics : metrics.slice(0, 5)).map((metric: any, index: number) => {
        const template = getFlaggerMetricTemplateRef(data, metric)
        return <div key={index} className="card-inner break-words"><PropertyList>
          <Property label="Metric" value={metric.name} />
          <Property label="Source" value={template ? <ResourceLink {...template} onNavigate={onNavigate} /> : metric.query ? 'Inline query' : ['request-success-rate', 'request-duration'].includes(metric.name) ? 'Built-in provider metric' : 'No query or template declared'} />
          <Property label="Query interval" value={metric.interval || '1m (default)'} />
          {metric.thresholdRange?.min != null && <Property label="Minimum" value={metric.thresholdRange.min} />}
          {metric.thresholdRange?.max != null && <Property label="Maximum" value={metric.thresholdRange.max} />}
          {!metric.thresholdRange && metric.threshold != null && <Property label="Threshold (deprecated)" value={metric.threshold} />}
        </PropertyList>{metric.query && !template && <div className="mt-2"><FlaggerQueryPreview query={metric.query} /></div>}</div>
      })}</div>
      {metrics.length > 5 && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => setShowAllMetrics(!showAllMetrics)}>{showAllMetrics ? 'Show fewer metrics' : `Show all ${metrics.length} metrics`}</button>}
    </Section>}
    {analysis.webhooks?.length > 0 && <Section title={`Analysis Hooks (${analysis.webhooks.length})`} defaultExpanded={['Waiting', 'WaitingPromotion'].includes(data.status?.phase)}>
      <PropertyList>{(showAllHooks ? analysis.webhooks : analysis.webhooks.slice(0, 5)).map((hook: any, index: number) => <Property key={index} label={hook.name} value={hook.type || 'rollout'} />)}</PropertyList>
      {analysis.webhooks.length > 5 && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => setShowAllHooks(!showAllHooks)}>{showAllHooks ? 'Show fewer hooks' : `Show all ${analysis.webhooks.length} hooks`}</button>}
      {data.spec?.suspend !== true && ['Waiting', 'WaitingPromotion'].includes(data.status?.phase) && <p className="mt-2 text-xs text-theme-text-tertiary">Flagger is waiting for a confirmation hook. Inspect the confirm-rollout or confirm-promotion hook and this Canary’s events.</p>}
    </Section>}
    <ConditionsSection conditions={data.status?.conditions} getConditionTone={condition => condition.type === 'Promoted' ? data.status?.phase === 'Failed' ? 'fail' : condition.status === 'True' ? 'ok' : 'neutral' : 'unknown'} />
  </>
}

export function FlaggerMetricTemplateRenderer({ data, onNavigate }: Props) {
  const provider = data.spec?.provider ?? {}
  const query = data.spec?.query ?? ''
  return <>
    <Section title="Metric Provider" defaultExpanded><PropertyList>
      <Property label="Type" value={provider.type} />
      {provider.address && <Property label="Address" value={<span className="break-all">{provider.address}</span>} />}
      {provider.region && <Property label="Region" value={provider.region} />}
      <Property label="TLS certificate verification" value={provider.insecureSkipVerify ? 'Disabled' : 'Enabled'} />
      {provider.secretRef?.name && <Property label="Credentials Secret" value={<ResourceLink name={provider.secretRef.name} kind="Secret" group="" namespace={data.metadata?.namespace} onNavigate={onNavigate} />} />}
    </PropertyList><p className="mt-2 text-xs text-theme-text-tertiary">Declared provider and query configuration. This view does not execute the query or verify provider connectivity.</p></Section>
    <Section title="Query Template" defaultExpanded>
      {query ? <FlaggerQueryPreview query={query} /> : <p className="text-sm text-theme-text-tertiary">No query is declared.</p>}
    </Section>
    <ConditionsSection conditions={data.status?.conditions} getConditionTone={() => 'unknown'} />
  </>
}

function FlaggerQueryPreview({ query }: { query: string }) {
  const [showFullQuery, setShowFullQuery] = useState(false)
  return <><pre className="whitespace-pre-wrap break-all text-xs text-theme-text-secondary">{showFullQuery ? query : query.slice(0, 2000)}</pre>
    {query.length > 2000 && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => setShowFullQuery(!showFullQuery)}>{showFullQuery ? 'Show less' : `Show full query (${query.length} characters)`}</button>}
  </>
}
