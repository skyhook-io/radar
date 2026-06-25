import { Lock } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'

interface TraefikTLSOptionRendererProps {
  data: any
}

export function TraefikTLSOptionRenderer({ data }: TraefikTLSOptionRendererProps) {
  const spec = data.spec || {}
  const ciphers: string[] = spec.cipherSuites || []
  const curves: string[] = spec.curvePreferences || []
  const alpn: string[] = spec.alpnProtocols || []
  const clientAuth = spec.clientAuth

  return (
    <>
      <Section title="TLS Option" icon={Lock} defaultExpanded>
        <PropertyList>
          <Property label="Min Version" value={spec.minVersion || 'default (VersionTLS12)'} />
          {spec.maxVersion && <Property label="Max Version" value={spec.maxVersion} />}
          {spec.sniStrict !== undefined && <Property label="SNI Strict" value={String(spec.sniStrict)} />}
          {spec.preferServerCipherSuites !== undefined && (
            <Property label="Prefer Server Ciphers" value={String(spec.preferServerCipherSuites)} />
          )}
          {alpn.length > 0 && <Property label="ALPN Protocols" value={alpn.join(', ')} />}
        </PropertyList>
      </Section>

      {ciphers.length > 0 && (
        <Section title={`Cipher Suites (${ciphers.length})`}>
          <div className="flex flex-wrap gap-1">
            {ciphers.map((c, i) => (
              <Badge key={i} tone="structural" size="sm" className="font-mono">{c}</Badge>
            ))}
          </div>
        </Section>
      )}

      {curves.length > 0 && (
        <Section title={`Curve Preferences (${curves.length})`}>
          <div className="flex flex-wrap gap-1">
            {curves.map((c, i) => (
              <Badge key={i} tone="structural" size="sm" className="font-mono">{c}</Badge>
            ))}
          </div>
        </Section>
      )}

      {clientAuth && (
        <Section title="Client Authentication" defaultExpanded>
          <PropertyList>
            {clientAuth.clientAuthType && <Property label="Type" value={clientAuth.clientAuthType} />}
            {Array.isArray(clientAuth.secretNames) && clientAuth.secretNames.length > 0 && (
              <Property label="CA Secrets" value={clientAuth.secretNames.join(', ')} />
            )}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
