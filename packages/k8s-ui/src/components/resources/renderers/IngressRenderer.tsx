import { Globe, Shield, Clock } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'

interface IngressRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function IngressRenderer({ data, onNavigate }: IngressRendererProps) {
  const spec = data.spec || {}
  const rules = spec.rules || []
  const tls = spec.tls || []
  const lbIngress = data.status?.loadBalancer?.ingress || []

  // Check for issues
  const hasNoAddress = lbIngress.length === 0
  const hasNoClass = !spec.ingressClassName && !data.metadata?.annotations?.['kubernetes.io/ingress.class']
  const hasNoRules = rules.length === 0

  return (
    <>
      {/* No address warning */}
      {hasNoAddress && (
        <AlertBanner
          variant="warning"
          icon={Clock}
          title="Address Not Assigned"
          message={hasNoClass
            ? 'No ingress class specified — an ingress controller may not pick up this resource.'
            : 'Waiting for ingress controller to provision address. Check Events if this persists.'}
        />
      )}

      {/* No rules warning */}
      {hasNoRules && (
        <AlertBanner
          variant="info"
          title="No Routing Rules"
          message="This ingress has no rules defined. Traffic will not be routed."
        />
      )}

      <Section title="Ingress" icon={Globe}>
        <PropertyList>
          <Property label="Class" value={spec.ingressClassName || data.metadata?.annotations?.['kubernetes.io/ingress.class']} />
          {lbIngress.length > 0 && (
            <Property label="Address" value={lbIngress[0].ip || lbIngress[0].hostname} />
          )}
          <Property label="TLS" value={tls.length > 0 ? `${tls.length} certificate(s)` : 'None'} />
        </PropertyList>
      </Section>

      <Section title="Rules" defaultExpanded>
        <div className="space-y-3">
          {rules.map((rule: any, i: number) => (
            <div key={i} className="card-inner-lg">
              <div className="flex items-center gap-2 mb-2">
                {tls.some((t: any) => t.hosts?.includes(rule.host)) && (
                  <Shield className="w-3.5 h-3.5 text-green-400" />
                )}
                <span className="text-sm font-medium text-theme-text-primary">{rule.host || '*'}</span>
              </div>
              <div className="space-y-1">
                {rule.http?.paths?.map((path: any) => (
                  <div key={path.path || '/'} className="text-xs text-theme-text-secondary flex items-center gap-2">
                    <span className="text-theme-text-tertiary">{path.pathType || 'Prefix'}:</span>
                    <span>{path.path || '/'}</span>
                    <span className="text-theme-text-tertiary">→</span>
                    {path.backend?.service?.name ? (
                      <ResourceLink
                        name={path.backend.service.name}
                        kind="services"
                        namespace={data.metadata?.namespace || ''}
                        label={<span className="text-blue-400">{path.backend.service.name}:{path.backend.service.port?.number || path.backend.service.port?.name}</span>}
                        onNavigate={onNavigate}
                      />
                    ) : (
                      <span className="text-blue-400">-</span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </Section>

      {tls.length > 0 && (
        <Section title="TLS" icon={Shield}>
          <div className="space-y-2">
            {tls.map((t: any) => (
              <div key={t.secretName} className="text-sm">
                <div className="text-theme-text-secondary">Secret: {t.secretName ? (
                  <ResourceLink name={t.secretName} kind="secrets" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} />
                ) : '-'}</div>
                <div className="text-xs text-theme-text-tertiary">Hosts: {t.hosts?.join(', ') || '*'}</div>
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
