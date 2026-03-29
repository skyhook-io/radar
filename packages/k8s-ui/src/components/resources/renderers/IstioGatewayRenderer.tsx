import { Globe, Lock } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getIstioGatewayStatus,
  getIstioGatewayServers,
  getIstioGatewaySelector,
} from '../resource-utils-istio'

const protocolColors: Record<string, string> = {
  HTTP: 'bg-blue-500/20 text-blue-400',
  HTTPS: 'bg-green-500/20 text-green-400',
  HTTP2: 'bg-blue-500/20 text-blue-400',
  GRPC: 'bg-purple-500/20 text-purple-400',
  TCP: 'bg-orange-500/20 text-orange-400',
  TLS: 'bg-green-500/20 text-green-400',
  MONGO: 'bg-emerald-500/20 text-emerald-400',
  MYSQL: 'bg-cyan-500/20 text-cyan-400',
}

interface IstioGatewayRendererProps {
  data: any
}

export function IstioGatewayRenderer({ data }: IstioGatewayRendererProps) {
  const status = getIstioGatewayStatus(data)
  const servers = getIstioGatewayServers(data)
  const selector = getIstioGatewaySelector(data)

  const hasNoServers = servers.length === 0

  return (
    <>
      {hasNoServers && (
        <AlertBanner
          variant="error"
          title="No Servers Configured"
          message="This Istio Gateway has no server definitions."
        />
      )}

      {/* Gateway info */}
      <Section title="Gateway" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Workload Selector" value={
            Object.keys(selector).length > 0 ? (
              <KeyValueBadgeList items={selector} />
            ) : '-'
          } />
        </PropertyList>
      </Section>

      {/* Servers */}
      {servers.length > 0 && (
        <Section title={`Servers (${servers.length})`} defaultExpanded>
          <div className="space-y-3">
            {servers.map((server, i) => {
              const isSecure = server.tls !== undefined
              const protocol = server.port.protocol || 'TCP'

              return (
                <div key={i} className="card-inner-lg">
                  <div className="flex items-center justify-between mb-2">
                    <div className="flex items-center gap-2">
                      {isSecure && <Lock className="w-3.5 h-3.5 text-green-400" />}
                      <span className="text-sm font-medium text-theme-text-primary">
                        {server.port.name || `Port ${server.port.number}`}
                      </span>
                      <span className={clsx(
                        'px-1.5 py-0.5 rounded text-[10px] font-medium',
                        protocolColors[protocol] || 'bg-gray-500/20 text-gray-400'
                      )}>
                        {protocol}:{server.port.number}
                      </span>
                    </div>
                  </div>

                  {/* Hosts */}
                  <div className="space-y-1 text-xs text-theme-text-secondary">
                    <div>
                      <span className="text-theme-text-tertiary">Hosts: </span>
                      <span className="break-all">{server.hosts.join(', ')}</span>
                    </div>

                    {/* TLS settings */}
                    {server.tls && (
                      <>
                        <div>
                          <span className="text-theme-text-tertiary">TLS Mode: </span>
                          <span className={clsx(
                            'px-1.5 py-0.5 rounded text-[10px] font-medium',
                            server.tls.mode === 'SIMPLE' || server.tls.mode === 'MUTUAL'
                              ? 'bg-green-500/20 text-green-400'
                              : server.tls.mode === 'PASSTHROUGH'
                                ? 'bg-blue-500/20 text-blue-400'
                                : 'bg-gray-500/20 text-gray-400'
                          )}>
                            {server.tls.mode || 'SIMPLE'}
                          </span>
                        </div>
                        {server.tls.credentialName && (
                          <div>
                            <span className="text-theme-text-tertiary">Credential: </span>
                            <span>{server.tls.credentialName}</span>
                          </div>
                        )}
                      </>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
