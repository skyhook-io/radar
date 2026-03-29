import { Network, Route } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getVirtualServiceStatus,
  getVirtualServiceHostsList,
  getVirtualServiceGatewaysList,
  getVirtualServiceHttpRoutes,
  getVirtualServiceTcpRoutes,
  getVirtualServiceTlsRoutes,
  getVirtualServiceRouteCount,
} from '../resource-utils-istio'

interface IstioVirtualServiceRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function IstioVirtualServiceRenderer({ data, onNavigate }: IstioVirtualServiceRendererProps) {
  const status = getVirtualServiceStatus(data)
  const hosts = getVirtualServiceHostsList(data)
  const gateways = getVirtualServiceGatewaysList(data)
  const httpRoutes = getVirtualServiceHttpRoutes(data)
  const tcpRoutes = getVirtualServiceTcpRoutes(data)
  const tlsRoutes = getVirtualServiceTlsRoutes(data)
  const routeCount = getVirtualServiceRouteCount(data)
  const ns = data.metadata?.namespace || ''

  const hasNoRoutes = routeCount === 0
  const hasFaultInjection = httpRoutes.some((r: any) => r.fault)

  return (
    <>
      {/* Problem alerts */}
      {hasNoRoutes && (
        <AlertBanner
          variant="error"
          title="No Routes Configured"
          message="This VirtualService has no HTTP, TCP, or TLS routes defined."
        />
      )}
      {hasFaultInjection && (
        <AlertBanner
          variant="warning"
          title="Fault Injection Active"
          message="One or more routes have fault injection configured (delay or abort)."
        />
      )}

      {/* Hosts & Gateways */}
      <Section title="Routing" icon={Network} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Hosts" value={hosts.length > 0 ? hosts.join(', ') : '-'} />
          <Property label="Gateways" value={
            gateways.length > 0 ? (
              <div className="flex flex-wrap gap-1">
                {gateways.map((gw, i) => {
                  // Gateway reference can be "namespace/name" or just "name" or "mesh"
                  if (gw === 'mesh') {
                    return <span key={i} className="badge-sm bg-theme-hover text-theme-text-secondary">mesh</span>
                  }
                  const parts = gw.split('/')
                  const gwNs = parts.length === 2 ? parts[0] : ns
                  const gwName = parts.length === 2 ? parts[1] : parts[0]
                  return (
                    <ResourceLink
                      key={i}
                      name={gwName}
                      kind="gateways"
                      namespace={gwNs}
                      onNavigate={onNavigate}
                    />
                  )
                })}
              </div>
            ) : '-'
          } />
        </PropertyList>
      </Section>

      {/* HTTP Routes */}
      {httpRoutes.length > 0 && (
        <Section title={`HTTP Routes (${httpRoutes.length})`} icon={Route} defaultExpanded>
          <div className="space-y-3">
            {httpRoutes.map((route: any, i: number) => (
              <div key={i} className="card-inner-lg">
                {/* Route header */}
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">
                    {route.name || `Route ${i + 1}`}
                  </span>
                  {route.timeout && (
                    <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary">
                      timeout: {route.timeout}
                    </span>
                  )}
                  {route.fault && (
                    <span className="px-1.5 py-0.5 bg-yellow-500/20 text-yellow-400 rounded text-[10px] font-medium">
                      fault injection
                    </span>
                  )}
                  {route.mirror && (
                    <span className="px-1.5 py-0.5 bg-purple-500/20 text-purple-400 rounded text-[10px] font-medium">
                      mirroring
                    </span>
                  )}
                </div>

                {/* Match conditions */}
                {route.match && route.match.length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Match</div>
                    <div className="space-y-1">
                      {route.match.map((m: any, mi: number) => (
                        <div key={mi} className="flex flex-wrap gap-1 text-xs">
                          {m.uri && (
                            <span className="px-1.5 py-0.5 bg-blue-500/10 text-blue-400 rounded">
                              URI {Object.keys(m.uri)[0]}: {Object.values(m.uri)[0] as string}
                            </span>
                          )}
                          {m.method && (
                            <span className="px-1.5 py-0.5 bg-blue-500/10 text-blue-400 rounded">
                              Method: {m.method.exact || Object.values(m.method)[0]}
                            </span>
                          )}
                          {m.headers && Object.entries(m.headers).map(([hk, hv]: [string, any]) => (
                            <span key={hk} className="px-1.5 py-0.5 bg-blue-500/10 text-blue-400 rounded">
                              Header {hk}: {typeof hv === 'object' ? Object.values(hv)[0] as string : String(hv)}
                            </span>
                          ))}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {/* Destinations with weights */}
                {route.route && route.route.length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Destinations</div>
                    <div className="space-y-1.5">
                      {route.route.map((dest: any, di: number) => {
                        const weight = dest.weight ?? (route.route.length === 1 ? 100 : undefined)
                        const destHost = dest.destination?.host || ''
                        const destPort = dest.destination?.port?.number
                        const destSubset = dest.destination?.subset

                        return (
                          <div key={di} className="flex items-center gap-2 text-xs">
                            {weight !== undefined && (
                              <div className="flex items-center gap-1 w-16 shrink-0">
                                <div className="flex-1 h-1.5 bg-theme-hover rounded-full overflow-hidden">
                                  <div
                                    className="h-full bg-blue-500 rounded-full"
                                    style={{ width: `${weight}%` }}
                                  />
                                </div>
                                <span className="text-theme-text-secondary font-medium">{weight}%</span>
                              </div>
                            )}
                            <ResourceLink
                              name={destHost.split('.')[0]}
                              kind="services"
                              namespace={destHost.includes('.') ? destHost.split('.')[1] : ns}
                              label={destHost}
                              onNavigate={onNavigate}
                            />
                            {destPort && (
                              <span className="text-theme-text-tertiary">:{destPort}</span>
                            )}
                            {destSubset && (
                              <span className="px-1.5 py-0.5 bg-theme-hover rounded text-theme-text-secondary">
                                subset: {destSubset}
                              </span>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}

                {/* Retries */}
                {route.retries && (
                  <div className="flex flex-wrap gap-1 text-xs mb-1">
                    <span className="text-theme-text-tertiary">Retries:</span>
                    <span className="text-theme-text-secondary">
                      {route.retries.attempts} attempts
                      {route.retries.perTryTimeout && `, ${route.retries.perTryTimeout} per try`}
                      {route.retries.retryOn && ` on ${route.retries.retryOn}`}
                    </span>
                  </div>
                )}

                {/* Fault injection details */}
                {route.fault && (
                  <div className="flex flex-wrap gap-2 text-xs">
                    {route.fault.delay && (
                      <span className="px-1.5 py-0.5 bg-yellow-500/10 text-yellow-400 rounded">
                        Delay: {route.fault.delay.fixedDelay}
                        {route.fault.delay.percentage?.value && ` (${route.fault.delay.percentage.value}%)`}
                      </span>
                    )}
                    {route.fault.abort && (
                      <span className="px-1.5 py-0.5 bg-red-500/10 text-red-400 rounded">
                        Abort: HTTP {route.fault.abort.httpStatus}
                        {route.fault.abort.percentage?.value && ` (${route.fault.abort.percentage.value}%)`}
                      </span>
                    )}
                  </div>
                )}

                {/* Mirror */}
                {route.mirror && (
                  <div className="flex items-center gap-1 text-xs mt-1">
                    <span className="text-theme-text-tertiary">Mirror:</span>
                    <span className="text-theme-text-secondary">
                      {route.mirror.host}
                      {route.mirror.port?.number && `:${route.mirror.port.number}`}
                    </span>
                    {route.mirrorPercentage?.value !== undefined && (
                      <span className="text-theme-text-tertiary">({route.mirrorPercentage.value}%)</span>
                    )}
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* TCP Routes */}
      {tcpRoutes.length > 0 && (
        <Section title={`TCP Routes (${tcpRoutes.length})`} defaultExpanded>
          <div className="space-y-2">
            {tcpRoutes.map((route: any, i: number) => (
              <div key={i} className="card-inner">
                {route.match && (
                  <div className="text-xs text-theme-text-tertiary mb-1">
                    Match: {JSON.stringify(route.match)}
                  </div>
                )}
                {route.route && route.route.map((dest: any, di: number) => (
                  <div key={di} className="text-xs text-theme-text-secondary">
                    {dest.destination?.host}
                    {dest.destination?.port?.number && `:${dest.destination.port.number}`}
                    {dest.weight !== undefined && ` (${dest.weight}%)`}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* TLS Routes */}
      {tlsRoutes.length > 0 && (
        <Section title={`TLS Routes (${tlsRoutes.length})`} defaultExpanded>
          <div className="space-y-2">
            {tlsRoutes.map((route: any, i: number) => (
              <div key={i} className="card-inner">
                {route.match && (
                  <div className="text-xs text-theme-text-tertiary mb-1">
                    SNI: {route.match.map((m: any) => m.sniHosts?.join(', ')).join('; ')}
                  </div>
                )}
                {route.route && route.route.map((dest: any, di: number) => (
                  <div key={di} className="text-xs text-theme-text-secondary">
                    {dest.destination?.host}
                    {dest.destination?.port?.number && `:${dest.destination.port.number}`}
                    {dest.weight !== undefined && ` (${dest.weight}%)`}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
