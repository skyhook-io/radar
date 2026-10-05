import { Globe, Network, Server, Layers, Link as LinkIcon } from 'lucide-react'
import {
  Section,
  PropertyList,
  Property,
  ConditionsSection,
  ResourceLink,
  AlertBanner,
} from '../../ui/drawer-components'
import { getDatumStatus } from '../resource-utils-datum'

type Ref = { kind: string; namespace: string; name: string; group?: string }
interface Props {
  data: any
  onNavigate?: (ref: Ref) => void
}
const DNS = 'dns.networking.miloapis.com'
const NET = 'networking.datumapis.com'
export function DatumRenderer({ data, onNavigate }: Props) {
  const spec = data.spec || {},
    status = data.status || {},
    kind = data.kind,
    ns = data.metadata?.namespace || ''
  const health = getDatumStatus(data)
  const link = (
    name: string | undefined,
    target: string,
    group = NET,
    namespace = ns,
  ) =>
    name ? (
      <ResourceLink
        name={name}
        kind={target}
        namespace={namespace}
        group={group}
        onNavigate={onNavigate}
      />
    ) : (
      'Not reported'
    )
  const fields: [string, any][] = []
  if (kind === 'DNSZone')
    fields.push(
      ['Domain', spec.domainName],
      ['Zone class', link(spec.dnsZoneClassName, 'dnszoneclasses', DNS, '')],
      ['Records', status.recordCount ?? 'Not reported'],
      ['Domain reference', link(status.domainRef?.name, 'domains')],
      ['Nameservers', status.nameservers?.join(', ') || 'Not reported'],
    )
  if (kind === 'DNSZoneClass')
    fields.push(
      ['Controller', spec.controllerName],
      ['Default TTL', spec.defaults?.defaultTTL],
      ['Nameserver policy', spec.nameServerPolicy?.mode],
      [
        'Static nameservers',
        spec.nameServerPolicy?.static?.servers?.join(', '),
      ],
    )
  if (kind === 'DNSRecordSet')
    fields.push(
      ['Zone', link(spec.dnsZoneRef?.name, 'dnszones', DNS)],
      ['Record type', spec.recordType],
    )
  if (kind === 'Domain')
    fields.push(
      ['Domain', spec.domainName],
      [
        'Apex domain',
        status.apex === undefined ? 'Not reported' : String(status.apex),
      ],
      [
        'Nameservers',
        status.nameservers?.map((s: any) => s.hostname).join(', ') ||
          'Not reported',
      ],
      ['Registrar', status.registration?.registrar],
      ['Expires', status.registration?.expiresAt],
      ['Last verification', status.verification?.lastVerificationAttempt],
      ['Next verification', status.verification?.nextVerificationAttempt],
    )
  if (kind === 'HTTPProxy')
    fields.push(
      ['Hostnames', spec.hostnames?.join(', ')],
      ['Canonical hostname', status.canonicalHostname || 'Not reported'],
      [
        'Addresses',
        status.addresses?.map((a: any) => `${a.type}: ${a.value}`).join(', ') ||
          'Not reported',
      ],
    )
  if (kind === 'Connector')
    fields.push(
      [
        'Connector class',
        link(spec.connectorClassName, 'connectorclasses', NET, ''),
      ],
      ['Capabilities', spec.capabilities?.map((c: any) => c.type).join(', ')],
      ['Lease', link(status.leaseRef?.name, 'leases', 'coordination.k8s.io')],
    )
  if (kind === 'ConnectorClass')
    fields.push(['Controller', spec.controllerName])
  if (kind === 'ConnectorAdvertisement')
    fields.push(['Connector', link(spec.connectorRef?.name, 'connectors')])
  if (kind === 'Network')
    fields.push(
      ['IP families', spec.ipFamilies?.join(', ')],
      ['IPAM mode', spec.ipam?.mode],
      ['IPv4 range', spec.ipam?.ipv4Range],
      ['IPv6 range', spec.ipam?.ipv6Range],
      ['Assigned IPv6 prefix', status.ipam?.ipv6Prefix],
      ['MTU', spec.mtu],
    )
  if (kind === 'NetworkContext')
    fields.push(
      ['Network', link(spec.network?.name, 'networks')],
      ['Location', spec.location?.name],
      ['IP families', spec.ipFamilies?.join(', ')],
      ['MTU', spec.mtu],
    )
  if (kind === 'NetworkBinding')
    fields.push(
      [
        'Network',
        link(
          spec.network?.name,
          'networks',
          NET,
          spec.network?.namespace || ns,
        ),
      ],
      [
        'Network context',
        link(
          status.networkContextRef?.name,
          'networkcontexts',
          NET,
          status.networkContextRef?.namespace || ns,
        ),
      ],
      [
        'Consumer (may be outside this control plane)',
        [spec.consumer?.kind, spec.consumer?.name].filter(Boolean).join(' / '),
      ],
    )
  if (kind === 'Subnet' || kind === 'SubnetClaim')
    fields.push(
      ['Network context', link(spec.networkContext?.name, 'networkcontexts')],
      ['Class', spec.subnetClass],
      ['Family', spec.ipFamily],
      [
        'Requested prefix',
        spec.startAddress
          ? `${spec.startAddress}/${spec.prefixLength}`
          : 'Automatic',
      ],
      [
        'Assigned prefix',
        status.startAddress
          ? `${status.startAddress}/${status.prefixLength}`
          : 'Not reported',
      ],
      [
        'Allocated subnet',
        kind === 'SubnetClaim'
          ? link(status.subnetRef?.name, 'subnets')
          : undefined,
      ],
    )
  if (kind === 'NetworkService')
    fields.push(
      ['Strategy', spec.trafficDistribution?.strategy],
      ['Locations', status.summary?.locations ?? 'Not reported'],
      ['Members', status.summary?.members ?? 'Not reported'],
      ['Healthy members', status.summary?.healthy ?? 'Not reported'],
    )
  const instanceSpec = kind === 'Workload' ? spec.template?.spec || {} : spec
  if (kind === 'Instance' || kind === 'Workload')
    fields.push(
      [
        'Runtime',
        instanceSpec.runtime?.virtualMachine
          ? 'Virtual machine'
          : instanceSpec.runtime?.sandbox?.containers
            ? 'Containers'
            : 'Not reported',
      ],
      ['Runtime class', instanceSpec.runtime?.class],
      ['Instance type', instanceSpec.runtime?.resources?.instanceType],
      ['Desired replicas', status.desiredReplicas],
      ['Observed replicas', status.replicas],
      ['Ready replicas', status.readyReplicas],
    )
  if (kind === 'Project' || kind === 'Organization')
    fields.push(
      ['Display name', spec.displayName],
      ['Description', spec.description],
      [
        'Parent',
        data.metadata?.ownerReferences?.map((r: any) => r.name).join(', '),
      ],
    )
  return (
    <>
      {data.metadata?.annotations?.['radar.skyhook.io/synthetic-status'] ===
        'true' && (
        <AlertBanner
          variant="info"
          title="Synthetic fixture status"
          message="These conditions were seeded for UI testing; no controller produced this status."
        />
      )}
      {health.label === 'Unknown' && (
        <AlertBanner
          variant="info"
          title="Status not established"
          message="The API has not reported current readiness. Configuration alone does not establish health."
        />
      )}
      <Section
        title={kind}
        icon={kind === 'Instance' || kind === 'Workload' ? Server : Globe}
        defaultExpanded
      >
        <PropertyList>
          {fields
            .filter(([, value]) => value !== undefined)
            .map(([label, value]) => (
              <Property
                key={label}
                label={label}
                value={
                  typeof value === 'object' && value && !('$$typeof' in value)
                    ? JSON.stringify(value)
                    : value
                }
              />
            ))}
        </PropertyList>
      </Section>
      {kind === 'DNSRecordSet' && (
        <Section
          title={`Records (${spec.records?.length || 0})`}
          icon={Layers}
          defaultExpanded
        >
          <div className="space-y-3">
            {(spec.records || []).map((record: any, index: number) => (
              <div className="card-inner-lg" key={index}>
                <PropertyList>
                  <Property label="Name" value={record.name} />
                  <Property
                    label="TTL"
                    value={
                      record.ttl === undefined ? 'Default' : `${record.ttl}s`
                    }
                  />
                  {Object.entries(record)
                    .filter(([key]) => !['name', 'ttl'].includes(key))
                    .flatMap(([type, value]) =>
                      typeof value === 'object' && value
                        ? Object.entries(value).map(([key, val]) => (
                            <Property
                              key={`${type}-${key}`}
                              label={`${type.toUpperCase()} ${key}`}
                              value={String(val)}
                            />
                          ))
                        : [],
                    )}
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      )}
      {kind === 'Domain' && status.verification && (
        <Section title="Ownership verification" icon={LinkIcon} defaultExpanded>
          <p className="text-xs text-theme-text-secondary mb-3">
            Publish the controller's DNS challenge or serve its HTTP challenge
            to prove ownership. A pending method does not invalidate another
            successful method.
          </p>
          <PropertyList>
            <Property
              label="DNS name"
              value={status.verification.dnsRecord?.name}
            />
            <Property
              label="DNS type"
              value={status.verification.dnsRecord?.type}
            />
            <Property
              label="DNS content"
              value={status.verification.dnsRecord?.content}
            />
            <Property
              label="HTTP URL"
              value={status.verification.httpToken?.url}
            />
            <Property
              label="HTTP body"
              value={status.verification.httpToken?.body}
            />
          </PropertyList>
        </Section>
      )}
      {kind === 'HTTPProxy' && (
        <Section
          title="Configured routes and backends"
          icon={Network}
          defaultExpanded
        >
          <p className="text-xs text-theme-text-secondary mb-3">
            These are configured destinations. They do not represent observed
            traffic.
          </p>
          <div className="space-y-3">
            {(spec.rules || []).map((rule: any, index: number) => (
              <div className="card-inner-lg" key={rule.name || index}>
                <div className="font-medium text-sm text-theme-text-primary mb-2">
                  {rule.name || `Rule ${index + 1}`}
                </div>
                <PropertyList>
                  <Property
                    label="Matches"
                    value={
                      rule.matches
                        ?.map((m: any) =>
                          m.path
                            ? `${m.path.type}: ${m.path.value}`
                            : JSON.stringify(m),
                        )
                        .join('; ') || 'All requests'
                    }
                  />
                  {(rule.backends || []).map((backend: any, i: number) => (
                    <Property
                      key={i}
                      label={`Backend ${i + 1}`}
                      value={
                        <span>
                          {(backend.connector
                              ? link(backend.connector.name, 'connectors')
                              : backend.instance
                                ? link(
                                    backend.instance.name,
                                    'endpointslices',
                                    'discovery.k8s.io',
                                  )
                                : backend.networkService
                                  ? link(
                                      backend.networkService.name,
                                      'networkservices',
                                    )
                                  : backend.endpoint ? '' : 'No destination')}{' '}
                          {backend.endpoint && <span>Endpoint: {backend.endpoint}</span>}{' '}
                          {(backend.instance?.port ||
                            backend.networkService?.port) &&
                            `:${backend.instance?.port || backend.networkService?.port}`}{' '}
                          {backend.weight !== undefined &&
                            `(weight ${backend.weight})`}{' '}
                          {backend.tls?.hostname &&
                            `TLS: ${backend.tls.hostname}`}
                        </span>
                      }
                    />
                  ))}
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      )}
      {kind === 'ConnectorAdvertisement' && (
        <Section title="Advertised endpoints" icon={Network} defaultExpanded>
          <div className="space-y-2">
            {(spec.layer4 || []).map((layer: any) => (
              <div className="card-inner-lg" key={layer.name}>
                <PropertyList>
                  <Property label="Name" value={layer.name} />
                  <Property
                    label="Services"
                    value={layer.services
                      ?.map(
                        (s: any) =>
                          `${s.address} (${s.ports?.map((p: any) => `${p.protocol}/${p.port}`).join(', ')})`,
                      )
                      .join('; ')}
                  />
                </PropertyList>
              </div>
            ))}
          </div>
        </Section>
      )}
      {kind === 'NetworkService' && (
        <Section title="Service ports" icon={Network} defaultExpanded>
          <PropertyList>
            {(spec.ports || []).map((p: any, i: number) => (
              <Property
                key={i}
                label={p.name || `Port ${i + 1}`}
                value={`${p.protocol}/${p.port}`}
              />
            ))}
          </PropertyList>
        </Section>
      )}
      {['hostnameStatuses', 'recordSets', 'capabilities'].flatMap((field) =>
        (status[field] || [])
          .filter((item: any) => item.conditions?.length)
          .map((item: any, i: number) => (
            <Section
              key={`${field}-${i}`}
              title={item.hostname || item.name || item.type}
              defaultExpanded
            >
              <ConditionsSection variant="plain" conditions={item.conditions} />
            </Section>
          )),
      )}
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
