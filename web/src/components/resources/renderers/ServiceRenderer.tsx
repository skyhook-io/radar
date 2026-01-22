import { Globe } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList, CopyHandler } from '../drawer-components'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'

interface ServiceRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
}

export function ServiceRenderer({ data, onCopy, copied }: ServiceRendererProps) {
  const spec = data.spec || {}
  const ports = spec.ports || []
  const lbIngress = data.status?.loadBalancer?.ingress || []
  const namespace = data.metadata?.namespace
  const serviceName = data.metadata?.name

  return (
    <>
      <Section title="Service" icon={Globe}>
        <PropertyList>
          <Property label="Type" value={spec.type || 'ClusterIP'} />
          <Property label="Cluster IP" value={spec.clusterIP} copyable onCopy={onCopy} copied={copied} />
          {spec.externalIPs?.length > 0 && (
            <Property label="External IPs" value={spec.externalIPs.join(', ')} copyable onCopy={onCopy} copied={copied} />
          )}
          {lbIngress.length > 0 && (
            <Property
              label="Load Balancer"
              value={lbIngress[0].ip || lbIngress[0].hostname}
              copyable
              onCopy={onCopy}
              copied={copied}
            />
          )}
          <Property label="Session Affinity" value={spec.sessionAffinity} />
          <Property label="External Traffic" value={spec.externalTrafficPolicy} />
        </PropertyList>
      </Section>

      <Section title="Ports" defaultExpanded>
        <div className="space-y-2">
          {ports.map((port: any, i: number) => (
            <div key={i} className="bg-theme-elevated/30 rounded p-2 text-sm">
              <div className="flex items-center justify-between">
                <span className="text-theme-text-primary font-medium">{port.name || `port-${i}`}</span>
                <div className="flex items-center gap-2">
                  <PortForwardInlineButton
                    namespace={namespace}
                    serviceName={serviceName}
                    port={port.port}
                    protocol={port.protocol || 'TCP'}
                  />
                </div>
              </div>
              <div className="text-xs text-theme-text-secondary mt-1">
                {port.port} {port.targetPort !== port.port && `→ ${port.targetPort}`}
                {port.nodePort && ` (NodePort: ${port.nodePort})`}
              </div>
            </div>
          ))}
        </div>
      </Section>

      {spec.selector && (
        <Section title="Selector">
          <KeyValueBadgeList items={spec.selector} />
        </Section>
      )}
    </>
  )
}
