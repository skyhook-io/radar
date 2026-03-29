import { Shield, AlertTriangle, Globe, Lock, Key } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'

function detectIssuerType(spec: any): string {
  if (spec.acme) return 'ACME'
  if (spec.ca) return 'CA'
  if (spec.selfSigned !== undefined) return 'SelfSigned'
  if (spec.vault) return 'Vault'
  return 'Unknown'
}

export function getSolverType(solver: any): { type: string; detail: string } {
  if (solver.http01) {
    const ingressClass = solver.http01.ingress?.class || solver.http01.ingress?.ingressClassName
    return { type: 'HTTP-01', detail: ingressClass ? `Ingress class: ${ingressClass}` : 'Ingress solver' }
  }
  if (solver.dns01) {
    const dns = solver.dns01
    if (dns.cloudDNS) return { type: 'DNS-01', detail: `Cloud DNS (project: ${dns.cloudDNS.project})` }
    if (dns.route53) return { type: 'DNS-01', detail: `Route 53 (region: ${dns.route53.region || 'default'})` }
    if (dns.cloudflare) return { type: 'DNS-01', detail: 'Cloudflare' }
    if (dns.digitalocean) return { type: 'DNS-01', detail: 'DigitalOcean' }
    if (dns.azureDNS) return { type: 'DNS-01', detail: 'Azure DNS' }
    if (dns.akamai) return { type: 'DNS-01', detail: 'Akamai' }
    if (dns.rfc2136) return { type: 'DNS-01', detail: 'RFC2136' }
    if (dns.webhook) return { type: 'DNS-01', detail: `Webhook (${dns.webhook.groupName || 'custom'})` }
    return { type: 'DNS-01', detail: `Provider: ${Object.keys(dns).join(', ')}` }
  }
  return { type: 'Unknown', detail: '' }
}

function IssuerRendererBase({ data, kind }: { data: any; kind: string }) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const isReady = readyCond?.status === 'True'
  const isNotReady = readyCond?.status === 'False'

  const issuerType = detectIssuerType(spec)

  const acme = spec.acme
  const acmeStatus = status.acme
  const solvers = acme?.solvers || []

  return (
    <>
      {/* Problem detection alert */}
      {isNotReady && (
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400">{kind} Not Ready</div>
              <div className="text-xs text-red-300/80 mt-1">
                {readyCond.reason && <span className="font-medium">{readyCond.reason}: </span>}
                {readyCond.message || 'The issuer is not in a ready state.'}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Status */}
      <Section title="Status" icon={Shield}>
        <PropertyList>
          <Property
            label="Ready"
            value={
              <span className={clsx(
                'badge',
                isReady
                  ? 'bg-green-500/20 text-green-400'
                  : 'bg-red-500/20 text-red-400'
              )}>
                {isReady ? 'Ready' : 'Not Ready'}
              </span>
            }
          />
          <Property label="Issuer Type" value={issuerType} />
        </PropertyList>
      </Section>

      {/* ACME section */}
      {acme && (
        <Section title="ACME" icon={Globe}>
          <PropertyList>
            <Property label="Server" value={acme.server} />
            <Property label="Email" value={acme.email} />
            <Property label="Private Key Secret" value={acme.privateKeySecretRef?.name} />
            <Property label="Registered Email" value={acmeStatus?.lastRegisteredEmail} />
            <Property label="Account URI" value={acmeStatus?.uri} />
          </PropertyList>
        </Section>
      )}

      {/* Solvers section */}
      {solvers.length > 0 && (
        <Section title={`Solvers (${solvers.length})`} icon={Key}>
          <div className="space-y-2">
            {solvers.map((solver: any, i: number) => {
              const { type, detail } = getSolverType(solver)
              return (
                <div key={i} className="card-inner-lg">
                  <div className="flex items-center gap-2">
                    <span className="badge bg-theme-elevated text-theme-text-secondary">
                      {type}
                    </span>
                    {detail && (
                      <span className="text-xs text-theme-text-tertiary">{detail}</span>
                    )}
                  </div>
                  {solver.selector && (
                    <div className="mt-2 text-xs text-theme-text-tertiary">
                      {solver.selector.dnsNames && (
                        <div>DNS Names: {solver.selector.dnsNames.join(', ')}</div>
                      )}
                      {solver.selector.dnsZones && (
                        <div>DNS Zones: {solver.selector.dnsZones.join(', ')}</div>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* CA section */}
      {spec.ca && (
        <Section title="CA" icon={Lock}>
          <PropertyList>
            <Property label="Secret Name" value={spec.ca.secretName} />
          </PropertyList>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}

export function ClusterIssuerRenderer({ data }: { data: any }) {
  return <IssuerRendererBase data={data} kind="ClusterIssuer" />
}

export function IssuerRenderer({ data }: { data: any }) {
  return <IssuerRendererBase data={data} kind="Issuer" />
}
