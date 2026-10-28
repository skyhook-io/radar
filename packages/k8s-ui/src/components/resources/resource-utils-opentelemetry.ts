import { isApiGroup } from './resource-utils-cnpg'
import { parseDocument } from 'yaml'
import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// Ready describes reconciliation only. In the operator, Ready=True can coexist
// with 0/1 ready Pods. Never derive runtime health from this condition or scale.
export function getOpenTelemetryCollectorStatus(data: any): StatusBadge {
  const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level] })
  if (data.spec?.managementState === 'unmanaged') return badge('Unmanaged', 'neutral')
  const ready = data.status?.conditions?.find((c: any) => c.type === 'Ready')
  if (!ready) return badge('Not reported', 'unknown')
  const observed = data.status?.observedGeneration
  if (observed > 0 && observed < data.metadata?.generation) return badge('Status stale', 'unknown')
  if (ready.status === 'False') return badge(ready.reason === 'StatusUpdateFailed' ? 'Status unavailable' : 'Reconcile failed', ready.reason === 'StatusUpdateFailed' ? 'unknown' : 'unhealthy')
  if (ready.status === 'True') return badge('Reconciled', 'neutral')
  return badge('Reconciliation unknown', 'unknown')
}

export function getOpenTelemetryInstrumentationStatus(data: any): StatusBadge {
  const blocked = Object.keys(data.status?.upgradeBlockedVersions ?? {}).length > 0
  const level = 'neutral'
  return { text: blocked ? 'Upgrade blocked' : 'Configured', level, color: healthColors[level] }
}

export interface CollectorPipeline {
  name: string
  receivers: string[]
  processors: string[]
  exporters: string[]
}

export interface CollectorPipelines {
  pipelines: CollectorPipeline[]
  connectors: string[]
  error?: string
}

export function getCollectorPipelines(data: any): CollectorPipelines {
  let config = data.spec?.config
  // These are two genuinely served API versions, not a response-shape fallback.
  if (data.apiVersion === 'opentelemetry.io/v1alpha1' && typeof config === 'string') {
    if (config.length > 1_000_000) return { pipelines: [], connectors: [], error: 'Configuration is too large for a pipeline preview. Inspect YAML.' }
    try {
      const document = parseDocument(config)
      if (document.errors.length) throw new Error('Invalid YAML')
      config = document.toJS({ maxAliasCount: 100 })
    } catch {
      return { pipelines: [], connectors: [], error: 'Pipeline configuration could not be read. Inspect YAML.' }
    }
  }
  if (!config || typeof config !== 'object' || Array.isArray(config)) {
    return { pipelines: [], connectors: [], error: 'No structured pipeline configuration is available. Inspect YAML.' }
  }
  const service = config.service
  if (service !== undefined && (!service || typeof service !== 'object' || Array.isArray(service))) {
    return { pipelines: [], connectors: [], error: 'Pipeline configuration could not be read. Inspect YAML.' }
  }
  const declared = service?.pipelines
  if (declared != null && (typeof declared !== 'object' || Array.isArray(declared))) {
    return { pipelines: [], connectors: [], error: 'Pipeline configuration could not be read. Inspect YAML.' }
  }
  if (Object.values(declared ?? {}).some(pipeline => !pipeline || typeof pipeline !== 'object' || Array.isArray(pipeline))) {
    return { pipelines: [], connectors: [], error: 'Pipeline configuration could not be read. Inspect YAML.' }
  }
  const names = (value: unknown): string[] => Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : []
  return {
    pipelines: Object.entries(declared ?? {}).map(([name, pipeline]: [string, any]) => ({
      name, receivers: names(pipeline?.receivers), processors: names(pipeline?.processors), exporters: names(pipeline?.exporters),
    })),
    connectors: Object.keys(config.connectors ?? {}),
  }
}

// A single reader backs native cells, their filters and sorting.
export function getOpenTelemetryColumnValue(data: any, column: string): string | undefined {
  if (!isApiGroup(data?.apiVersion, 'opentelemetry.io')) return undefined
  if (data.kind === 'OpenTelemetryCollector') {
    switch (column) {
      case 'status': return getOpenTelemetryCollectorStatus(data).text
      case 'mode': return data.spec?.mode ?? ''
      case 'reportedPods': return data.status?.scale?.statusReplicas ?? ''
      case 'version': return data.status?.version ?? ''
      case 'image': return data.status?.image ?? ''
      case 'management': return data.spec?.managementState ?? ''
    }
  }
  if (data.kind === 'Instrumentation') {
    switch (column) {
      case 'status': return getOpenTelemetryInstrumentationStatus(data).text
      case 'endpoint': return data.spec?.exporter?.endpoint ?? ''
      case 'sampler': return data.spec?.sampler?.type ?? ''
      case 'samplerArgument': return data.spec?.sampler?.argument ?? ''
    }
  }
  return undefined
}
