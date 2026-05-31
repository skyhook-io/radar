import { KeyRound, Settings } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { CodeViewer } from '../../ui/CodeViewer'
import { getProviderConfigStatus, getProviderConfigCredentialsSource } from '../resource-utils-crossplane'

interface CrossplaneProviderConfigRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function extractApiGroup(apiVersion?: string): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}

export function CrossplaneProviderConfigRenderer({ data, onNavigate }: CrossplaneProviderConfigRendererProps) {
  const status = getProviderConfigStatus(data)
  const credentialsSource = getProviderConfigCredentialsSource(data)
  const apiGroup = extractApiGroup(data?.apiVersion)
  const secretRef = data?.spec?.credentials?.secretRef
  const namespace = data?.metadata?.namespace || ''
  const users = typeof data?.status?.users === 'number' ? data.status.users : null

  return (
    <>
      {credentialsSource === 'None' && (
        <AlertBanner
          variant="info"
          title="No credentials configured"
          message="This ProviderConfig has credentials.source set to None — only useful for providers that auto-discover credentials (e.g. in-cluster K8s, IRSA)."
        />
      )}

      <Section title="ProviderConfig" icon={Settings} defaultExpanded>
        <PropertyList>
          <Property label="API Group" value={apiGroup || '-'} />
          <Property label="Credentials Source" value={credentialsSource} />
          {users !== null && <Property label="In Use By" value={`${users} resource${users === 1 ? '' : 's'}`} />}
          <Property label="Status" value={status.text} />
        </PropertyList>
      </Section>

      {secretRef && (
        <Section title="Credentials Secret" icon={KeyRound} defaultExpanded>
          <PropertyList>
            <Property
              label="Secret"
              value={
                <ResourceLink
                  name={secretRef.name}
                  kind="secrets"
                  namespace={secretRef.namespace || namespace}
                  onNavigate={onNavigate}
                />
              }
            />
            {secretRef.namespace && <Property label="Namespace" value={secretRef.namespace} />}
            {secretRef.key && <Property label="Key" value={<span className="inline-code">{secretRef.key}</span>} />}
          </PropertyList>
        </Section>
      )}

      {data?.spec && (
        <Section title="Spec" icon={Settings} defaultExpanded={false}>
          <CodeViewer code={JSON.stringify(data.spec, null, 2)} language="json" />
        </Section>
      )}
    </>
  )
}
