// KNative cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getKnativeConditionStatus,
  getKnativeServiceUrl,
  getKnativeServiceLatestRevision,
  getKnativeServiceTraffic,
  getRevisionStatus,
  getRevisionImage,
  getRevisionConcurrency,
  getRouteUrl,
  getRouteTraffic,
  getConfigurationLatestCreated,
  getConfigurationLatestReady,
  getBrokerAddress,
  getTriggerBroker,
  getTriggerSubscriber,
  getTriggerFilter,
  getSourceSink,
  getPingSourceSchedule,
  getPingSourceData,
  getChannelAddress,
  getSubscriptionChannel,
  getSubscriptionSubscriber,
  getSequenceStepCount,
  getParallelBranchCount,
  getDomainMappingUrl,
  getServerlessServiceMode,
} from '../resource-utils-knative'

function StatusCell({ resource, getStatus }: { resource: any; getStatus: (r: any) => { text: string; color: string } }) {
  const status = getStatus(resource)
  return (
    <span className={clsx('badge', status.color)}>
      {status.text}
    </span>
  )
}

function TextCell({ value }: { value: string }) {
  return <span className="text-sm text-theme-text-secondary truncate block">{value}</span>
}

function NumberCell({ value }: { value: number }) {
  return <span className="text-sm text-theme-text-secondary">{value}</span>
}

function DefaultCell() {
  return <span className="text-sm text-theme-text-tertiary">-</span>
}

// ============================================================================
// KNATIVE SERVICE
// ============================================================================

