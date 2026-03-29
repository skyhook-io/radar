import { Lock, Key, FileText } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList, AlertBanner } from '../../ui/drawer-components'

interface SealedSecretRendererProps {
  data: any
}

function getScope(annotations: Record<string, string> | undefined): string {
  if (!annotations) return 'strict'
  if (annotations['sealedsecrets.bitnami.com/cluster-wide'] === 'true') return 'cluster-wide'
  if (annotations['sealedsecrets.bitnami.com/namespace-wide'] === 'true') return 'namespace-wide'
  return 'strict'
}

export function SealedSecretRenderer({ data }: SealedSecretRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const annotations = data.metadata?.annotations || {}

  const encryptedData = spec.encryptedData || {}
  const encryptedKeys = Object.keys(encryptedData)
  const template = spec.template || {}
  const templateMetadata = template.metadata || {}
  const templateLabels = templateMetadata.labels
  const templateAnnotations = templateMetadata.annotations
  const secretType = template.type || 'Opaque'
  const scope = getScope(annotations)

  const syncedCond = conditions.find((c: any) => c.type === 'Synced')
  const isSynced = syncedCond?.status === 'True'
  const isNotSynced = syncedCond?.status === 'False'

  const hasTemplateMetadata =
    (templateLabels && Object.keys(templateLabels).length > 0) ||
    (templateAnnotations && Object.keys(templateAnnotations).length > 0)

  return (
    <>
      {/* Problem detection alert */}
      {isNotSynced && (
        <AlertBanner
          variant="error"
          title="Secret is not synced"
          message={<>{syncedCond.reason && <span className="font-medium">{syncedCond.reason}: </span>}{syncedCond.message || 'The sealed secret failed to unseal and sync to a Secret.'}</>}
        />
      )}

      {/* Status */}
      <Section title="Status" icon={Lock}>
        <PropertyList>
          <Property
            label="Synced"
            value={
              <span className={clsx(
                'badge',
                isSynced
                  ? 'bg-green-500/20 text-green-400'
                  : isNotSynced
                    ? 'bg-red-500/20 text-red-400'
                    : 'bg-yellow-500/20 text-yellow-400'
              )}>
                {isSynced ? 'Synced' : isNotSynced ? 'Not Synced' : 'Unknown'}
              </span>
            }
          />
          <Property label="Secret Type" value={secretType} />
          <Property label="Scope" value={scope} />
          <Property label="Observed Gen" value={status.observedGeneration} />
        </PropertyList>
      </Section>

      {/* Encrypted Keys */}
      <Section title={`Encrypted Keys (${encryptedKeys.length})`} icon={Key}>
        {encryptedKeys.length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {encryptedKeys.map((key) => (
              <span
                key={key}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {key}
              </span>
            ))}
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">No encrypted keys</div>
        )}
      </Section>

      {/* Template Metadata */}
      {hasTemplateMetadata && (
        <Section title="Template Metadata" icon={FileText}>
          {templateLabels && Object.keys(templateLabels).length > 0 && (
            <div className="mb-3">
              <div className="text-xs text-theme-text-tertiary mb-1">Labels</div>
              <KeyValueBadgeList items={templateLabels} />
            </div>
          )}
          {templateAnnotations && Object.keys(templateAnnotations).length > 0 && (
            <div>
              <div className="text-xs text-theme-text-tertiary mb-1">Annotations</div>
              <div className="space-y-1 max-h-48 overflow-y-auto">
                {Object.entries(templateAnnotations).map(([k, v]) => (
                  <div key={k} className="text-xs">
                    <span className="text-theme-text-tertiary">{k}:</span>
                    <span className="text-theme-text-secondary ml-1 break-all">{v as string}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
