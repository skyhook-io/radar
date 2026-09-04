import { Activity, Server } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'

interface AnalysisTemplateRendererProps {
  data: any
}

// Every metric.provider Argo Rollouts defines. Order matches the CRD's own
// AnalysisTemplate reference doc.
const PROVIDER_KEYS = [
  'prometheus', 'web', 'kubernetes', 'job', 'wavefront', 'newRelic',
  'datadog', 'cloudWatch', 'graphite', 'influxdb', 'skywalking', 'plugin',
] as const

function detectProvider(metric: any): { name: string; details: any } | null {
  const provider = metric?.provider
  if (!provider) return null
  for (const key of PROVIDER_KEYS) {
    if (provider[key]) return { name: key, details: provider[key] }
  }
  const key = Object.keys(provider)[0]
  return key ? { name: key, details: provider[key] } : null
}

function providerLabel(name: string): string {
  switch (name) {
    case 'web': return 'Web'
    case 'newRelic': return 'New Relic'
    case 'cloudWatch': return 'CloudWatch'
    default: return name.charAt(0).toUpperCase() + name.slice(1)
  }
}

// A metric's provider config is unbounded (a full Job PodSpec, a PromQL
// query, a plugin's arbitrary args) — show the handful of fields an operator
// actually scans for per provider, not a full dump.
function ProviderDetails({ name, details }: { name: string; details: any }) {
  switch (name) {
    case 'prometheus':
      return (
        <>
          {details.address && <Property label="Address" value={details.address} />}
          {details.query && <Property label="Query" value={<code className="text-xs">{details.query}</code>} />}
        </>
      )
    case 'web':
      return (
        <>
          {details.url && <Property label="URL" value={details.url} />}
          {details.jsonPath && <Property label="JSON Path" value={<code className="text-xs">{details.jsonPath}</code>} />}
          {details.method && <Property label="Method" value={details.method} />}
        </>
      )
    case 'job':
      return (
        <Property
          label="Job Image"
          value={details.spec?.template?.spec?.containers?.[0]?.image}
        />
      )
    case 'kubernetes':
      return <Property label="Function" value={details.function} />
    case 'datadog':
      return details.query ? <Property label="Query" value={<code className="text-xs">{details.query}</code>} /> : null
    case 'newRelic':
      return details.query ? <Property label="Query" value={<code className="text-xs">{details.query}</code>} /> : null
    case 'graphite':
      return (
        <>
          {details.address && <Property label="Address" value={details.address} />}
          {details.query && <Property label="Query" value={<code className="text-xs">{details.query}</code>} />}
        </>
      )
    case 'influxdb':
      return details.query ? <Property label="Query" value={<code className="text-xs">{details.query}</code>} /> : null
    case 'wavefront':
      return details.query ? <Property label="Query" value={<code className="text-xs">{details.query}</code>} /> : null
    case 'plugin':
      return null
    default:
      return null
  }
}

// Pure spec renderer for AnalysisTemplate / ClusterAnalysisTemplate — no live
// status to show (that's the AnalysisRun this template gets instantiated
// into), just the metric definitions and args an operator configured.
export function AnalysisTemplateRenderer({ data }: AnalysisTemplateRendererProps) {
  const spec = data?.spec || {}
  const metrics: any[] = spec.metrics || []
  const args: any[] = spec.args || []

  return (
    <>
      {metrics.length > 0 && (
        <Section title={`Metrics (${metrics.length})`} icon={Activity} defaultExpanded>
          <div className="space-y-2">
            {metrics.map((metric: any) => {
              const provider = detectProvider(metric)
              return (
                <div key={metric.name} className="card-inner space-y-1.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-sm text-theme-text-primary">{metric.name}</span>
                    {provider && <Badge severity="neutral" size="sm">{providerLabel(provider.name)}</Badge>}
                  </div>
                  <PropertyList>
                    {metric.interval && <Property label="Interval" value={metric.interval} />}
                    {metric.count != null && <Property label="Count" value={metric.count} />}
                    {metric.initialDelay && <Property label="Initial Delay" value={metric.initialDelay} />}
                    {metric.successCondition && (
                      <Property label="Success if" value={<code className="text-xs">{metric.successCondition}</code>} />
                    )}
                    {metric.failureCondition && (
                      <Property label="Fail if" value={<code className="text-xs">{metric.failureCondition}</code>} />
                    )}
                    {metric.failureLimit != null && <Property label="Failure Limit" value={metric.failureLimit} />}
                    {metric.inconclusiveLimit != null && <Property label="Inconclusive Limit" value={metric.inconclusiveLimit} />}
                    {metric.consecutiveErrorLimit != null && (
                      <Property label="Consecutive Error Limit" value={metric.consecutiveErrorLimit} />
                    )}
                    {provider && <ProviderDetails name={provider.name} details={provider.details} />}
                  </PropertyList>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {args.length > 0 && (
        <Section title={`Arguments (${args.length})`} icon={Server}>
          <PropertyList>
            {args.map((arg: any) => (
              <Property
                key={arg.name}
                label={arg.name}
                value={arg.value ?? (arg.valueFrom ? 'from reference' : undefined)}
              />
            ))}
          </PropertyList>
        </Section>
      )}

      {metrics.length === 0 && args.length === 0 && (
        <div className="p-4 text-sm text-theme-text-tertiary">This template declares no metrics.</div>
      )}
    </>
  )
}
