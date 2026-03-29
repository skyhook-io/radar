import { Container, Settings } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner } from '../../ui/drawer-components'
import { getRevisionStatus } from '../resource-utils-knative'

interface KnativeRevisionRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KnativeRevisionRenderer({ data }: KnativeRevisionRendererProps) {
  const status = getRevisionStatus(data)
  const spec = data.spec || {}
  const containers = spec.containers || []
  const containerConcurrency = spec.containerConcurrency
  const timeoutSeconds = spec.timeoutSeconds
  const serviceAccountName = spec.serviceAccountName

  const firstContainer = containers[0] || {}
  const image = firstContainer.image

  // Runtime status
  const actualReplicas = data.status?.actualReplicas
  const desiredReplicas = data.status?.desiredReplicas
  const routingState = data.metadata?.labels?.['serving.knative.dev/routingState']
  const imageDigest = data.status?.containerStatuses?.[0]?.imageDigest

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Revision" />

      <Section title="Overview" icon={Settings} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Image" value={image} />
          {imageDigest && <Property label="Image Digest" value={
            <span className="text-xs font-mono text-theme-text-secondary break-all">{imageDigest}</span>
          } />}
          {routingState && <Property label="Routing" value={
            <span className={clsx(
              'badge',
              routingState === 'active' ? 'bg-green-500/20 text-green-400' : 'bg-theme-hover text-theme-text-secondary'
            )}>
              {routingState}
            </span>
          } />}
          {(actualReplicas != null || desiredReplicas != null) && (
            <Property label="Replicas" value={`${actualReplicas ?? 0} / ${desiredReplicas ?? 0}`} />
          )}
          <Property label="Concurrency" value={containerConcurrency != null ? (containerConcurrency === 0 ? 'Unlimited' : String(containerConcurrency)) : undefined} />
          <Property label="Timeout" value={timeoutSeconds != null ? `${timeoutSeconds}s` : undefined} />
          <Property label="Service Account" value={serviceAccountName} />
        </PropertyList>
      </Section>

      {containers.length > 0 && (
        <Section title={`Containers (${containers.length})`} icon={Container} defaultExpanded>
          <div className="space-y-2">
            {containers.map((c: any, i: number) => (
              <div key={i} className="card-inner text-sm">
                <div className="font-medium text-theme-text-primary">{c.name || 'container'}</div>
                <div className="text-xs text-theme-text-secondary truncate" title={c.image}>{c.image}</div>
                {c.ports && c.ports.length > 0 && (
                  <div className="text-xs text-theme-text-tertiary mt-1">
                    Ports: {c.ports.map((p: any) => `${p.containerPort}/${p.protocol || 'TCP'}`).join(', ')}
                  </div>
                )}
                {c.resources && (c.resources.requests || c.resources.limits) && (
                  <div className="text-xs text-theme-text-tertiary mt-1">
                    {c.resources.requests && (
                      <span>Requests: {Object.entries(c.resources.requests).map(([k, v]) => `${k}=${v}`).join(', ')}</span>
                    )}
                    {c.resources.requests && c.resources.limits && ' | '}
                    {c.resources.limits && (
                      <span>Limits: {Object.entries(c.resources.limits).map(([k, v]) => `${k}=${v}`).join(', ')}</span>
                    )}
                  </div>
                )}
                {c.env && c.env.length > 0 && (
                  <div className="text-xs text-theme-text-tertiary mt-1">
                    Env: {c.env.length} variable{c.env.length !== 1 ? 's' : ''}
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
