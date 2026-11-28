import { Rss, Boxes } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import { TimeValue } from '../../ui/ScheduleValue'
import { Badge } from '../../ui/Badge'
import {
  formatGoDurationWords,
  getFreightAlias,
  getWarehouseDiscoveryError,
  getWarehouseSubscriptions,
  sortFreightNewestFirst,
  type KargoFreight,
  type KargoWarehouse,
} from '../resource-utils-kargo'
import { FreightNameLink, FreightVersionChips, kargoConditionTone, type KargoNavigate } from './KargoShared'

const SHOWN_FREIGHT = 10

interface KargoWarehouseRendererProps {
  data: any
  onNavigate?: KargoNavigate
  /**
   * Freight from this Warehouse, from a host that can read it. Without it only
   * the latest Freight's name is shown.
   */
  freights?: KargoFreight[]
  /**
   * How many Freight the Warehouse has in all, when `freights` holds only its
   * newest. Left out, `freights` is the whole list.
   */
  freightTotal?: number
}

export function KargoWarehouseRenderer({ data, onNavigate, freights, freightTotal }: KargoWarehouseRendererProps) {
  const warehouse = data as KargoWarehouse
  const namespace = warehouse.metadata?.namespace ?? ''
  const subscriptions = getWarehouseSubscriptions(warehouse)
  const discoveryError = getWarehouseDiscoveryError(warehouse)
  const issueShown = useOperationalIssuesShown()
  const produced = freights ? sortFreightNewestFirst(freights) : undefined
  const latestName = warehouse.status?.lastFreightID
  const total = produced ? Math.max(freightTotal ?? 0, produced.length) : undefined

  return (
    <>
      {discoveryError && !issueShown && (
        <AlertBanner
          variant="error"
          title="This Warehouse could not check its sources for new versions"
          message={discoveryError.message ?? discoveryError.reason ?? 'Kargo recorded no message.'}
        />
      )}

      <Section title={`Subscriptions (${subscriptions.length})`} icon={Rss} defaultExpanded>
        {subscriptions.length === 0 ? (
          <div className="text-sm text-theme-text-secondary">This Warehouse subscribes to nothing.</div>
        ) : (
          <div className="space-y-1.5">
            {subscriptions.map((sub, i) => (
              <div key={`${sub.type}-${sub.repoURL ?? i}`} className="card-inner space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="note" size="sm">{sub.type}</Badge>
                  <span className="break-all font-mono text-sm text-theme-text-primary">{sub.repoURL ?? sub.name ?? 'No repository set'}</span>
                </div>
                <div className="text-xs text-theme-text-secondary">
                  {sub.constraint ? `Picks ${sub.constraint}` : 'Picks the newest version'}
                </div>
              </div>
            ))}
          </div>
        )}
        <div className="mt-2">
          <PropertyList>
            <Property label="Last checked" value={<TimeValue timestamp={warehouse.status?.discoveredArtifacts?.discoveredAt} fallback="Not yet" />} />
            <Property label="Checks every" value={formatGoDurationWords(warehouse.spec?.interval)} />
            <Property label="Creates Freight" value={warehouse.spec?.freightCreationPolicy === 'Manual' ? 'Only when someone asks' : 'Automatically'} />
          </PropertyList>
        </div>
      </Section>

      <Section title={total !== undefined ? `Freight (${total})` : 'Freight'} icon={Boxes} defaultExpanded>
        {produced ? (
          total === 0 ? (
            <div className="text-sm text-theme-text-secondary">This Warehouse has not produced any Freight yet.</div>
          ) : (
            <div className="space-y-1.5">
              {produced.slice(0, SHOWN_FREIGHT).map(f => (
                <div key={f.metadata?.name} className="card-inner flex flex-wrap items-center gap-2">
                  <FreightNameLink name={f.metadata?.name ?? ''} alias={getFreightAlias(f)} namespace={namespace} onNavigate={onNavigate} />
                  <FreightVersionChips freight={f} />
                  <span className="ml-auto text-xs text-theme-text-tertiary"><TimeValue timestamp={f.discoveredAt ?? f.metadata?.creationTimestamp} /></span>
                </div>
              ))}
              {total! > Math.min(produced.length, SHOWN_FREIGHT) && (
                <div className="text-xs text-theme-text-tertiary">Showing the newest {Math.min(produced.length, SHOWN_FREIGHT)} of {total}.</div>
              )}
            </div>
          )
        ) : (
          <PropertyList>
            <Property
              label="Latest"
              value={latestName
                ? <FreightNameLink name={latestName} namespace={namespace} onNavigate={onNavigate} />
                : warehouse.spec?.freightCreationPolicy === 'Manual'
                  // Kargo records the latest Freight only when it creates Freight on its own.
                  ? <span className="text-theme-text-secondary">Not recorded: this Warehouse creates Freight only when someone asks</span>
                  : <span className="text-theme-text-secondary">None yet</span>}
            />
          </PropertyList>
        )}
      </Section>

      <ConditionsSection conditions={warehouse.status?.conditions} getConditionTone={kargoConditionTone} />
    </>
  )
}
