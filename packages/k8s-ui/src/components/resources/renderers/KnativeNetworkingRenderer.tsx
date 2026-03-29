import { Network, ShieldCheck, Server, Globe } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface RendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// ============================================================================
// KnativeIngress (Ingress from networking.internal.knative.dev)
// ============================================================================

export function KnativeIngressRenderer({ data }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const spec = data.spec || {}
  const rules = spec.rules || []
  const ingressClass = data.metadata?.annotations?.['networking.knative.dev/ingress.class']
  const httpOption = spec.httpOption

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Ingress" />

      <Section title="Overview" icon={Network} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Ingress Class" value={ingressClass} />
          <Property label="HTTP" value={httpOption} />
        </PropertyList>
      </Section>

      {rules.length > 0 && (
        <Section title={`Rules (${rules.length})`} defaultExpanded>
          <div className="space-y-3">
            {rules.map((rule: any, i: number) => (
              <div key={i} className="card-inner-lg">
                <div className="flex flex-wrap gap-1 mb-2">
                  {(rule.hosts || []).map((host: string, hi: number) => (
                    <span key={hi} className="badge-sm bg-blue-500/10 text-blue-400">
                      {host}
                    </span>
                  ))}
                  {rule.visibility && (
                    <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary">
                      {rule.visibility}
                    </span>
                  )}
                </div>
                {rule.http?.paths && rule.http.paths.length > 0 && (
                  <div className="space-y-1.5">
                    {rule.http.paths.map((path: any, pi: number) => (
                      <div key={pi} className="text-xs">
                        <span className="text-theme-text-tertiary">
                          {path.path || '/'}
                        </span>
                        {path.splits && path.splits.length > 0 && (
                          <div className="ml-3 mt-1 space-y-0.5">
                            {path.splits.map((split: any, si: number) => (
                              <div key={si} className="text-theme-text-secondary">
                                {split.serviceName}:{split.servicePort}
                                {split.percent !== undefined && (
                                  <span className="text-theme-text-tertiary ml-1">({split.percent}%)</span>
                                )}
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    ))}
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

// ============================================================================
// KnativeCertificate (Certificate from networking.internal.knative.dev)
// ============================================================================

export function KnativeCertificateRenderer({ data }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const spec = data.spec || {}
  const dnsNames = spec.dnsNames || []
  const secretName = spec.secretName || data.status?.http01Challenges?.[0]?.secretName

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Certificate" />

      <Section title="Overview" icon={ShieldCheck} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Secret Name" value={secretName} />
          <Property label="Domain" value={spec.domain} />
        </PropertyList>
      </Section>

      {dnsNames.length > 0 && (
        <Section title={`DNS Names (${dnsNames.length})`} defaultExpanded>
          <div className="flex flex-wrap gap-1">
            {dnsNames.map((name: string, i: number) => (
              <span key={i} className="badge-sm bg-theme-elevated text-theme-text-secondary">
                {name}
              </span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// ServerlessService (from networking.internal.knative.dev)
// ============================================================================

export function ServerlessServiceRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const mode = spec.mode
  const numActivators = spec.numActivators
  const protocolType = spec.protocolType
  const objectRef = spec.objectRef
  const privateServiceName = data.status?.privateServiceName
  const serviceName = data.status?.serviceName

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="ServerlessService" />

      <Section title="Overview" icon={Server} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Mode" value={mode ? (
            <span className={clsx(
              'badge',
              mode === 'Proxy' ? 'bg-yellow-500/20 text-yellow-400' : 'bg-green-500/20 text-green-400'
            )}>
              {mode}
            </span>
          ) : undefined} />
          <Property label="Activators" value={numActivators != null ? String(numActivators) : undefined} />
          <Property label="Protocol" value={protocolType} />
          {objectRef && (
            <Property label="Target" value={
              <ResourceLink
                name={objectRef.name}
                kind={kindToPlural(objectRef.kind || 'Deployment')}
                namespace={ns}
                onNavigate={onNavigate}
              />
            } />
          )}
          {serviceName && <Property label="Public Service" value={serviceName} />}
          {privateServiceName && <Property label="Private Service" value={privateServiceName} />}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// DomainMapping (serving.knative.dev/v1beta1)
// ============================================================================

export function DomainMappingRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const url = data.status?.url
  const ref = data.spec?.ref
  const tlsSecret = data.spec?.tls?.secretName

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="DomainMapping" />

      <Section title="Overview" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="URL" value={url ? (
            <a href={url} target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:text-blue-300 break-all">
              {url}
            </a>
          ) : undefined} />
          {ref && (
            <Property label="Target" value={
              <ResourceLink
                name={ref.name}
                kind={kindToPlural(ref.kind || 'Service')}
                namespace={ns}
                onNavigate={onNavigate}
              />
            } />
          )}
          {tlsSecret && <Property label="TLS Secret" value={tlsSecret} />}
        </PropertyList>
      </Section>

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
