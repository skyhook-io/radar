import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import { K3S_HELM_GROUP, getK3sHelmChartStatus, k3sHelmChartSource, k3sHelmTargetNamespace, k3sValuesSummary } from '../resource-utils-k3s'

interface Props { data: any; onNavigate?: (ref: ResourceRef) => void }

function Values({ spec }: { spec: any }) {
  const { lines, setKeys } = k3sValuesSummary(spec)
  return (
    <Section title="Values" defaultExpanded={true}>
      <PropertyList>
        <Property label="valuesContent" value={lines ? `${lines} lines` : 'none'} />
        {setKeys.length > 0 && <Property label="set" value={setKeys.join(', ')} />}
      </PropertyList>
      {(lines > 0 || setKeys.length > 0) && <div className="mt-2 text-xs text-theme-text-tertiary">Values can hold credentials, so they are not shown here; see the YAML tab.</div>}
    </Section>
  )
}

export function K3sHelmChartRenderer({ data, onNavigate, onOpenHelmRelease }: Props & { onOpenHelmRelease?: (ref: { namespace: string; name: string }) => void }) {
  const status = getK3sHelmChartStatus(data)
  const source = k3sHelmChartSource(data)
  const namespace = data.metadata?.namespace ?? ''
  const target = k3sHelmTargetNamespace(data)
  const name = data.metadata?.name ?? ''
  const jobName = data.status?.jobName
  const failed = (data.status?.conditions ?? []).find((c: any) => c?.type === 'Failed' && c?.status === 'True')
  const operationalIssuesShown = useOperationalIssuesShown()
  const managedBy = data.metadata?.annotations?.['helmcharts.cattle.io/managed-by']
  return (
    <>
      {failed && !operationalIssuesShown && <AlertBanner variant="error" title="HelmChart install failed" message={failed.message || 'The helm-install Job failed; see its logs.'} />}
      <Section title="Status" defaultExpanded={true}>
        <PropertyList>
          <Property label="State" value={<Badge severity={healthToSeverity(status.level)}>{status.text}</Badge>} />
          {data.spec?.failurePolicy && <Property label="Failure policy" value={data.spec.failurePolicy} />}
        </PropertyList>
      </Section>
      <Section title="Chart" defaultExpanded={true}>
        <PropertyList>
          <Property label="Chart" value={source.chart || '-'} />
          {source.bundled && <Property label="Source" value="Bundled with k3s" />}
          {source.version && <Property label="Version" value={source.version} />}
          {source.repo && <Property label="Repository" value={source.repo} />}
        </PropertyList>
      </Section>
      <Section title="Target" defaultExpanded={true}>
        <PropertyList>
          <Property label="Namespace" value={target} />
          <Property label="Release" value={name} />
        </PropertyList>
        {onOpenHelmRelease && (
          <button type="button" className="mt-2 text-sm text-accent hover:underline" onClick={() => onOpenHelmRelease({ namespace: target, name })}>
            Open Helm release
          </button>
        )}
      </Section>
      {jobName && (
        <Section title="Install job" defaultExpanded={true}>
          <ResourceLink name={jobName} kind="jobs" namespace={namespace} onNavigate={onNavigate} />
        </Section>
      )}
      <Values spec={data.spec} />
      {managedBy && (
        <Section title="Managed by" defaultExpanded={true}>
          <div className="text-sm text-theme-text-secondary">{managedBy}</div>
        </Section>
      )}
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}

export function K3sHelmChartConfigRenderer({ data, onNavigate }: Props) {
  const namespace = data.metadata?.namespace ?? ''
  const name = data.metadata?.name ?? ''
  return (
    <>
      <Section title="Target HelmChart" defaultExpanded={true}>
        <ResourceLink name={name} kind="helmcharts" namespace={namespace} group={K3S_HELM_GROUP} onNavigate={onNavigate} />
        <div className="mt-1 text-xs text-theme-text-tertiary">A HelmChartConfig overrides the values of the HelmChart with the same name and namespace.</div>
      </Section>
      <Values spec={data.spec} />
      {data.spec?.failurePolicy && (
        <Section title="Failure policy" defaultExpanded={true}>
          <div className="text-sm text-theme-text-secondary">{data.spec.failurePolicy}</div>
        </Section>
      )}
    </>
  )
}
