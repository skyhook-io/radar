import { Globe, Route, Layers, Lock } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ConditionsSection, ResourceLink } from '../../ui/drawer-components'
import {
  getHTTPProxyFQDN,
  getHTTPProxyRouteCount,
  getHTTPProxyServiceCount,
  getHTTPProxyStatus,
  hasHTTPProxyTLS,
} from '../resource-utils-contour'

interface ContourHTTPProxyRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; name: string; namespace: string }) => void
}

export function ContourHTTPProxyRenderer({ data, onNavigate }: ContourHTTPProxyRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const routes = spec.routes || []
  const includes = spec.includes || []
  const tls = spec.virtualhost?.tls
  const tcpproxy = spec.tcpproxy
  const ns = data.metadata?.namespace || ''
  const conditions = status.conditions

  const { label: statusLabel } = getHTTPProxyStatus(data)
  const currentStatus = status.currentStatus?.toLowerCase()

  return (
    <>
      {currentStatus === 'invalid' && (
        <AlertBanner
          variant="error"
          title="Invalid HTTPProxy"
          message={status.description || 'This HTTPProxy has an invalid configuration.'}
        />
      )}

      {currentStatus === 'orphaned' && (
        <AlertBanner
          variant="warning"
          title="Orphaned HTTPProxy"
          message={status.description || 'This HTTPProxy is orphaned — it is not part of any valid delegation chain.'}
        />
      )}

      <Section title="HTTPProxy" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="FQDN" value={getHTTPProxyFQDN(data)} />
          <Property label="TLS" value={hasHTTPProxyTLS(data) ? 'Enabled' : 'None'} />
          <Property label="Status" value={statusLabel} />
          <Property label="Routes" value={`${getHTTPProxyRouteCount(data)}`} />
          <Property label="Services" value={`${getHTTPProxyServiceCount(data)}`} />
        </PropertyList>
      </Section>

      <Section title={`Routes (${routes.length})`} icon={Route} defaultExpanded>
        <div className="space-y-3">
          {routes.map((route: any, i: number) => {
            const services = route.services || []
            const routeConditions = route.conditions || []

            return (
              <div key={i} className="card-inner-lg">
                {/* Conditions (prefix match, header match, etc.) */}
                {routeConditions.length > 0 && (
                  <div className="flex items-start gap-2 mb-2">
                    <span className="text-sm font-medium text-theme-text-primary break-all">
                      {routeConditions.map((c: any) => {
                        if (c.prefix) return `prefix: ${c.prefix}`
                        if (c.header) return `header: ${c.header.name} ${c.header.contains || c.header.exact || c.header.present ? 'match' : ''}`
                        return JSON.stringify(c)
                      }).join(', ')}
                    </span>
                  </div>
                )}

                {routeConditions.length === 0 && (
                  <div className="flex items-start gap-2 mb-2">
                    <span className="text-sm font-medium text-theme-text-primary">
                      (no conditions)
                    </span>
                  </div>
                )}

                {/* Services */}
                {services.length > 0 && (
                  <div>
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Services</div>
                    <div className="space-y-1">
                      {services.map((svc: any, si: number) => {
                        const svcNs = svc.namespace || ns
                        const port = svc.port ? `:${svc.port}` : ''
                        const weight = svc.weight !== undefined ? ` (${svc.weight}%)` : ''

                        return (
                          <div key={si} className="flex items-center gap-2 text-xs">
                            <ResourceLink
                              name={svc.name}
                              kind="services"
                              namespace={svcNs}
                              label={<span className="text-blue-400">{svc.name}{port}{weight}</span>}
                              onNavigate={onNavigate}
                            />
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}
              </div>
            )
          })}

          {routes.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No routes configured</div>
          )}
        </div>
      </Section>

      {includes.length > 0 && (
        <Section title={`Includes (${includes.length})`} icon={Layers} defaultExpanded>
          <div className="space-y-2">
            {includes.map((inc: any, i: number) => {
              const incNs = inc.namespace || ns
              const incConditions = inc.conditions || []

              return (
                <div key={i} className="flex items-center gap-2 text-xs">
                  <ResourceLink
                    name={inc.name}
                    kind="httpproxies"
                    namespace={incNs}
                    label={
                      <span className="text-blue-400">
                        {incNs !== ns ? `${incNs}/` : ''}{inc.name}
                      </span>
                    }
                    onNavigate={onNavigate}
                  />
                  {incConditions.length > 0 && (
                    <span className="text-theme-text-tertiary">
                      ({incConditions.map((c: any) => c.prefix ? `prefix: ${c.prefix}` : JSON.stringify(c)).join(', ')})
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {tcpproxy && (
        <Section title="TCP Proxy" icon={Route} defaultExpanded>
          <div className="space-y-1">
            {(tcpproxy.services || []).map((svc: any, i: number) => {
              const port = svc.port ? `:${svc.port}` : ''
              return (
                <div key={i} className="flex items-center gap-2 text-xs">
                  <ResourceLink
                    name={svc.name}
                    kind="services"
                    namespace={svc.namespace || ns}
                    label={<span className="text-blue-400">{svc.name}{port}</span>}
                    onNavigate={onNavigate}
                  />
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {tls && (
        <Section title="TLS" icon={Lock} defaultExpanded>
          <PropertyList>
            {tls.secretName && (
              <Property label="Secret" value={
                <ResourceLink
                  name={tls.secretName}
                  kind="secrets"
                  namespace={ns}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {tls.minimumProtocolVersion && (
              <Property label="Min Protocol" value={tls.minimumProtocolVersion} />
            )}
            {tls.passthrough !== undefined && (
              <Property label="Passthrough" value={tls.passthrough ? 'Yes' : 'No'} />
            )}
            {!tls.secretName && !tls.passthrough && (
              <Property label="Mode" value="TLS termination (no explicit secret)" />
            )}
          </PropertyList>
        </Section>
      )}

      {conditions && conditions.length > 0 && (
        <Section title="Status" defaultExpanded>
          <ConditionsSection conditions={conditions} />
        </Section>
      )}
    </>
  )
}
