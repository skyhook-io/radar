import { KeyRound, Server, Cloud, Shield } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ResourceLink } from '../../ui/drawer-components'
import {
  getTriggerAuthSecretRefs,
  getTriggerAuthEnvVars,
} from '../resource-utils-keda'

interface KedaTriggerAuthRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function KedaTriggerAuthRenderer({ data, onNavigate }: KedaTriggerAuthRendererProps) {
  const spec = data.spec || {}
  const conditions = data.status?.conditions || []

  const secretRefs = getTriggerAuthSecretRefs(data)
  const envVars = getTriggerAuthEnvVars(data)
  const vault = spec.hashiCorpVault
  const azureKeyVault = spec.azureKeyVault
  const awsSecretManager = spec.awsSecretManager
  const podIdentity = spec.podIdentity

  return (
    <>
      {/* Pod Identity */}
      {podIdentity && (
        <Section title="Pod Identity" icon={Shield}>
          <PropertyList>
            <Property label="Provider" value={podIdentity.provider || '-'} />
            {podIdentity.identityId && (
              <Property label="Identity ID" value={podIdentity.identityId} />
            )}
            {podIdentity.identityOwner && (
              <Property label="Identity Owner" value={podIdentity.identityOwner} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Secret References */}
      {secretRefs.length > 0 && (
        <Section title={`Secret References (${secretRefs.length})`} icon={KeyRound} defaultExpanded>
          <div className="space-y-2">
            {secretRefs.map((ref, i) => (
              <div key={i} className="card-inner">
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                  <span className="text-theme-text-primary font-medium">{ref.parameter}</span>
                  <span className="text-theme-text-secondary">
                    Secret: <ResourceLink name={ref.name} kind="secrets" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} /> / {ref.key}
                  </span>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Environment Variables */}
      {envVars.length > 0 && (
        <Section title={`Environment Variables (${envVars.length})`} defaultExpanded>
          <div className="space-y-2">
            {envVars.map((env, i) => (
              <div key={i} className="card-inner">
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                  <span className="text-theme-text-primary font-medium">{env.parameter}</span>
                  <span className="text-theme-text-secondary">Env: {env.name}</span>
                  {env.containerName && (
                    <span className="text-theme-text-tertiary">Container: {env.containerName}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* HashiCorp Vault */}
      {vault && (
        <Section title="HashiCorp Vault" icon={Server}>
          <PropertyList>
            <Property label="Address" value={vault.address || '-'} />
            {vault.authentication && (
              <Property label="Authentication" value={vault.authentication} />
            )}
            {vault.role && <Property label="Role" value={vault.role} />}
            {vault.mount && <Property label="Mount" value={vault.mount} />}
          </PropertyList>
          {vault.secrets && vault.secrets.length > 0 && (
            <div className="mt-2 space-y-1">
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Secrets</div>
              {vault.secrets.map((s: any, i: number) => (
                <div key={i} className="card-inner text-sm">
                  <span className="text-theme-text-primary font-medium">{s.parameter}</span>
                  <span className="text-theme-text-secondary ml-2">{s.path} / {s.key}</span>
                </div>
              ))}
            </div>
          )}
        </Section>
      )}

      {/* Azure Key Vault */}
      {azureKeyVault && (
        <Section title="Azure Key Vault" icon={Cloud}>
          <PropertyList>
            <Property label="Vault URI" value={azureKeyVault.vaultUri || '-'} />
            {azureKeyVault.credentials?.clientId && (
              <Property label="Client ID" value={azureKeyVault.credentials.clientId} />
            )}
          </PropertyList>
          {azureKeyVault.secrets && azureKeyVault.secrets.length > 0 && (
            <div className="mt-2 space-y-1">
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Secrets</div>
              {azureKeyVault.secrets.map((s: any, i: number) => (
                <div key={i} className="card-inner text-sm">
                  <span className="text-theme-text-primary font-medium">{s.parameter}</span>
                  <span className="text-theme-text-secondary ml-2">{s.name}</span>
                </div>
              ))}
            </div>
          )}
        </Section>
      )}

      {/* AWS Secrets Manager */}
      {awsSecretManager && (
        <Section title="AWS Secrets Manager" icon={Cloud}>
          <PropertyList>
            {awsSecretManager.region && (
              <Property label="Region" value={awsSecretManager.region} />
            )}
          </PropertyList>
          {awsSecretManager.secrets && awsSecretManager.secrets.length > 0 && (
            <div className="mt-2 space-y-1">
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Secrets</div>
              {awsSecretManager.secrets.map((s: any, i: number) => (
                <div key={i} className="card-inner text-sm">
                  <span className="text-theme-text-primary font-medium">{s.parameter}</span>
                  <span className="text-theme-text-secondary ml-2">{s.name}</span>
                </div>
              ))}
            </div>
          )}
        </Section>
      )}

      {conditions.length > 0 && <ConditionsSection conditions={conditions} />}
    </>
  )
}
