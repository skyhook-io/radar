import { Radio, Server, Waypoints } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import type { ResourceRef } from '../../../types'

interface EndpointSliceRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

function isEndpointReady(endpoint: any): boolean {
  return endpoint?.conditions?.ready !== false
}

function endpointAddressCount(endpoints: any[]): number {
  return endpoints.reduce((total, endpoint) => total + (endpoint.addresses?.length || 0), 0)
}

function endpointTargetLabel(endpoint: any): string | null {
  const target = endpoint.targetRef
  if (!target?.kind || !target?.name) return null
  return target.namespace ? `${target.kind} ${target.namespace}/${target.name}` : `${target.kind} ${target.name}`
}

export function EndpointSliceRenderer({ data, onNavigate }: EndpointSliceRendererProps) {
  const metadata = data.metadata || {}
  const labels = metadata.labels || {}
  const endpoints = data.endpoints || []
  const ports = data.ports || []
  const serviceName = labels['kubernetes.io/service-name']
  const readyCount = endpoints.filter(isEndpointReady).length
  const addresses = endpointAddressCount(endpoints)

  return (
    <>
      <Section title="EndpointSlice" icon={Radio}>
        <PropertyList>
          <Property label="Address Type" value={data.addressType || '-'} />
          <Property label="Endpoints" value={`${readyCount}/${endpoints.length} ready`} />
          <Property label="Addresses" value={addresses} />
          <Property label="Ports" value={ports.length} />
          {serviceName && (
            <Property
              label="Service"
              value={onNavigate ? (
                <button
                  type="button"
                  className="text-sm text-accent-text hover:underline font-medium"
                  onClick={() => onNavigate({ kind: 'Service', namespace: metadata.namespace, name: serviceName })}
                >
                  {serviceName}
                </button>
              ) : serviceName}
            />
          )}
        </PropertyList>
      </Section>

      {ports.length > 0 && (
        <Section title="Ports" icon={Waypoints}>
          <div className="space-y-2">
            {ports.map((port: any, index: number) => (
              <div key={`${port.name || 'port'}-${port.port || index}-${port.protocol || 'TCP'}`} className="card-inner text-sm">
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="font-medium text-theme-text-primary truncate">{port.name || `port-${index + 1}`}</div>
                    {port.appProtocol && (
                      <div className="text-xs text-theme-text-tertiary mt-0.5">{port.appProtocol}</div>
                    )}
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <Badge size="sm" protocol={port.protocol || 'TCP'}>{port.protocol || 'TCP'}</Badge>
                    <span className="font-mono text-theme-text-secondary">{port.port ?? '-'}</span>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {endpoints.length > 0 && (
        <Section title="Endpoints" icon={Server}>
          <div className="space-y-2">
            {endpoints.map((endpoint: any, index: number) => {
              const ready = isEndpointReady(endpoint)
              const targetLabel = endpointTargetLabel(endpoint)
              const target = endpoint.targetRef
              return (
                <div key={`${endpoint.addresses?.join(',') || 'endpoint'}-${index}`} className="card-inner text-sm space-y-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        {(endpoint.addresses || []).map((address: string) => (
                          <Badge key={address} size="sm" tone="structural" className="font-mono">
                            {address}
                          </Badge>
                        ))}
                      </div>
                      {targetLabel && (
                        <button
                          type="button"
                          className="text-xs text-theme-text-secondary hover:text-accent-text"
                          disabled={!onNavigate}
                          onClick={() => onNavigate?.({
                            kind: target.kind,
                            namespace: target.namespace || metadata.namespace,
                            name: target.name,
                          })}
                        >
                          {targetLabel}
                        </button>
                      )}
                    </div>
                    <div className="flex flex-wrap justify-end gap-1.5 shrink-0">
                      <span className={clsx('badge-sm', ready ? 'status-healthy' : 'status-unhealthy')}>
                        {ready ? 'Ready' : 'Not Ready'}
                      </span>
                      {endpoint.conditions?.serving === false && (
                        <span className="badge-sm status-degraded">Not Serving</span>
                      )}
                      {endpoint.conditions?.terminating === true && (
                        <span className="badge-sm status-alert">Terminating</span>
                      )}
                    </div>
                  </div>
                  {(endpoint.nodeName || endpoint.zone) && (
                    <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-theme-text-tertiary">
                      {endpoint.nodeName && <span>Node: {endpoint.nodeName}</span>}
                      {endpoint.zone && <span>Zone: {endpoint.zone}</span>}
                    </div>
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
