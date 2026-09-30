import type { ReactNode } from 'react'
import { ArrowRight } from 'lucide-react'
import { CNPG_GROUP } from '../resources/resource-utils-cnpg'
import type { CNPGFact } from './workspace'
import type { CNPGLogicalPath } from './logicalReplication'
import { FactGrid, FactRow, FactSource, FactValue, RefLink, toneTextClass, type CNPGNavigate } from './primitives'

function Hop({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className="min-w-0 break-words text-sm">{children}</div>
    </div>
  )
}

/**
 * One Subscription's path to its publisher: publication, the slot the
 * publisher keeps for it, and whether that slot survives a publisher
 * failover. `slot` is the host's reading of the publisher primary; absent
 * means it was not read.
 */
export function CNPGLogicalPathView({
  path,
  slot,
  onNavigate,
  compact,
  notice,
}: {
  path: CNPGLogicalPath
  slot?: CNPGFact
  onNavigate?: CNPGNavigate
  compact?: boolean
  /** The host's word on the slot reading, e.g. that its latest refresh failed. */
  notice?: ReactNode
}) {
  const s = path.subscription
  const pub = path.publication
  const publisher = path.publisher
  const slotFact: CNPGFact = slot ?? { text: path.slot.name ? `Slot ${path.slot.name}: not read` : path.slot.reason ?? 'No slot', tone: 'unknown' }
  const chain = (
    <div className="flex flex-wrap items-start gap-x-2 gap-y-1">
      <Hop label="Subscription">
        <RefLink refTo={{ kind: 'Subscription', group: CNPG_GROUP, namespace: s.namespace, name: s.name }} onNavigate={onNavigate} mono>
          {s.sqlName ?? s.name}
        </RefLink>
        <span className="text-xs text-theme-text-tertiary"> on {s.cluster ?? '?'}/{s.dbname ?? '?'}</span>
      </Hop>
      <ArrowRight className="mt-4 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
      <Hop label="Publication">
        {pub.object ? (
          <RefLink refTo={{ kind: 'Publication', group: CNPG_GROUP, namespace: pub.object.namespace, name: pub.object.name }} onNavigate={onNavigate} mono>
            {pub.name}
          </RefLink>
        ) : (
          <span className="font-mono">{pub.name ?? '?'}</span>
        )}
        <span className="text-xs text-theme-text-tertiary">
          {' '}
          on{' '}
          {publisher.kind === 'cluster' ? (
            <RefLink refTo={{ kind: 'Cluster', group: CNPG_GROUP, namespace: publisher.namespace, name: publisher.name }} onNavigate={onNavigate}>
              {publisher.namespace === s.namespace && publisher.name === s.cluster ? 'the same cluster' : `${publisher.namespace}/${publisher.name}`}
            </RefLink>
          ) : (
            <span>{path.externalCluster.host ?? `external cluster ${path.externalCluster.name ?? '?'}`}</span>
          )}
          /{pub.dbname ?? '?'}
        </span>
      </Hop>
      <ArrowRight className="mt-4 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
      <Hop label="Slot on the publisher">
        <FactValue fact={slotFact} />
      </Hop>
    </div>
  )
  if (compact) {
    return (
      <div className="space-y-1">
        {notice}
        {chain}
        <div className={`text-xs ${toneTextClass(path.failover.tone)}`}>Failover: {path.failover.text}</div>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      {notice}
      {chain}
      <FactGrid>
        <FactRow label="Publisher">
          {publisher.kind === 'cluster' ? (
            <span>
              {publisher.namespace}/{publisher.name} <span className="text-xs text-theme-text-tertiary">via {publisher.via}</span>
            </span>
          ) : (
            <span className="text-theme-text-secondary">Outside Radar's view: {publisher.reason}</span>
          )}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
            From the subscriber's spec.externalClusters[{path.externalCluster.name ?? '?'}].connectionParameters.host
          </div>
        </FactRow>
        <FactRow label="Publication object">
          {pub.object ? (
            <span className={pub.object.applied === false ? toneTextClass('degraded') : undefined}>
              {pub.object.applied === true ? 'Applied' : pub.object.applied === false ? 'Not applied' : 'Pending'}
            </span>
          ) : (
            <span className="text-theme-text-tertiary">
              {publisher.kind === 'cluster' ? 'No Publication object declares it: it may exist in SQL only' : 'Unknown'}
            </span>
          )}
        </FactRow>
        <FactRow label="Slot">
          <FactValue fact={slotFact} />
          <FactSource fact={slotFact} />
        </FactRow>
        <FactRow label="Survives failover">
          <FactValue fact={path.failover} />
          <FactSource fact={path.failover} />
        </FactRow>
        <FactRow label="Subscription status">
          {s.applied === false ? (
            <span className={toneTextClass('degraded')}>Not applied{s.message ? `: ${s.message}` : ''}</span>
          ) : s.applied === true ? (
            'Applied'
          ) : (
            <span className="text-theme-text-tertiary">Pending</span>
          )}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
            Subscription status. Apply errors and lag are not reported: CloudNativePG's default exporter has no pg_stat_subscription query.
          </div>
        </FactRow>
      </FactGrid>
    </div>
  )
}
