import { Lock, Key, FileText, Server } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList, AlertBanner, ResourceLink, useOperationalIssuesShown } from '../../ui/drawer-components'

export interface SealedSecretsController {
  installed: boolean
  controller?: { kind: string; namespace: string; name: string; ready: boolean }
  controllerState: 'ok' | 'denied' | 'notFound' | 'notCollected'
  keys?: { name: string; createdAt: string; notBefore?: string; notAfter?: string; active: boolean; notUsed?: boolean }[]
  keysState: 'ok' | 'denied' | 'notCollected' | 'loading' | 'failed' | 'unknown'
  counts?: { total: number; synced: number; notSynced: number; partial: boolean }
}

interface SealedSecretRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  /** Host-fetched controller status; undefined hides the section, null means the fetch failed. */
  controller?: SealedSecretsController | null
}

const day = (value?: string) => (value ? value.slice(0, 10) : '-')

// The Secrets namespace the keys live in: the controller's. A denied
// controller hides its name, so the key messages fall back to generic text.
function ControllerSection({ controller, isSynced, onNavigate }: { controller: SealedSecretsController | null; isSynced: boolean; onNavigate?: SealedSecretRendererProps['onNavigate'] }) {
  if (!controller) {
    return (
      <Section title="Controller and sealing keys" icon={Server} defaultExpanded={!isSynced}>
        <div className="text-sm text-theme-text-tertiary">Controller status could not be loaded.</div>
      </Section>
    )
  }
  const ref = controller.controller
  const ns = ref?.namespace
  const where = ns ? ` in ${ns}` : " in the controller's namespace"
  const controllerValue =
    controller.controllerState === 'ok' && ref ? (
      <span className="inline-flex items-center gap-2">
        <ResourceLink name={ref.name} kind="deployments" namespace={ref.namespace} onNavigate={onNavigate} />
        <Badge severity={ref.ready ? 'success' : 'error'} size="sm">{ref.ready ? 'Ready' : 'Not ready'}</Badge>
      </span>
    ) : controller.controllerState === 'denied' ? "No access to the controller's Deployment"
      : controller.controllerState === 'notCollected' ? 'Radar does not collect Deployments'
      : 'No sealed-secrets controller found'
  const counts = controller.counts
  const fetchCert = ref
    ? `kubeseal --controller-namespace ${ref.namespace} --controller-name ${ref.name} --fetch-cert`
    : 'kubeseal --fetch-cert'
  return (
    <Section title="Controller and sealing keys" icon={Server} defaultExpanded={!isSynced}>
      <PropertyList>
        <Property label="Controller" value={controllerValue} />
        {counts && (
          <Property
            label="SealedSecrets"
            value={`${counts.synced} synced · ${counts.notSynced} not synced${counts.total > counts.synced + counts.notSynced ? ` · ${counts.total - counts.synced - counts.notSynced} without a Synced condition` : ''}${counts.partial ? ' in namespaces you can list' : ''}`}
          />
        )}
      </PropertyList>
      {controller.keysState === 'ok' && controller.keys && controller.keys.length > 0 && (
        <table className="mt-3 w-full text-xs">
          <thead>
            <tr className="text-left text-theme-text-tertiary">
              <th className="py-1 pr-3 font-medium">Sealing key</th>
              <th className="py-1 pr-3 font-medium">Valid from</th>
              <th className="py-1 pr-3 font-medium">Valid until</th>
              <th className="py-1 font-medium" />
            </tr>
          </thead>
          <tbody>
            {controller.keys.map((k) => (
              <tr key={k.name} className="border-t border-theme-border">
                <td className="py-1 pr-3 text-theme-text-primary break-all">{k.name}</td>
                <td className="py-1 pr-3 text-theme-text-secondary">{day(k.notBefore || k.createdAt)}</td>
                <td className="py-1 pr-3 text-theme-text-secondary">{day(k.notAfter)}</td>
                <td className="py-1">
                  {k.active && <Badge severity="info" size="sm">Seals new secrets</Badge>}
                  {k.notUsed && <Badge severity="neutral" size="sm">Not used</Badge>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {controller.keysState === 'denied' && <div className="mt-2 text-sm text-theme-text-tertiary">No access to Secrets{where}, so the sealing keys are not shown.</div>}
      {controller.keysState === 'ok' && controller.keys?.length === 0 && <div className="mt-2 text-sm text-theme-text-tertiary">No sealing keys found{where}.</div>}
      {controller.keysState === 'loading' && <div className="mt-2 text-sm text-theme-text-tertiary">Sealing keys are still loading.</div>}
      {controller.keysState === 'failed' && <div className="mt-2 text-sm text-theme-text-tertiary">Radar could not load Secrets{where}, so the sealing keys are not shown.</div>}
      {controller.keysState === 'notCollected' && <div className="mt-2 text-sm text-theme-text-tertiary">Radar does not collect Secrets{where}, so the sealing keys are not shown.</div>}
      {!isSynced && (
        <div className="mt-3 text-xs text-theme-text-secondary">
          To re-seal for this cluster, <code className="rounded bg-theme-elevated px-1 py-0.5 font-mono">{fetchCert}</code> prints its current certificate.
        </div>
      )}
    </Section>
  )
}

function getScope(annotations: Record<string, string> | undefined): string {
  if (!annotations) return 'strict'
  if (annotations['sealedsecrets.bitnami.com/cluster-wide'] === 'true') return 'cluster-wide'
  if (annotations['sealedsecrets.bitnami.com/namespace-wide'] === 'true') return 'namespace-wide'
  return 'strict'
}

export function SealedSecretRenderer({ data, onNavigate, controller }: SealedSecretRendererProps) {
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
  const operationalIssuesShown = useOperationalIssuesShown()

  const hasTemplateMetadata =
    (templateLabels && Object.keys(templateLabels).length > 0) ||
    (templateAnnotations && Object.keys(templateAnnotations).length > 0)

  return (
    <>
      {/* Problem detection alert */}
      {isNotSynced && !operationalIssuesShown && (
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
                  ? 'status-green'
                  : isNotSynced
                    ? 'status-red'
                    : 'status-amber'
              )}>
                {isSynced ? 'Synced' : isNotSynced ? 'Not Synced' : 'Unknown'}
              </span>
            }
          />
          <Property label="Target Secret" value={
            <ResourceLink
              name={data.spec?.template?.metadata?.name || data.metadata?.name || ''}
              kind="secrets"
              namespace={data.metadata?.namespace || ''}
              onNavigate={onNavigate}
            />
          } />
          <Property label="Secret Type" value={secretType} />
          <Property label="Scope" value={scope} />
          <Property label="Observed Gen" value={status.observedGeneration} />
        </PropertyList>
      </Section>

      {controller !== undefined && <ControllerSection controller={controller} isSynced={isSynced} onNavigate={onNavigate} />}

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
