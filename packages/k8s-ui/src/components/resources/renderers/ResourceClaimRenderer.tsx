import { Cpu, Layers, Users } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getResourceClaimStatus,
  getResourceClaimAllocation,
  getResourceClaimReservedFor,
} from '../resource-utils-dra'

interface ResourceClaimRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function requestDeviceClass(req: any): string {
  return req?.exactly?.deviceClassName ||
    req?.deviceClassName ||
    (req?.firstAvailable || []).map((s: any) => s?.deviceClassName).filter(Boolean).join(' | ') ||
    '-'
}

export function ResourceClaimRenderer({ data, onNavigate }: ResourceClaimRendererProps) {
  const status = getResourceClaimStatus(data)
  const requests = data.spec?.devices?.requests || []
  const allocation = getResourceClaimAllocation(data)
  const reservedFor = getResourceClaimReservedFor(data)
  const deviceStatuses = data.status?.devices || []

  return (
    <>
      {status.level === 'degraded' && (
        <AlertBanner
          variant="warning"
          title="Allocated but unreserved"
          items={['A device is allocated to this claim but no consumer holds it — long-lived, this leaks the device.']}
        />
      )}

      {/* Device Requests */}
      {requests.length > 0 && (
        <Section title={`Device Requests (${requests.length})`} icon={Cpu} defaultExpanded>
          <div className="space-y-2">
            {requests.map((req: any, i: number) => {
              const detail = req?.exactly || req
              return (
                <div key={req?.name || i} className="card-inner">
                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                    <span className="text-theme-text-primary font-medium">{req?.name || `request-${i}`}</span>
                    <span className="text-theme-text-secondary">{requestDeviceClass(req)}</span>
                    {detail?.count != null && (
                      <span className="text-theme-text-tertiary">count: {String(detail.count)}</span>
                    )}
                    {detail?.allocationMode && (
                      <span className="text-theme-text-tertiary">{detail.allocationMode}</span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Allocation */}
      <Section title="Allocation" icon={Layers} defaultExpanded>
        {allocation.length > 0 ? (
          <div className="space-y-2">
            {allocation.map((r, i) => (
              <div key={i} className="card-inner">
                <PropertyList>
                  <Property label="Driver" value={r.driver} />
                  <Property label="Pool" value={r.pool} />
                  <Property label="Device" value={r.device} />
                </PropertyList>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">Not allocated — waiting for a driver to satisfy this claim</div>
        )}
      </Section>

      {/* Reserved For */}
      {reservedFor.length > 0 && (
        <Section title={`Reserved For (${reservedFor.length})`} icon={Users} defaultExpanded>
          <div className="space-y-1">
            {reservedFor.map((r, i) => (
              <div key={i} className="card-inner text-sm">
                {r.resource === 'pods' ? (
                  <ResourceLink name={r.name} kind="pods" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} />
                ) : (
                  <span className="text-theme-text-secondary">{r.resource}/{r.name}</span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Per-device health (beta, populated by drivers that report it) */}
      {deviceStatuses.length > 0 && (
        <Section title={`Device Status (${deviceStatuses.length})`}>
          <div className="space-y-2">
            {deviceStatuses.map((d: any, i: number) => (
              <div key={i} className="card-inner text-sm">
                <div className="text-theme-text-primary font-medium">{d.device || '-'}</div>
                {(d.conditions || []).map((c: any, j: number) => (
                  <div key={j} className="text-xs text-theme-text-secondary mt-0.5">
                    {c.type}: {c.status}{c.message ? ` — ${c.message}` : ''}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
