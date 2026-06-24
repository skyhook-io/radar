import { ArrowRightLeft } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'

interface TraefikServersTransportRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// Covers ServersTransport (HTTP) and ServersTransportTCP — overlapping shapes;
// fields absent on one kind simply don't render.
export function TraefikServersTransportRenderer({ data, onNavigate }: TraefikServersTransportRendererProps) {
  const spec = data.spec || {}
  const ns = data.metadata?.namespace || ''
  const kindLabel = data.kind || 'ServersTransport'
  const insecure = !!spec.insecureSkipVerify
  const rootCAs: any[] = spec.rootCAsSecrets || []
  const certs: any[] = spec.certificatesSecrets || []
  const timeouts = spec.forwardingTimeouts

  return (
    <>
      {insecure && (
        <AlertBanner
          variant="warning"
          title="TLS verification disabled"
          message="insecureSkipVerify is enabled — Traefik will not validate the upstream's certificate. Connections to the backend are vulnerable to interception."
        />
      )}

      <Section title={kindLabel} icon={ArrowRightLeft} defaultExpanded>
        <PropertyList>
          {spec.serverName && <Property label="Server Name (SNI)" value={spec.serverName} />}
          <Property
            label="Insecure Skip Verify"
            value={<span className={insecure ? 'text-orange-400' : undefined}>{String(insecure)}</span>}
          />
          {spec.peerCertURI && <Property label="Peer Cert URI" value={spec.peerCertURI} />}
          {spec.maxIdleConnsPerHost !== undefined && <Property label="Max Idle Conns / Host" value={String(spec.maxIdleConnsPerHost)} />}
          {spec.disableHTTP2 !== undefined && <Property label="Disable HTTP/2" value={String(spec.disableHTTP2)} />}
          {spec.terminationDelay !== undefined && <Property label="Termination Delay" value={String(spec.terminationDelay)} />}
        </PropertyList>
      </Section>

      {(rootCAs.length > 0 || certs.length > 0) && (
        <Section title="Certificates" defaultExpanded>
          <PropertyList>
            {rootCAs.length > 0 && (
              <Property label="Root CA Secrets" value={
                <div className="flex flex-wrap gap-1">
                  {rootCAs.map((s: any, i: number) => {
                    const name = typeof s === 'string' ? s : s?.secret || s?.name
                    return name ? <ResourceLink key={i} name={name} kind="secrets" namespace={ns} onNavigate={onNavigate} /> : null
                  })}
                </div>
              } />
            )}
            {certs.length > 0 && (
              <Property label="Client Cert Secrets" value={
                <div className="flex flex-wrap gap-1">
                  {certs.map((s: any, i: number) => {
                    const name = typeof s === 'string' ? s : s?.secret || s?.name
                    return name ? <ResourceLink key={i} name={name} kind="secrets" namespace={ns} onNavigate={onNavigate} /> : null
                  })}
                </div>
              } />
            )}
          </PropertyList>
        </Section>
      )}

      {timeouts && (
        <Section title="Forwarding Timeouts">
          <PropertyList>
            {timeouts.dialTimeout !== undefined && <Property label="Dial" value={String(timeouts.dialTimeout)} />}
            {timeouts.responseHeaderTimeout !== undefined && <Property label="Response Header" value={String(timeouts.responseHeaderTimeout)} />}
            {timeouts.idleConnTimeout !== undefined && <Property label="Idle Conn" value={String(timeouts.idleConnTimeout)} />}
            {timeouts.readIdleTimeout !== undefined && <Property label="Read Idle" value={String(timeouts.readIdleTimeout)} />}
            {timeouts.pingTimeout !== undefined && <Property label="Ping" value={String(timeouts.pingTimeout)} />}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
