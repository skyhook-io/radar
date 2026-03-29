import { KeyRound, RefreshCw, CloudCog } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getExternalSecretStatus,
  getExternalSecretStore,
  getExternalSecretRefreshInterval,
  getExternalSecretLastSync,
  getExternalSecretTargetName,
  getExternalSecretTargetCreationPolicy,
  getExternalSecretTargetDeletionPolicy,
  getExternalSecretDataMappings,
  getExternalSecretDataFromSources,
} from '../resource-utils-eso'

interface ExternalSecretRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function ExternalSecretRenderer({ data, onNavigate }: ExternalSecretRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const esStatus = getExternalSecretStatus(data)
  const store = getExternalSecretStore(data)
  const dataMappings = getExternalSecretDataMappings(data)
  const dataFromSources = getExternalSecretDataFromSources(data)
  const targetTemplate = data.spec?.target?.template

  // Problem detection
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const isNotReady = readyCond?.status === 'False'

  return (
    <>
      {/* Alert if not synced */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="ExternalSecret Not Synced"
          message={readyCond?.message || 'The ExternalSecret is not in a synced state.'}
        />
      )}

      {/* Status section */}
      <Section title="Sync Status" icon={RefreshCw} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={esStatus.text} />
          <Property label="Last Sync" value={getExternalSecretLastSync(data)} />
          <Property label="Refresh Interval" value={getExternalSecretRefreshInterval(data)} />
          <Property label="Target Secret" value={(() => {
            const targetName = getExternalSecretTargetName(data)
            if (targetName && targetName !== '-') {
              return (
                <ResourceLink
                  name={targetName}
                  kind="secrets"
                  namespace={data.metadata?.namespace || ''}
                  onNavigate={onNavigate}
                />
              )
            }
            return targetName
          })()} />
          {status.syncedResourceVersion && (
            <Property label="Synced Version" value={status.syncedResourceVersion} />
          )}
          {status.binding?.name && (
            <Property label="Binding" value={status.binding.name} />
          )}
        </PropertyList>
      </Section>

      {/* Store Reference */}
      <Section title="Store Reference" icon={CloudCog} defaultExpanded>
        <PropertyList>
          <Property label="Store Name" value={(() => {
            if (store.name && store.name !== '-') {
              const storeKind = store.kind === 'ClusterSecretStore'
                ? 'clustersecretstores'
                : 'secretstores'
              return (
                <ResourceLink
                  name={store.name}
                  kind={storeKind}
                  namespace={store.kind === 'ClusterSecretStore' ? '' : (data.metadata?.namespace || '')}
                  onNavigate={onNavigate}
                />
              )
            }
            return store.name
          })()} />
          <Property label="Store Kind" value={store.kind} />
        </PropertyList>
      </Section>

      {/* Secret Mappings - data[] */}
      {dataMappings.length > 0 && (
        <Section title={`Secret Mappings (${dataMappings.length})`} icon={KeyRound} defaultExpanded>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-theme-text-secondary text-left text-xs uppercase tracking-wider">
                  <th className="pb-2 pr-3 font-medium">Secret Key</th>
                  <th className="pb-2 pr-3 font-medium">Remote Key</th>
                  <th className="pb-2 font-medium">Property</th>
                </tr>
              </thead>
              <tbody>
                {dataMappings.map((mapping, i) => (
                  <tr key={i} className="border-t border-theme-border/50">
                    <td className="py-1.5 pr-3 text-theme-text-primary font-mono text-xs">{mapping.secretKey || '-'}</td>
                    <td className="py-1.5 pr-3 text-theme-text-secondary font-mono text-xs break-all">{mapping.remoteKey || '-'}</td>
                    <td className="py-1.5 text-theme-text-secondary text-xs">
                      {mapping.remoteProperty || '-'}
                      {mapping.remoteVersion && (
                        <span className="ml-1 text-theme-text-tertiary">v{mapping.remoteVersion}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>
      )}

      {/* DataFrom sources */}
      {dataFromSources.length > 0 && (
        <Section title={`Data Sources (${dataFromSources.length})`} defaultExpanded>
          <div className="space-y-1">
            {dataFromSources.map((source, i) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 text-sm">
                  <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                    {source.type}
                  </span>
                  {source.details && (
                    <span className="text-theme-text-tertiary text-xs font-mono break-all">{source.details}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Target Configuration */}
      <Section title="Target Configuration" defaultExpanded={false}>
        <PropertyList>
          <Property label="Target Name" value={getExternalSecretTargetName(data)} />
          <Property label="Creation Policy" value={getExternalSecretTargetCreationPolicy(data)} />
          <Property label="Deletion Policy" value={getExternalSecretTargetDeletionPolicy(data)} />
          {targetTemplate?.type && (
            <Property label="Template Type" value={targetTemplate.type} />
          )}
          {targetTemplate?.engineVersion && (
            <Property label="Engine Version" value={targetTemplate.engineVersion} />
          )}
          {targetTemplate?.metadata?.labels && (
            <Property label="Template Labels" value={Object.entries(targetTemplate.metadata.labels).map(([k, v]) => `${k}=${v}`).join(', ')} />
          )}
          {targetTemplate?.metadata?.annotations && (
            <Property label="Template Annotations" value={Object.entries(targetTemplate.metadata.annotations).map(([k, v]) => `${k}=${v}`).join(', ')} />
          )}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
