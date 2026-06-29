import { Globe, Lock } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, KeyValueBadgeList } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import {
  getIstioGatewayStatus,
  getIstioGatewayServers,
  getIstioGatewaySelector,
} from '../resource-utils-istio'

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
                      <Badge protocol={protocol} size="sm">
                        {protocol}:{server.port.number}
                      </Badge>
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
                          <Badge
                            // Every TLS server row already shows a teal HTTPS/TLS protocol pill, so
                            // a teal mode badge would collide. PASSTHROUGH uses `note` (the mode where
                            // the gateway does not terminate TLS — the one worth flagging) to stay clear.
                            tone={
                              server.tls.mode === 'SIMPLE'
                                ? 'accent1'
                                : server.tls.mode === 'MUTUAL' || server.tls.mode === 'ISTIO_MUTUAL'
                                  ? 'accent2'
                                  : server.tls.mode === 'PASSTHROUGH' || server.tls.mode === 'AUTO_PASSTHROUGH'
                                    ? 'note'
                                    : 'structural'
                            }
                            size="sm"
                          >
                            {server.tls.mode || 'SIMPLE'}
                          </Badge>
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
