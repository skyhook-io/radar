import { Globe } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getServiceEntryStatus,
  getServiceEntryHostsList,
  getServiceEntryLocation,
  getServiceEntryPorts,
  getServiceEntryResolution,
  getServiceEntryEndpoints,
} from '../resource-utils-istio'

interface IstioServiceEntryRendererProps {
  data: any
}

export function IstioServiceEntryRenderer({ data }: IstioServiceEntryRendererProps) {
  const status = getServiceEntryStatus(data)
  const hosts = getServiceEntryHostsList(data)
  const location = getServiceEntryLocation(data)
  const ports = getServiceEntryPorts(data)
  const resolution = getServiceEntryResolution(data)
  const endpoints = getServiceEntryEndpoints(data)

  const hasNoHosts = hosts.length === 0

  return (
    <>
      {hasNoHosts && (
        <AlertBanner
          variant="error"
          title="No Hosts Configured"
          message="This ServiceEntry has no hosts defined."
        />
      )}

      {/* Service Entry info */}
      <Section title="Service Entry" icon={Globe} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Hosts" value={hosts.join(', ') || '-'} />
          <Property label="Location" value={
            <span className={clsx(
              'badge',
              location === 'MESH_EXTERNAL'
                ? 'bg-orange-500/20 text-orange-400'
                : 'bg-blue-500/20 text-blue-400'
            )}>
              {location}
            </span>
          } />
          <Property label="Resolution" value={resolution} />
        </PropertyList>
      </Section>

      {/* Ports */}
      {ports.length > 0 && (
        <Section title={`Ports (${ports.length})`} defaultExpanded>
          <div className="space-y-1">
            {ports.map((port, i) => (
              <div key={i} className="flex items-center gap-2 text-sm">
                <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                  {port.number}/{port.protocol || 'TCP'}
                </span>
                {port.name && (
                  <span className="text-theme-text-tertiary">{port.name}</span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Endpoints */}
      {endpoints.length > 0 && (
        <Section title={`Endpoints (${endpoints.length})`} defaultExpanded>
          <div className="space-y-2">
            {endpoints.map((endpoint, i) => (
              <div key={i} className="card-inner">
                <div className="text-sm text-theme-text-primary">{endpoint.address}</div>
                {endpoint.ports && Object.keys(endpoint.ports).length > 0 && (
                  <div className="flex flex-wrap gap-1 mt-1">
                    {Object.entries(endpoint.ports).map(([name, port]) => (
                      <span key={name} className="badge-sm bg-theme-hover text-theme-text-secondary">
                        {name}: {port}
                      </span>
                    ))}
                  </div>
                )}
                {endpoint.labels && Object.keys(endpoint.labels).length > 0 && (
                  <div className="flex flex-wrap gap-1 mt-1">
                    {Object.entries(endpoint.labels).map(([k, v]) => (
                      <span key={k} className="badge-sm bg-theme-hover text-theme-text-secondary">
                        {k}={v}
                      </span>
                    ))}
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
