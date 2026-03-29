import { UserCog, Key, Cloud } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface ServiceAccountRendererProps {
  data: any
}

export function ServiceAccountRenderer({ data }: ServiceAccountRendererProps) {
  const metadata = data.metadata || {}
  const annotations = metadata.annotations || {}
  const secrets = data.secrets || []
  const imagePullSecrets = data.imagePullSecrets || []

  // automountServiceAccountToken defaults to true if not set
  const automountToken = data.automountServiceAccountToken !== false

  // Workload identity annotations
  const gcpServiceAccount = annotations['iam.gke.io/gcp-service-account']
  const awsRoleArn = annotations['eks.amazonaws.com/role-arn']
  const azureClientId = annotations['azure.workload.identity/client-id']
  const hasWorkloadIdentity = gcpServiceAccount || awsRoleArn || azureClientId

  return (
    <>
      {/* Configuration */}
      <Section title="Configuration" icon={UserCog}>
        <PropertyList>
          <Property
            label="Automount Token"
            value={
              <span
                className={automountToken ? 'text-yellow-400' : 'text-green-400'}
                title={automountToken ? 'Token is automatically mounted in pods' : undefined}
              >
                {automountToken ? 'Yes' : 'No'}
              </span>
            }
          />
        </PropertyList>
      </Section>

      {/* Workload Identity */}
      {hasWorkloadIdentity && (
        <Section title="Workload Identity" icon={Cloud}>
          <PropertyList>
            <Property label="GCP Service Account" value={gcpServiceAccount} />
            <Property label="AWS Role ARN" value={awsRoleArn} />
            <Property label="Azure Client ID" value={azureClientId} />
          </PropertyList>
        </Section>
      )}

      {/* Secrets */}
      {secrets.length > 0 && (
        <Section title={`Secrets (${secrets.length})`} icon={Key}>
          <PropertyList>
            {secrets.map((secret: any) => (
              <Property key={secret.name} label="Secret" value={secret.name} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Image Pull Secrets */}
      {imagePullSecrets.length > 0 && (
        <Section title={`Image Pull Secrets (${imagePullSecrets.length})`}>
          <div className="flex flex-wrap gap-1">
            {imagePullSecrets.map((secret: any) => (
              <span
                key={secret.name}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {secret.name}
              </span>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
