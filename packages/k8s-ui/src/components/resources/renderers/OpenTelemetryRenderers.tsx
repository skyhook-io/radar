import { useMemo, useState } from 'react'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'
import { getCollectorPipelines, getOpenTelemetryCollectorStatus, getOpenTelemetryInstrumentationStatus } from '../resource-utils-opentelemetry'

export function OpenTelemetryCollectorRenderer({ data }: { data: any }) {
  const state = getOpenTelemetryCollectorStatus(data)
  const preview = useMemo(() => getCollectorPipelines(data), [data])
  const [showAll, setShowAll] = useState(false)
  const ready = data.status?.conditions?.find((c: any) => c.type === 'Ready')
  const components = (names: string[], separator = ', ') => names.slice(0, 10).map(name => `${name}${preview.connectors.includes(name) ? ' (connector)' : ''}`).join(separator) + (names.length > 10 ? `; ${names.length - 10} more in YAML` : '') || 'None'
  return <>
    <Section title="Collector" defaultExpanded>
      <PropertyList>
        <Property label="Reconciliation" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Mode" value={data.spec?.mode} />
        <Property label="Management" value={data.spec?.managementState} />
        <Property label={data.spec?.mode === 'daemonset' ? 'Reported ready / scheduled Pods' : 'Reported ready / observed Pods'} value={data.status?.scale?.statusReplicas} />
        <Property label="Version" value={data.status?.version} />
        {data.status?.image && <Property label="Image" value={<span className="break-all">{data.status.image}</span>} />}
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">Reconciled means the operator applied its configuration. Pod counts are its last reported snapshot. Inspect owned workloads for current readiness; neither proves telemetry delivery.</p>
      {state.text === 'Status stale' && <p className="mt-2 text-sm text-theme-text-secondary">The reported reconciliation describes an earlier configuration generation.</p>}
      {ready?.message && <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{ready.message}</p>}
    </Section>
    <Section title={preview.error ? 'Configured Pipelines' : `Configured Pipelines (${preview.pipelines.length})`} defaultExpanded>
      {preview.error ? <p className="text-sm text-theme-text-secondary">{preview.error}</p> : preview.pipelines.length === 0 ? <p className="text-sm text-theme-text-tertiary">No pipelines are declared in this configuration.</p> : <>
        <div className="space-y-3">
          {(showAll ? preview.pipelines : preview.pipelines.slice(0, 5)).map(pipeline => <div key={pipeline.name} className="card-inner min-w-0 break-words">
            <div className="mb-2 text-sm font-medium text-theme-text-primary">{pipeline.name}</div>
            <PropertyList>
              <Property label="Receivers" value={components(pipeline.receivers)} />
              <Property label="Processors" value={components(pipeline.processors, ' → ')} />
              <Property label="Exporters" value={components(pipeline.exporters)} />
            </PropertyList>
          </div>)}
        </div>
        {preview.pipelines.length > 5 && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => setShowAll(!showAll)}>{showAll ? 'Show fewer pipelines' : `Show all ${preview.pipelines.length} pipelines`}</button>}
        <p className="mt-2 text-xs text-theme-text-tertiary">Declared component names, not observed data flow. Connectors pass data between pipelines within the collector.</p>
      </>}
    </Section>
    <ConditionsSection conditions={data.status?.conditions} getConditionTone={condition => ['Status stale', 'Unmanaged'].includes(state.text) ? 'unknown' : condition.type === 'Ready' && condition.status === 'False' ? condition.reason === 'StatusUpdateFailed' ? 'unknown' : 'fail' : 'neutral'} />
  </>
}

export function OpenTelemetryInstrumentationRenderer({ data }: { data: any }) {
  const spec = data.spec ?? {}
  const state = getOpenTelemetryInstrumentationStatus(data)
  const blocked = Object.keys(data.status?.upgradeBlockedVersions ?? {})
  const runtimeImages = ['java', 'nodejs', 'python', 'dotnet', 'go', 'apacheHttpd', 'nginx', 'php', 'ruby'].filter(language => spec[language]?.image)
  return <>
    <Section title="Auto-instrumentation Configuration" defaultExpanded>
      <PropertyList>
        <Property label="Configuration" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        {spec.exporter?.endpoint && <Property label="OTLP endpoint" value={<span className="break-all">{spec.exporter.endpoint}</span>} />}
        <Property label="Propagators" value={spec.propagators?.join(', ')} />
        <Property label="Sampler" value={spec.sampler?.type} />
        <Property label="Sampler argument" value={spec.sampler?.argument} />
        {blocked.length > 0 && <Property label="Blocked image upgrades" value={blocked.join(', ')} />}
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">These settings configure injection into annotated workloads. This resource does not report which Pods were injected or whether telemetry reaches the endpoint.</p>
      {blocked.length > 0 && <p className="mt-2 text-sm text-theme-text-secondary">The operator could not automatically upgrade the listed runtime images. Inspect status in YAML and the operator logs before choosing a supported image.</p>}
    </Section>
    {blocked.length > 0 && <Section title="Blocked Image Upgrades" defaultExpanded>
      <PropertyList>{blocked.map(language => <Property key={language} label={language} value={<span className="whitespace-pre-wrap break-words">{String(data.status.upgradeBlockedVersions[language]).slice(0, 1000)}{String(data.status.upgradeBlockedVersions[language]).length > 1000 ? '… Inspect YAML for the full explanation.' : ''}</span>} />)}</PropertyList>
    </Section>}
    {/* Runtime image defaults are secondary to endpoint/sampler and upgrade failures. */}
    {runtimeImages.length > 0 && <Section title="Runtime Images" defaultExpanded={false}><PropertyList>
      {runtimeImages.map(language => <Property key={language} label={language} value={<span className="break-all">{spec[language].image}</span>} />)}
    </PropertyList></Section>}
  </>
}
