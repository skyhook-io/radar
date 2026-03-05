import { Globe, Route, Layers, Lock } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../drawer-components'

interface TraefikIngressRouteRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function TraefikIngressRouteRenderer({ data, onNavigate }: TraefikIngressRouteRendererProps) {
  const spec = data.spec || {}
  const routes = spec.routes || []
  const entryPoints = spec.entryPoints || []
  const tls = spec.tls
  const ns = data.metadata?.namespace || ''

  // Collect unique services and middleware across all routes
  const allServices: Array<{ name: string; kind: string; port?: number; namespace?: string; serversTransport?: string }> = []
  const allMiddlewares: Array<{ name: string; namespace?: string }> = []
  const seenSvc = new Set<string>()
  const seenMw = new Set<string>()

  for (const route of routes) {
    for (const svc of route.services || []) {
      const key = `${svc.kind || 'Service'}/${svc.namespace || ''}/${svc.name}`
      if (!seenSvc.has(key)) {
        seenSvc.add(key)
        allServices.push(svc)
      }
    }
    for (const mw of route.middlewares || []) {
      const key = `${mw.namespace || ''}/${mw.name}`
      if (!seenMw.has(key)) {
        seenMw.add(key)
        allMiddlewares.push(mw)
      }
    }
  }

  const hasNoRoutes = routes.length === 0
  const hasNoServices = allServices.length === 0 && routes.length > 0

  // Determine kind label from apiVersion/kind
  const kindLabel = data.kind || 'IngressRoute'

  return (
    <>
      {hasNoRoutes && (
        <AlertBanner
          variant="warning"
          title="No Routes Defined"
          message={`This ${kindLabel} has no routes configured. No traffic will be routed.`}
        />
      )}

      {hasNoServices && (
        <AlertBanner
          variant="info"
          title="No Backend Services"
          message="Routes are defined but none reference backend services."
        />
      )}

      <Section title={kindLabel} icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Entry Points" value={entryPoints.length > 0 ? entryPoints.join(', ') : '-'} />
          <Property label="Routes" value={`${routes.length}`} />
          <Property label="Services" value={`${allServices.length}`} />
          <Property label="Middlewares" value={allMiddlewares.length > 0 ? `${allMiddlewares.length}` : 'None'} />
          <Property label="TLS" value={tls ? 'Enabled' : 'None'} />
        </PropertyList>
      </Section>

      <Section title={`Routes (${routes.length})`} icon={Route} defaultExpanded>
        <div className="space-y-3">
          {routes.map((route: any, i: number) => {
            const services = route.services || []
            const middlewares = route.middlewares || []

            return (
              <div key={i} className="bg-theme-elevated/30 rounded p-3">
                {/* Match expression */}
                <div className="flex items-start gap-2 mb-2">
                  <span className="text-sm font-medium text-theme-text-primary break-all">
                    {route.match || 'No match rule'}
                  </span>
                  {route.priority && (
                    <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary shrink-0">
                      priority: {route.priority}
                    </span>
                  )}
                  {route.kind && route.kind !== 'Rule' && (
                    <span className="px-1.5 py-0.5 bg-theme-hover rounded text-[10px] text-theme-text-secondary shrink-0">
                      {route.kind}
                    </span>
                  )}
                </div>

                {/* Services */}
                {services.length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Services</div>
                    <div className="space-y-1">
                      {services.map((svc: any, si: number) => {
                        const svcKind = svc.kind || 'Service'
                        const svcNs = svc.namespace || ns
                        const isTraefikSvc = svcKind === 'TraefikService'
                        const port = svc.port ? `:${svc.port}` : ''
                        const weight = svc.weight !== undefined ? ` (${svc.weight}%)` : ''

                        return (
                          <div key={si} className="flex items-center gap-2 text-xs">
                            {isTraefikSvc && (
                              <span className="px-1.5 py-0.5 bg-cyan-500/10 text-cyan-400 rounded text-[10px]">
                                TraefikService
                              </span>
                            )}
                            <ResourceLink
                              name={svc.name}
                              kind={isTraefikSvc ? 'traefikservices' : 'services'}
                              namespace={svcNs}
                              label={<span className="text-blue-400">{svc.name}{port}{weight}</span>}
                              onNavigate={onNavigate}
                            />
                            {svc.serversTransport && (
                              <span className="text-theme-text-tertiary">
                                transport: <ResourceLink
                                  name={svc.serversTransport}
                                  kind="serverstransports"
                                  namespace={ns}
                                  onNavigate={onNavigate}
                                />
                              </span>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}

                {/* Middlewares */}
                {middlewares.length > 0 && (
                  <div>
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Middlewares</div>
                    <div className="flex flex-wrap gap-1">
                      {middlewares.map((mw: any, mi: number) => {
                        const mwNs = mw.namespace || ns
                        return (
                          <ResourceLink
                            key={mi}
                            name={mw.name}
                            kind="middlewares"
                            namespace={mwNs}
                            label={
                              <span className="px-1.5 py-0.5 bg-purple-500/10 text-purple-400 rounded text-[10px] inline-block">
                                {mw.namespace && mw.namespace !== ns ? `${mw.namespace}/` : ''}{mw.name}
                              </span>
                            }
                            onNavigate={onNavigate}
                          />
                        )
                      })}
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </Section>

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
            {tls.options?.name && (
              <Property label="TLS Option" value={
                <ResourceLink
                  name={tls.options.name}
                  kind="tlsoptions"
                  namespace={tls.options.namespace || ns}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {tls.store?.name && (
              <Property label="TLS Store" value={
                <ResourceLink
                  name={tls.store.name}
                  kind="tlsstores"
                  namespace={tls.store.namespace || ns}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {tls.certResolver && (
              <Property label="Cert Resolver" value={tls.certResolver} />
            )}
            {tls.domains && tls.domains.length > 0 && (
              <Property label="Domains" value={
                <div className="space-y-1">
                  {tls.domains.map((d: any, i: number) => (
                    <div key={i} className="text-xs">
                      {d.main && <span className="text-theme-text-secondary">{d.main}</span>}
                      {d.sans && d.sans.length > 0 && (
                        <span className="text-theme-text-tertiary"> + {d.sans.length} SAN(s)</span>
                      )}
                    </div>
                  ))}
                </div>
              } />
            )}
            {!tls.secretName && !tls.certResolver && (
              <Property label="Mode" value="TLS termination (no explicit secret or resolver)" />
            )}
          </PropertyList>
        </Section>
      )}

      {allMiddlewares.length > 0 && (
        <Section title={`Middleware Chain (${allMiddlewares.length})`} icon={Layers}>
          <div className="space-y-1">
            {allMiddlewares.map((mw, i) => {
              const mwNs = mw.namespace || ns
              const isCrossNs = mw.namespace && mw.namespace !== ns
              return (
                <div key={i} className="flex items-center gap-2 text-xs">
                  <span className="text-theme-text-tertiary w-4 text-right">{i + 1}.</span>
                  <ResourceLink
                    name={mw.name}
                    kind="middlewares"
                    namespace={mwNs}
                    onNavigate={onNavigate}
                  />
                  {isCrossNs && (
                    <span className="px-1.5 py-0.5 bg-yellow-500/10 text-yellow-400 rounded text-[10px]">
                      {mw.namespace}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}
    </>
  )
}
