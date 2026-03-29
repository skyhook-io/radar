import { Clock, Server, Container, Link2 } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface RendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function SinkProperty({ sink, ns, onNavigate }: { sink: any; ns: string; onNavigate?: RendererProps['onNavigate'] }) {
  if (!sink) return <Property label="Sink" value="-" />

  if (sink.ref) {
    return (
      <Property label="Sink" value={
        <ResourceLink
          name={sink.ref.name}
          kind={kindToPlural(sink.ref.kind || 'Service')}
          namespace={sink.ref.namespace || ns}
          onNavigate={onNavigate}
        />
      } />
    )
  }

  if (sink.uri) {
    return <Property label="Sink URI" value={
      <span className="text-theme-text-primary break-all">{sink.uri}</span>
    } />
  }

  return <Property label="Sink" value="-" />
}

// ============================================================================
// PingSource
// ============================================================================

export function PingSourceRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="PingSource" />

      <Section title="Overview" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Schedule" value={spec.schedule} />
          <Property label="Timezone" value={spec.timezone} />
          <Property label="Content Type" value={spec.contentType} />
          <Property label="Data" value={spec.data ? (
            <span className="font-mono text-xs break-all">{spec.data.length > 200 ? spec.data.slice(0, 200) + '...' : spec.data}</span>
          ) : undefined} />
          <SinkProperty sink={spec.sink} ns={ns} onNavigate={onNavigate} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// ApiServerSource
// ============================================================================

export function ApiServerSourceRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const resources = spec.resources || []

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="ApiServerSource" />

      <Section title="Overview" icon={Server} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Mode" value={spec.mode || spec.eventMode} />
          <Property label="Service Account" value={spec.serviceAccountName} />
          <SinkProperty sink={spec.sink} ns={ns} onNavigate={onNavigate} />
        </PropertyList>
      </Section>

      {resources.length > 0 && (
        <Section title={`Watched Resources (${resources.length})`} defaultExpanded>
          <div className="space-y-1.5">
            {resources.map((r: any, i: number) => (
              <div key={i} className="card-inner text-sm">
                <span className="text-theme-text-primary">{r.apiVersion}/{r.kind}</span>
                {r.selector && (
                  <div className="text-xs text-theme-text-tertiary mt-0.5">
                    selector: {typeof r.selector === 'string' ? r.selector : JSON.stringify(r.selector.matchLabels || r.selector)}
                  </div>
                )}
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
// ContainerSource
// ============================================================================

export function ContainerSourceRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const template = spec.template?.spec || {}
  const containers = template.containers || []

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="ContainerSource" />

      <Section title="Overview" icon={Container} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <SinkProperty sink={spec.sink} ns={ns} onNavigate={onNavigate} />
        </PropertyList>
      </Section>

      {containers.length > 0 && (
        <Section title={`Containers (${containers.length})`} defaultExpanded>
          <div className="space-y-2">
            {containers.map((c: any, i: number) => (
              <div key={i} className="card-inner text-sm">
                <div className="font-medium text-theme-text-primary">{c.name || 'container'}</div>
                <div className="text-xs text-theme-text-secondary truncate" title={c.image}>{c.image}</div>
                {c.args && c.args.length > 0 && (
                  <div className="text-xs text-theme-text-tertiary mt-1 font-mono truncate">
                    $ {c.args.join(' ')}
                  </div>
                )}
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
// SinkBinding
// ============================================================================

export function SinkBindingRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const subjectRef = spec.subject

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="SinkBinding" />

      <Section title="Overview" icon={Link2} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          {subjectRef && (
            <>
              <Property label="Subject Kind" value={subjectRef.kind} />
              <Property label="Subject API" value={subjectRef.apiVersion} />
              {subjectRef.name && (
                <Property label="Subject" value={
                  <ResourceLink
                    name={subjectRef.name}
                    kind={kindToPlural(subjectRef.kind || 'Deployment')}
                    namespace={subjectRef.namespace || ns}
                    onNavigate={onNavigate}
                  />
                } />
              )}
              {subjectRef.selector && (
                <Property label="Selector" value={
                  <span className="text-xs font-mono">
                    {JSON.stringify(subjectRef.selector.matchLabels || subjectRef.selector)}
                  </span>
                } />
              )}
            </>
          )}
          <SinkProperty sink={spec.sink} ns={ns} onNavigate={onNavigate} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
