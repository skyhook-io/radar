import { Users, Database } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../drawer-components'
import {
  getCNPGPoolerCluster,
  getCNPGPoolerType,
  getCNPGPoolerMode,
  getCNPGPoolerInstances,
  getCNPGPoolerParameters,
} from '../resource-utils-cnpg'

interface CNPGPoolerRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGPoolerRenderer({ data, onNavigate }: CNPGPoolerRendererProps) {
  const desired = data.spec?.instances ?? 0
  const ready = data.status?.instances ?? 0
  const isDegraded = desired > 0 && ready < desired
  const clusterName = getCNPGPoolerCluster(data)
  const parameters = getCNPGPoolerParameters(data)

  return (
    <>
      {/* Problem alerts */}
      {isDegraded && (
        <AlertBanner
          variant="warning"
          title="Pooler Degraded"
          message={`Only ${ready} of ${desired} pooler instances are ready.`}
        />
      )}

      {/* Pooler Configuration */}
      <Section title="Pooler Configuration" icon={Users} defaultExpanded>
        <PropertyList>
          <Property label="Type" value={getCNPGPoolerType(data)} />
          <Property label="Pool Mode" value={getCNPGPoolerMode(data)} />
          <Property label="Instances" value={getCNPGPoolerInstances(data)} />
        </PropertyList>
      </Section>

      {/* Cluster Reference */}
      <Section title="Cluster" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Cluster" value={(() => {
            if (clusterName && clusterName !== '-') {
              return (
                <ResourceLink
                  name={clusterName}
                  kind="clusters"
                  namespace={data.metadata?.namespace || ''}
                  group="postgresql.cnpg.io"
                  onNavigate={onNavigate}
                />
              )
            }
            return clusterName
          })()} />
        </PropertyList>
      </Section>

      {/* PgBouncer Parameters */}
      {Object.keys(parameters).length > 0 && (
        <Section title="PgBouncer Parameters" defaultExpanded>
          <div className="space-y-0.5">
            {Object.entries(parameters).map(([key, value]) => (
              <div key={key} className="flex items-center gap-2 text-xs">
                <span className="text-theme-text-secondary font-mono shrink-0">{key}</span>
                <span className="text-theme-text-tertiary">=</span>
                <span className="text-theme-text-primary font-mono break-all">{value}</span>
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