export function KnativeServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'url':
      return <TextCell value={getKnativeServiceUrl(resource)} />
    case 'latestRevision':
      return <TextCell value={getKnativeServiceLatestRevision(resource)} />
    case 'traffic':
      return <TextCell value={getKnativeServiceTraffic(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// REVISION
// ============================================================================

export function RevisionCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getRevisionStatus} />
    case 'routing': {
      const state = resource?.metadata?.labels?.['serving.knative.dev/routingState'] || ''
      if (state === 'active') return <span className="text-green-400 text-sm">Active</span>
      if (state === 'reserve') return <span className="text-theme-text-tertiary text-sm">Reserve</span>
      return <TextCell value={state || '-'} />
    }
    case 'image':
      return <TextCell value={getRevisionImage(resource)} />
    case 'concurrency':
      return <TextCell value={getRevisionConcurrency(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// ROUTE
// ============================================================================

export function RouteCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'url':
      return <TextCell value={getRouteUrl(resource)} />
    case 'traffic':
      return <TextCell value={getRouteTraffic(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// CONFIGURATION
// ============================================================================

export function ConfigurationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'latestCreated':
      return <TextCell value={getConfigurationLatestCreated(resource)} />
    case 'latestReady':
      return <TextCell value={getConfigurationLatestReady(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// BROKER
// ============================================================================

export function BrokerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'address':
      return <TextCell value={getBrokerAddress(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// TRIGGER
// ============================================================================

export function TriggerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'broker':
      return <TextCell value={getTriggerBroker(resource)} />
    case 'subscriber':
      return <TextCell value={getTriggerSubscriber(resource)} />
    case 'filter':
      return <TextCell value={getTriggerFilter(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// EVENTTYPE
// ============================================================================

export function EventTypeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'type':
      return <TextCell value={resource?.spec?.type || '-'} />
    case 'source':
      return <TextCell value={resource?.spec?.source || '-'} />
    case 'reference': {
      const ref = resource?.spec?.reference
      if (ref) return <TextCell value={`${ref.kind || ''}/${ref.name || ''}`} />
      const broker = resource?.spec?.broker
      if (broker) return <TextCell value={`Broker/${broker}`} />
      return <DefaultCell />
    }
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// PING SOURCE
// ============================================================================

export function PingSourceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'schedule':
      return <TextCell value={getPingSourceSchedule(resource)} />
    case 'sink':
      return <TextCell value={getSourceSink(resource)} />
    case 'data':
      return <TextCell value={getPingSourceData(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// APISERVERSOURCE / CONTAINERSOURCE (identical columns: status + sink)
// ============================================================================

function SourceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'sink':
      return <TextCell value={getSourceSink(resource)} />
    default:
      return <DefaultCell />
  }
}

export { SourceCell as ApiServerSourceCell, SourceCell as ContainerSourceCell }

// ============================================================================
// SINKBINDING
// ============================================================================

export function SinkBindingCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'sink':
      return <TextCell value={getSourceSink(resource)} />
    case 'subject': {
      const subject = resource?.spec?.subject
      if (!subject) return <TextCell value="-" />
      if (subject.name) return <TextCell value={`${subject.kind || ''}/${subject.name}`} />
      if (subject.selector?.matchLabels) {
        const labels = Object.entries(subject.selector.matchLabels).map(([k, v]) => `${k}=${v}`).join(', ')
        return <TextCell value={`${subject.kind || ''} (${labels})`} />
      }
      return <TextCell value={subject.kind || '-'} />
    }
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// CHANNEL / INMEMORYCHANNEL (identical columns: status + address)
// ============================================================================

function ChannelCellBase({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'address':
      return <TextCell value={getChannelAddress(resource)} />
    default:
      return <DefaultCell />
  }
}

export { ChannelCellBase as ChannelCell, ChannelCellBase as InMemoryChannelCell }

// ============================================================================
// SUBSCRIPTION
// ============================================================================

export function SubscriptionCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'channel':
      return <TextCell value={getSubscriptionChannel(resource)} />
    case 'subscriber':
      return <TextCell value={getSubscriptionSubscriber(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// SEQUENCE
// ============================================================================

export function SequenceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'steps':
      return <NumberCell value={getSequenceStepCount(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// PARALLEL
// ============================================================================

export function ParallelCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'branches':
      return <NumberCell value={getParallelBranchCount(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// DOMAINMAPPING
// ============================================================================

export function DomainMappingCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'url':
      return <TextCell value={getDomainMappingUrl(resource)} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// KNATIVE INGRESS
// ============================================================================

export function KnativeIngressCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'ingressClass': {
      const cls = resource?.metadata?.annotations?.['networking.knative.dev/ingress.class'] || '-'
      // Show just the short name (e.g., "kourier" from "kourier.ingress.networking.knative.dev")
      const short = cls.includes('.') ? cls.split('.')[0] : cls
      return <TextCell value={short} />
    }
    case 'hosts': {
      const rules = resource?.spec?.rules || []
      const allHosts = rules.flatMap((r: any) => r.hosts || [])
      const unique = [...new Set(allHosts)]
      return <TextCell value={unique.length > 0 ? unique.join(', ') : '-'} />
    }
    case 'visibility': {
      const rules = resource?.spec?.rules || []
      const vis = rules[0]?.visibility || '-'
      return <TextCell value={vis} />
    }
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// KNATIVE CERTIFICATE
// ============================================================================

export function KnativeCertificateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'dnsNames': {
      const names = resource?.spec?.dnsNames || []
      return <TextCell value={names.length > 0 ? names.join(', ') : '-'} />
    }
    case 'secretName':
      return <TextCell value={resource?.spec?.secretName || '-'} />
    default:
      return <DefaultCell />
  }
}

// ============================================================================
// SERVERLESSSERVICE
// ============================================================================

export function ServerlessServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusCell resource={resource} getStatus={getKnativeConditionStatus} />
    case 'mode': {
      const mode = getServerlessServiceMode(resource)
      return (
        <span className={clsx(
          'badge',
          mode === 'Proxy' ? 'bg-yellow-500/20 text-yellow-400' : 'bg-green-500/20 text-green-400'
        )}>
          {mode}
        </span>
      )
    }
    default:
      return <DefaultCell />
  }
}
