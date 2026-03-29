import { Globe, Layers, Container } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface KnativeServiceRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KnativeServiceRenderer({ data, onNavigate }: KnativeServiceRendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const url = data.status?.url
  const traffic = data.status?.traffic || []
  const templateSpec = data.spec?.template?.spec || {}
  const containers = templateSpec.containers || []
  const templateAnnotations = data.spec?.template?.metadata?.annotations || {}

  const latestReady = data.status?.latestReadyRevisionName
  const latestCreated = data.status?.latestCreatedRevisionName
  const generation = data.metadata?.generation

  // Autoscaling config from template annotations
  const minScale = templateAnnotations['autoscaling.knative.dev/minScale']
  const maxScale = templateAnnotations['autoscaling.knative.dev/maxScale']
  const concurrency = templateSpec.containerConcurrency
  const timeoutSeconds = templateSpec.timeoutSeconds

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Service" />

      <Section title="Overview" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="URL" value={url ? (
            <a href={url} target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:text-blue-300 hover:underline break-all">
              {url}
            </a>
          ) : undefined} />
          <Property label="Latest Ready" value={latestReady ? (
            <ResourceLink
              name={latestReady}
              kind="revisions"
              namespace={ns}
              onNavigate={onNavigate}
            />
          ) : '-'} />
          <Property label="Latest Created" value={latestCreated ? (
            <ResourceLink
              name={latestCreated}
              kind="revisions"
              namespace={ns}
              onNavigate={onNavigate}
            />
          ) : '-'} />
          <Property label="Generation" value={generation} />
        </PropertyList>
      </Section>

      {(minScale || maxScale || concurrency != null || timeoutSeconds != null) && (
        <Section title="Scaling & Configuration" defaultExpanded>
          <PropertyList>
            {minScale && <Property label="Min Scale" value={minScale} />}
            {maxScale && <Property label="Max Scale" value={maxScale} />}
            <Property label="Concurrency" value={concurrency != null ? (concurrency === 0 ? 'Unlimited' : String(concurrency)) : undefined} />
            <Property label="Timeout" value={timeoutSeconds != null ? `${timeoutSeconds}s` : undefined} />
          </PropertyList>
        </Section>
      )}

      {traffic.length > 0 && (
        <Section title={`Traffic (${traffic.length} targets)`} icon={Layers} defaultExpanded>
          <div className="space-y-2">
            {traffic.map((t: any, i: number) => (
              <div key={i} className="flex items-center gap-2 text-sm">
                <div className="flex items-center gap-1 w-16 shrink-0">
                  <div className="flex-1 h-1.5 bg-theme-hover rounded-full overflow-hidden">
                    <div
                      className="h-full bg-blue-500 rounded-full"
                      style={{ width: `${t.percent || 0}%` }}
                    />
                  </div>
                  <span className="text-theme-text-secondary font-medium text-xs">{t.percent || 0}%</span>
                </div>
                {t.revisionName ? (
                  <ResourceLink
                    name={t.revisionName}
                    kind="revisions"
                    namespace={ns}
                    onNavigate={onNavigate}
                  />
                ) : (
                  <span className="text-theme-text-secondary">{t.latestRevision ? '@latest' : '-'}</span>
                )}
                {t.tag && (
                  <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary">
                    tag: {t.tag}
                  </span>
                )}
                {t.url && (
                  <a href={t.url} target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:text-blue-300 text-xs">
                    {t.tag || 'link'}
                  </a>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {containers.length > 0 && (
        <Section title="Template" icon={Container} defaultExpanded>
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
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
