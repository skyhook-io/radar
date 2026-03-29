import { Globe, AlertTriangle } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getClusterExternalSecretStatus,
  getClusterExternalSecretProvisionedNamespaces,
  getClusterExternalSecretFailedNamespaces,
  getClusterExternalSecretNamespaces,
  getClusterExternalSecretNamespaceSelector,
} from '../resource-utils-eso'

interface ClusterExternalSecretRendererProps {
  data: any
}

export function ClusterExternalSecretRenderer({ data }: ClusterExternalSecretRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []

  const cesStatus = getClusterExternalSecretStatus(data)
  const provisionedNamespaces = getClusterExternalSecretProvisionedNamespaces(data)
  const failedNamespaces = getClusterExternalSecretFailedNamespaces(data)
  const explicitNamespaces = getClusterExternalSecretNamespaces(data)
  const namespaceSelector = getClusterExternalSecretNamespaceSelector(data)

  // ExternalSecret spec embedded in the ClusterExternalSecret
  const esSpec = data.spec?.externalSecretSpec
  const refreshInterval = esSpec?.refreshInterval || '-'
  const storeRef = esSpec?.secretStoreRef
  const dataCount = (esSpec?.data || []).length
  const dataFromCount = (esSpec?.dataFrom || []).length

  return (
    <>
      {/* Alert for failed namespaces */}
      {failedNamespaces.length > 0 && (
        <AlertBanner
          variant="error"
          title={`Failed in ${failedNamespaces.length} namespace${failedNamespaces.length > 1 ? 's' : ''}`}
          message="ExternalSecret provisioning failed in one or more namespaces."
          items={failedNamespaces.map(f => `${f.namespace}${f.reason ? `: ${f.reason}` : ''}`)}
        />
      )}

      {/* Overview */}
      <Section title="Overview" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={cesStatus.text} />
          <Property label="Provisioned" value={`${provisionedNamespaces.length} namespace${provisionedNamespaces.length !== 1 ? 's' : ''}`} />
          {failedNamespaces.length > 0 && (
            <Property label="Failed" value={`${failedNamespaces.length} namespace${failedNamespaces.length !== 1 ? 's' : ''}`} />
          )}
        </PropertyList>
      </Section>

      {/* Namespace Targeting */}
      <Section title="Namespace Selection" defaultExpanded>
        {explicitNamespaces.length > 0 ? (
          <div>
            <div className="text-xs text-theme-text-secondary mb-1.5">Explicit Namespaces</div>
            <div className="flex flex-wrap gap-1">
              {explicitNamespaces.map((ns: string) => (
                <span key={ns} className="badge bg-theme-elevated text-theme-text-primary">
                  {ns}
                </span>
              ))}
            </div>
          </div>
        ) : namespaceSelector ? (
          <div>
            <div className="text-xs text-theme-text-secondary mb-1.5">Label Selector</div>
            <div className="flex flex-wrap gap-1">
              {Object.entries(namespaceSelector).map(([k, v]) => (
                <span key={k} className="badge bg-theme-elevated text-theme-text-secondary">
                  {k}={v}
                </span>
              ))}
            </div>
          </div>
        ) : (
          <div className="text-xs text-theme-text-tertiary">All namespaces (no selector)</div>
        )}
      </Section>

      {/* Provisioned Namespaces */}
      {provisionedNamespaces.length > 0 && (
        <Section title={`Provisioned Namespaces (${provisionedNamespaces.length})`} defaultExpanded={provisionedNamespaces.length <= 10}>
          <div className="flex flex-wrap gap-1">
            {provisionedNamespaces.map((ns: string) => (
              <span key={ns} className="badge bg-green-500/10 text-green-400">
                {ns}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Failed Namespaces (detailed) */}
      {failedNamespaces.length > 0 && (
        <Section title={`Failed Namespaces (${failedNamespaces.length})`} defaultExpanded>
          <div className="space-y-1">
            {failedNamespaces.map((f, i) => (
              <div key={i} className="bg-red-500/10 rounded p-2 flex items-start gap-2">
                <AlertTriangle className="w-3.5 h-3.5 text-red-400 mt-0.5 shrink-0" />
                <div className="text-xs">
                  <span className="text-red-400 font-medium">{f.namespace}</span>
                  {f.reason && (
                    <span className="text-red-300/80 ml-1 break-all">{f.reason}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* External Secret Spec Summary */}
      <Section title="ExternalSecret Spec" defaultExpanded>
        <PropertyList>
          <Property label="Refresh Interval" value={refreshInterval} />
          {storeRef && (
            <>
              <Property label="Store Name" value={storeRef.name || '-'} />
              <Property label="Store Kind" value={storeRef.kind || 'SecretStore'} />
            </>
          )}
          <Property label="Data Mappings" value={dataCount > 0 ? String(dataCount) : '-'} />
          <Property label="Data Sources" value={dataFromCount > 0 ? String(dataFromCount) : '-'} />
          {esSpec?.target?.creationPolicy && (
            <Property label="Creation Policy" value={esSpec.target.creationPolicy} />
          )}
          {esSpec?.target?.deletionPolicy && (
            <Property label="Deletion Policy" value={esSpec.target.deletionPolicy} />
          )}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
