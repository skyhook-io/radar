import { type ReactNode } from 'react'
import { Globe, Clock } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList, CopyHandler, AlertBanner } from '../../ui/drawer-components'

interface ServiceRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  renderPortAction?: (props: { namespace: string; serviceName: string; port: number; protocol: string }) => ReactNode
}

export function ServiceRenderer({ data, onCopy, copied, renderPortAction }: ServiceRendererProps) {
  const spec = data.spec || {}
  const ports = spec.ports || []
  const lbIngress = data.status?.loadBalancer?.ingress || []
  const namespace = data.metadata?.namespace
  const serviceName = data.metadata?.name

  // Check for issues
  const isLoadBalancer = spec.type === 'LoadBalancer'
  const lbPending = isLoadBalancer && lbIngress.length === 0
  const hasNoSelector = !spec.selector || Object.keys(spec.selector).length === 0
  const isExternalName = spec.type === 'ExternalName'

  return (
    <>
      {/* LoadBalancer pending warning */}
      {lbPending && (
        <AlertBanner
          variant="warning"
          icon={Clock}
          title="Load Balancer Pending"
          message="External IP/hostname has not been assigned yet. This may take a few minutes. Check Events below if provisioning is stuck."
        />
      )}

      {/* No selector warning (manual endpoints) */}
      {hasNoSelector && !isExternalName && (
        <AlertBanner
          variant="info"
          title="No Pod Selector"
          message="This service has no selector — endpoints must be managed manually or by an external controller."
        />
      )}

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
            <div key={`${port.port}-${port.protocol || 'TCP'}`} className="card-inner text-sm">
              <div className="flex items-center justify-between">
                <span className="text-theme-text-primary font-medium">{port.name || `port-${i + 1}`}</span>
                <div className="flex items-center gap-2">
                  {renderPortAction?.({
                    namespace,
                    serviceName,
                    port: port.port,
                    protocol: port.protocol || 'TCP',
                  })}
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
