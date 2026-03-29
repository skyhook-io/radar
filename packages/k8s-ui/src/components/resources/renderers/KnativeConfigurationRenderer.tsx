import { Settings, Container } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface KnativeConfigurationRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KnativeConfigurationRenderer({ data, onNavigate }: KnativeConfigurationRendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''

  const latestCreated = data.status?.latestCreatedRevisionName
  const latestReady = data.status?.latestReadyRevisionName
  const containers = data.spec?.template?.spec?.containers || []

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Configuration" />

      <Section title="Overview" icon={Settings} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Latest Created" value={latestCreated ? (
            <ResourceLink
              name={latestCreated}
              kind="revisions"
              namespace={ns}
              onNavigate={onNavigate}
            />
          ) : '-'} />
          <Property label="Latest Ready" value={latestReady ? (
            <ResourceLink
              name={latestReady}
              kind="revisions"
              namespace={ns}
              onNavigate={onNavigate}
            />
          ) : '-'} />
        </PropertyList>
      </Section>

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
