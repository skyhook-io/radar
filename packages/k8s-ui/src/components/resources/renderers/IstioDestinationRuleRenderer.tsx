import { Network } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ResourceLink, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getDestinationRuleHost,
  getDestinationRuleSubsets,
  getDestinationRuleTrafficPolicy,
  getDestinationRuleLoadBalancer,
} from '../resource-utils-istio'

interface IstioDestinationRuleRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function IstioDestinationRuleRenderer({ data, onNavigate }: IstioDestinationRuleRendererProps) {
  const host = getDestinationRuleHost(data)
  const subsets = getDestinationRuleSubsets(data)
  const trafficPolicy = getDestinationRuleTrafficPolicy(data)
  const loadBalancer = getDestinationRuleLoadBalancer(data)
  const ns = data.metadata?.namespace || ''

  // Parse host to create service link
  const hostParts = host.split('.')
  const svcName = hostParts[0]
  const svcNs = hostParts.length >= 2 ? hostParts[1] : ns

  return (
    <>
      {/* Host reference */}
      <Section title="Destination" icon={Network} defaultExpanded>
        <PropertyList>
          <Property label="Host" value={
            host !== '-' ? (
              <ResourceLink
                name={svcName}
                kind="services"
                namespace={svcNs}
                label={host}
                onNavigate={onNavigate}
              />
            ) : '-'
          } />
          <Property label="Load Balancer" value={loadBalancer} />
        </PropertyList>
      </Section>

      {/* Traffic Policy */}
      {trafficPolicy && (
        <Section title="Traffic Policy" defaultExpanded>
          <div className="space-y-3">
            {/* Connection Pool */}
            {trafficPolicy.connectionPool && (
              <div>
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Connection Pool</div>
                <PropertyList>
                  {trafficPolicy.connectionPool.tcp && (
                    <>
                      {trafficPolicy.connectionPool.tcp.maxConnections !== undefined && (
                        <Property label="Max TCP Connections" value={String(trafficPolicy.connectionPool.tcp.maxConnections)} />
                      )}
                      {trafficPolicy.connectionPool.tcp.connectTimeout && (
                        <Property label="Connect Timeout" value={trafficPolicy.connectionPool.tcp.connectTimeout} />
                      )}
                    </>
                  )}
                  {trafficPolicy.connectionPool.http && (
                    <>
                      {trafficPolicy.connectionPool.http.h2UpgradePolicy && (
                        <Property label="H2 Upgrade" value={trafficPolicy.connectionPool.http.h2UpgradePolicy} />
                      )}
                      {trafficPolicy.connectionPool.http.http1MaxPendingRequests !== undefined && (
                        <Property label="Max Pending Requests" value={String(trafficPolicy.connectionPool.http.http1MaxPendingRequests)} />
                      )}
                      {trafficPolicy.connectionPool.http.http2MaxRequests !== undefined && (
                        <Property label="Max Requests (H2)" value={String(trafficPolicy.connectionPool.http.http2MaxRequests)} />
                      )}
                      {trafficPolicy.connectionPool.http.maxRequestsPerConnection !== undefined && (
                        <Property label="Max Req/Connection" value={String(trafficPolicy.connectionPool.http.maxRequestsPerConnection)} />
                      )}
                      {trafficPolicy.connectionPool.http.maxRetries !== undefined && (
                        <Property label="Max Retries" value={String(trafficPolicy.connectionPool.http.maxRetries)} />
                      )}
                    </>
                  )}
                </PropertyList>
              </div>
            )}

            {/* Load Balancer */}
            {trafficPolicy.loadBalancer && (
              <div>
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Load Balancer</div>
                <PropertyList>
                  {trafficPolicy.loadBalancer.simple && (
                    <Property label="Algorithm" value={trafficPolicy.loadBalancer.simple} />
                  )}
                  {trafficPolicy.loadBalancer.consistentHash && (
                    <Property label="Consistent Hash" value={
                      trafficPolicy.loadBalancer.consistentHash.httpHeaderName
                        ? `Header: ${trafficPolicy.loadBalancer.consistentHash.httpHeaderName}`
                        : trafficPolicy.loadBalancer.consistentHash.httpCookie
                          ? `Cookie: ${trafficPolicy.loadBalancer.consistentHash.httpCookie.name}`
                          : trafficPolicy.loadBalancer.consistentHash.useSourceIp
                            ? 'Source IP'
                            : 'Configured'
                    } />
                  )}
                </PropertyList>
              </div>
            )}

            {/* Outlier Detection */}
            {trafficPolicy.outlierDetection && (
              <div>
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Outlier Detection</div>
                <PropertyList>
                  {trafficPolicy.outlierDetection.consecutiveErrors !== undefined && (
                    <Property label="Consecutive Errors" value={String(trafficPolicy.outlierDetection.consecutiveErrors)} />
                  )}
                  {trafficPolicy.outlierDetection.consecutive5xxErrors !== undefined && (
                    <Property label="Consecutive 5xx" value={String(trafficPolicy.outlierDetection.consecutive5xxErrors)} />
                  )}
                  {trafficPolicy.outlierDetection.interval && (
                    <Property label="Interval" value={trafficPolicy.outlierDetection.interval} />
                  )}
                  {trafficPolicy.outlierDetection.baseEjectionTime && (
                    <Property label="Ejection Time" value={trafficPolicy.outlierDetection.baseEjectionTime} />
                  )}
                  {trafficPolicy.outlierDetection.maxEjectionPercent !== undefined && (
                    <Property label="Max Ejection %" value={`${trafficPolicy.outlierDetection.maxEjectionPercent}%`} />
                  )}
                </PropertyList>
              </div>
            )}

            {/* TLS */}
            {trafficPolicy.tls && (
              <div>
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">TLS</div>
                <PropertyList>
                  <Property label="Mode" value={trafficPolicy.tls.mode || '-'} />
                </PropertyList>
              </div>
            )}
          </div>
        </Section>
      )}

      {/* Subsets */}
      {subsets.length > 0 && (
        <Section title={`Subsets (${subsets.length})`} defaultExpanded>
          <div className="space-y-3">
            {subsets.map((subset, i) => (
              <div key={i} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary mb-2">{subset.name}</div>
                {Object.keys(subset.labels).length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Labels</div>
                    <KeyValueBadgeList items={subset.labels} />
                  </div>
                )}
                {subset.trafficPolicy && (
                  <div className="text-xs text-theme-text-tertiary">
                    Has traffic policy override
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
