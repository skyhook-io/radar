import { type ReactNode } from 'react'
import { Globe, Clock, Radio } from 'lucide-react'
import { Section, PropertyList, Property, KeyValueBadgeList, CopyHandler, AlertBanner } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'

interface ServiceRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  endpointSlices?: any[]
  endpointSlicesLoading?: boolean
  onNavigate?: (ref: ResourceRef) => void
  renderPortAction?: (props: { namespace: string; serviceName: string; port: number; protocol: string }) => ReactNode
}

function endpointSliceAddressCount(slice: any): number {
  return (slice.endpoints || []).reduce((total: number, endpoint: any) => total + (endpoint.addresses?.length || 0), 0)
}

function endpointSliceReadyCount(slice: any): number {
  return (slice.endpoints || []).filter((endpoint: any) => endpoint?.conditions?.ready !== false).length
}

function endpointSliceReadyClass(ready: number, total: number): string {
  if (total === 0) return 'status-unknown'
  if (ready === total) return 'status-healthy'
  if (ready > 0) return 'status-degraded'
  return 'status-unhealthy'
}

export function ServiceRenderer({ data, onCopy, copied, endpointSlices, endpointSlicesLoading, onNavigate, renderPortAction }: ServiceRendererProps) {
  const spec = data.spec || {}
  const ports = spec.ports || []
  const lbIngress = data.status?.loadBalancer?.ingress || []
  const namespace = data.metadata?.namespace
  const serviceName = data.metadata?.name

  const isLoadBalancer = spec.type === 'LoadBalancer'
  const isExternalName = spec.type === 'ExternalName'
  const lbPending = isLoadBalancer && lbIngress.length === 0
  const hasNoSelector = !spec.selector || Object.keys(spec.selector).length === 0

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
          {isExternalName ? (
            <Property label="External Name" value={spec.externalName} copyable onCopy={onCopy} copied={copied} />
          ) : (
            <Property label="Cluster IP" value={spec.clusterIP} copyable onCopy={onCopy} copied={copied} />
          )}
          {spec.externalIPs?.length > 0 && (
            <Property label="External IPs" value={spec.externalIPs.join(', ')} copyable onCopy={onCopy} copied={copied} />
          )}
          {lbIngress.map((ing: any, i: number) => (
            <Property
              key={i}
              label={lbIngress.length > 1 ? `Load Balancer ${i + 1}` : 'Load Balancer'}
              value={ing.ip || ing.hostname}
              copyable
              onCopy={onCopy}
              copied={copied}
            />
          ))}
          <Property label="Session Affinity" value={spec.sessionAffinity} />
          <Property label="External Traffic" value={spec.externalTrafficPolicy} />
          <Property label="Internal Traffic" value={spec.internalTrafficPolicy} />
          {spec.ipFamilyPolicy && <Property label="IP Family Policy" value={spec.ipFamilyPolicy} />}
          {spec.ipFamilies?.length > 0 && <Property label="IP Families" value={spec.ipFamilies.join(', ')} />}
        </PropertyList>
      </Section>

      {ports.length > 0 && (
        <Section title="Ports" defaultExpanded>
          <div className="space-y-2">
            {ports.map((port: any, i: number) => (
              <div key={`${port.port}-${port.protocol || 'TCP'}`} className="card-inner text-sm">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <span className="text-theme-text-primary font-medium">{port.name || `port-${i + 1}`}</span>
                    <span className="text-xs text-theme-text-tertiary">{port.protocol || 'TCP'}</span>
                  </div>
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
                  {port.port}{port.targetPort != null && port.targetPort !== port.port ? ` → ${port.targetPort}` : ''}
                  {port.nodePort ? ` (NodePort: ${port.nodePort})` : ''}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {spec.selector && (
        <Section title="Selector">
          <KeyValueBadgeList items={spec.selector} />
        </Section>
      )}

      {hasNoSelector && !isExternalName && (
        <Section title="EndpointSlices" icon={Radio}>
          {endpointSlicesLoading ? (
            <div className="text-sm text-theme-text-tertiary">Loading EndpointSlices…</div>
          ) : endpointSlices && endpointSlices.length > 0 ? (
            <div className="space-y-2">
              {endpointSlices.map((slice: any) => {
                const sliceName = slice.metadata?.name
                const endpoints = slice.endpoints || []
                const ready = endpointSliceReadyCount(slice)
                const addresses = endpointSliceAddressCount(slice)
                return (
                  <button
                    key={slice.metadata?.uid || sliceName}
                    type="button"
                    className="card-inner w-full text-left hover:bg-theme-hover transition-colors"
                    onClick={() => onNavigate?.({
                      kind: 'EndpointSlice',
                      group: 'discovery.k8s.io',
                      namespace,
                      name: sliceName,
                    })}
                  >
                    <div className="flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-theme-text-primary truncate">{sliceName}</div>
                        <div className="text-xs text-theme-text-tertiary mt-0.5">{slice.addressType || 'Unknown'} address type</div>
                      </div>
                      <div className="flex flex-wrap items-center justify-end gap-1.5 shrink-0">
                        <span className={`badge-sm ${endpointSliceReadyClass(ready, endpoints.length)}`}>{ready}/{endpoints.length} ready</span>
                        <span className="badge-sm bg-theme-elevated text-theme-text-secondary border border-theme-border">{addresses} addresses</span>
                      </div>
                    </div>
                  </button>
                )
              })}
            </div>
          ) : (
            <div className="text-sm text-theme-text-tertiary">No EndpointSlices found for this Service.</div>
          )}
        </Section>
      )}
    </>
  )
}
