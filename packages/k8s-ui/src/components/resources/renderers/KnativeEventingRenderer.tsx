import { Radio, Filter, FileType, Inbox, ArrowRightLeft } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KnativeNotReadyBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { getKnativeConditionStatus } from '../resource-utils-knative'

interface RendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// ============================================================================
// Broker
// ============================================================================

export function BrokerRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const address = data.status?.address?.url
  const delivery = data.spec?.delivery
  const brokerClass = data.metadata?.annotations?.['eventing.knative.dev/broker.class']
  const configRef = data.spec?.config

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Broker" />

      <Section title="Overview" icon={Radio} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Address" value={address ? (
            <span className="text-theme-text-primary break-all">{address}</span>
          ) : undefined} />
          <Property label="Class" value={brokerClass} />
          {configRef && (
            <Property label="Config" value={`${configRef.kind}/${configRef.name}`} />
          )}
        </PropertyList>
      </Section>

      {delivery && (
        <Section title="Delivery" defaultExpanded>
          <PropertyList>
            {delivery.deadLetterSink?.ref && (
              <Property label="Dead Letter" value={
                <ResourceLink
                  name={delivery.deadLetterSink.ref.name}
                  kind={kindToPlural(delivery.deadLetterSink.ref.kind || 'Service')}
                  namespace={delivery.deadLetterSink.ref.namespace || ns}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {delivery.deadLetterSink?.uri && (
              <Property label="Dead Letter URI" value={delivery.deadLetterSink.uri} />
            )}
            <Property label="Retry" value={delivery.retry != null ? String(delivery.retry) : undefined} />
            <Property label="Backoff Policy" value={delivery.backoffPolicy} />
            <Property label="Backoff Delay" value={delivery.backoffDelay} />
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// Trigger
// ============================================================================

export function TriggerRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}
  const brokerName = spec.broker || 'default'
  const subscriberRef = spec.subscriber?.ref
  const subscriberUri = spec.subscriber?.uri
  const filterAttrs = spec.filter?.attributes || {}
  const filterEntries = Object.entries(filterAttrs)

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Trigger" />

      <Section title="Overview" icon={Filter} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Broker" value={
            <ResourceLink
              name={brokerName}
              kind="brokers"
              namespace={ns}
              onNavigate={onNavigate}
            />
          } />
          <Property label="Subscriber" value={
            subscriberRef ? (
              <ResourceLink
                name={subscriberRef.name}
                kind={kindToPlural(subscriberRef.kind || 'Service')}
                namespace={subscriberRef.namespace || ns}
                onNavigate={onNavigate}
              />
            ) : subscriberUri ? (
              <span className="text-theme-text-primary break-all">{subscriberUri}</span>
            ) : '-'
          } />
        </PropertyList>
      </Section>

      {filterEntries.length > 0 && (
        <Section title={`Filter (${filterEntries.length} attributes)`} defaultExpanded>
          <div className="flex flex-wrap gap-1">
            {filterEntries.map(([key, val]) => (
              <span key={key} className="badge bg-theme-elevated text-theme-text-secondary">
                {key}={String(val)}
              </span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}

// ============================================================================
// EventType
// ============================================================================

export function EventTypeRenderer({ data }: RendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}

  return (
    <>
      <Section title="Overview" icon={FileType} defaultExpanded>
        <PropertyList>
          <Property label="Type" value={spec.type} />
          <Property label="Source" value={spec.source} />
          <Property label="Schema" value={spec.schema} />
          <Property label="Description" value={spec.description} />
          <Property label="Broker" value={spec.reference?.name || spec.broker} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={status.conditions || []} />
    </>
  )
}

// ============================================================================
// Channel
// ============================================================================

export function ChannelRenderer({ data }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const address = data.status?.address?.url
  const subscribers = data.status?.subscribers || []

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Channel" />

      <Section title="Overview" icon={Inbox} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          <Property label="Address" value={address ? (
            <span className="text-theme-text-primary break-all">{address}</span>
          ) : undefined} />
        </PropertyList>
      </Section>

      {subscribers.length > 0 && (
        <Section title={`Subscribers (${subscribers.length})`} defaultExpanded>
          <div className="space-y-1.5">
            {subscribers.map((sub: any, i: number) => (
              <div key={i} className="text-sm text-theme-text-secondary">
                {sub.subscriberURI || sub.replyURI || `Subscriber ${i + 1}`}
                {sub.ready === 'True' && (
                  <span className="ml-2 px-1.5 py-0.5 bg-green-500/20 text-green-400 rounded text-[10px]">ready</span>
                )}
                {sub.ready === 'False' && (
                  <span className="ml-2 px-1.5 py-0.5 bg-red-500/20 text-red-400 rounded text-[10px]">not ready</span>
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

// ============================================================================
// InMemoryChannel
// ============================================================================

export function InMemoryChannelRenderer({ data, onNavigate }: RendererProps) {
  // InMemoryChannel has the same shape as Channel
  return <ChannelRenderer data={data} onNavigate={onNavigate} />
}

// ============================================================================
// Subscription
// ============================================================================

export function SubscriptionRenderer({ data, onNavigate }: RendererProps) {
  const status = getKnativeConditionStatus(data)
  const ns = data.metadata?.namespace || ''
  const spec = data.spec || {}

  const channelRef = spec.channel
  const subscriberRef = spec.subscriber?.ref
  const subscriberUri = spec.subscriber?.uri
  const replyRef = spec.reply?.ref
  const replyUri = spec.reply?.uri
  const delivery = spec.delivery

  return (
    <>
      <KnativeNotReadyBanner status={status} data={data} resourceType="Subscription" />

      <Section title="Overview" icon={ArrowRightLeft} defaultExpanded>
        <PropertyList>
          <Property label="Status" value={
            <span className={clsx('badge', status.color)}>
              {status.text}
            </span>
          } />
          {channelRef && (
            <Property label="Channel" value={
              <ResourceLink
                name={channelRef.name}
                kind={kindToPlural(channelRef.kind || 'Channel')}
                namespace={ns}
                onNavigate={onNavigate}
              />
            } />
          )}
          <Property label="Subscriber" value={
            subscriberRef ? (
              <ResourceLink
                name={subscriberRef.name}
                kind={kindToPlural(subscriberRef.kind || 'Service')}
                namespace={subscriberRef.namespace || ns}
                onNavigate={onNavigate}
              />
            ) : subscriberUri ? (
              <span className="text-theme-text-primary break-all">{subscriberUri}</span>
            ) : '-'
          } />
          <Property label="Reply" value={
            replyRef ? (
              <ResourceLink
                name={replyRef.name}
                kind={kindToPlural(replyRef.kind || 'Channel')}
                namespace={replyRef.namespace || ns}
                onNavigate={onNavigate}
              />
            ) : replyUri ? (
              <span className="text-theme-text-primary break-all">{replyUri}</span>
            ) : undefined
          } />
        </PropertyList>
      </Section>

      {delivery?.deadLetterSink && (
        <Section title="Dead Letter" defaultExpanded>
          <PropertyList>
            {delivery.deadLetterSink.ref && (
              <Property label="Sink" value={
                <ResourceLink
                  name={delivery.deadLetterSink.ref.name}
                  kind={kindToPlural(delivery.deadLetterSink.ref.kind || 'Service')}
                  namespace={delivery.deadLetterSink.ref.namespace || ns}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {delivery.deadLetterSink.uri && (
              <Property label="URI" value={delivery.deadLetterSink.uri} />
            )}
            <Property label="Retry" value={delivery.retry != null ? String(delivery.retry) : undefined} />
            <Property label="Backoff Policy" value={delivery.backoffPolicy} />
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
