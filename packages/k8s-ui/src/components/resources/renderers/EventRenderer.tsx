import { AlertTriangle, Info, Clock, Target, Server, Hash } from 'lucide-react'
import { clsx } from 'clsx'
import { Badge } from '../../ui/Badge'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import type { NavigateToResource } from '../../../utils/navigation'
import { apiVersionToGroup, kindToPluralWithGroup } from '../../../utils/navigation'

interface EventRendererProps {
  data: any
  onNavigate?: NavigateToResource
}

export function EventRenderer({ data, onNavigate }: EventRendererProps) {
  const eventType = data.type || 'Normal'
  const isWarning = eventType === 'Warning'
  const reason = data.reason || ''
  const message = data.message || ''
  const count = data.count || 1
  const involvedObject = data.involvedObject || {}
  const source = data.source || {}
  const firstTimestamp = data.firstTimestamp
  const lastTimestamp = data.lastTimestamp || data.metadata?.creationTimestamp
  const statusClass = isWarning ? 'status-degraded' : 'status-neutral'
  const badgeSeverity = isWarning ? 'warning' : 'info'

  return (
    <>
      {/* Event Type Banner */}
      <div className={clsx(
        'mb-4 p-4 rounded-lg border',
        statusClass,
      )}>
        <div className="flex items-start gap-3">
          {isWarning ? (
            <AlertTriangle className="w-5 h-5 mt-0.5 shrink-0" />
          ) : (
            <Info className="w-5 h-5 mt-0.5 shrink-0" />
          )}
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <Badge severity={badgeSeverity}>
                {eventType}
              </Badge>
              {reason && (
                <span className="text-sm font-semibold">
                  {reason}
                </span>
              )}
              {count > 1 && (
                <span className="text-xs text-theme-text-tertiary">
                  ({count} times)
                </span>
              )}
            </div>
            {message && (
              <p className="text-sm break-all">
                {message}
              </p>
            )}
          </div>
        </div>
      </div>

      {/* Involved Object */}
      {involvedObject.kind && (
        <Section title="Involved Object" icon={Target} defaultExpanded>
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <span className="text-theme-text-tertiary text-sm w-24">Kind</span>
              <span className="text-theme-text-primary text-sm font-medium">{involvedObject.kind}</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-theme-text-tertiary text-sm w-24">Name</span>
              {onNavigate ? (
                <button
                  onClick={() => {
                    const group = apiVersionToGroup(involvedObject.apiVersion) || undefined
                    onNavigate({
                      kind: kindToPluralWithGroup(involvedObject.kind, group ?? ''),
                      namespace: involvedObject.namespace || '',
                      name: involvedObject.name,
                      group,
                    })
                  }}
                  className="text-blue-400 hover:text-blue-300 text-sm font-medium hover:underline"
                >
                  {involvedObject.name}
                </button>
              ) : (
                <span className="text-theme-text-primary text-sm">{involvedObject.name}</span>
              )}
            </div>
            {involvedObject.namespace && (
              <div className="flex items-center gap-2">
                <span className="text-theme-text-tertiary text-sm w-24">Namespace</span>
                <span className="text-theme-text-secondary text-sm">{involvedObject.namespace}</span>
              </div>
            )}
            {involvedObject.uid && (
              <div className="flex items-center gap-2">
                <span className="text-theme-text-tertiary text-sm w-24">UID</span>
                <span className="text-theme-text-tertiary text-xs font-mono">{involvedObject.uid}</span>
              </div>
            )}
            {involvedObject.fieldPath && (
              <div className="flex items-center gap-2">
                <span className="text-theme-text-tertiary text-sm w-24">Field</span>
                <span className="text-theme-text-secondary text-sm font-mono">{involvedObject.fieldPath}</span>
              </div>
            )}
          </div>
        </Section>
      )}

      {/* Timing */}
      <Section title="Timing" icon={Clock} defaultExpanded>
        <PropertyList>
          {count > 1 && <Property label="Count" value={count} />}
          {firstTimestamp && (
            <Property label="First Seen" value={formatAge(firstTimestamp)} />
          )}
          {lastTimestamp && (
            <Property label="Last Seen" value={formatAge(lastTimestamp)} />
          )}
          {firstTimestamp && lastTimestamp && firstTimestamp !== lastTimestamp && (
            <Property label="Duration" value={calculateDuration(firstTimestamp, lastTimestamp)} />
          )}
        </PropertyList>
      </Section>

      {/* Source */}
      {(source.component || source.host) && (
        <Section title="Source" icon={Server} defaultExpanded={false}>
          <PropertyList>
            {source.component && <Property label="Component" value={source.component} />}
            {source.host && <Property label="Host" value={source.host} />}
          </PropertyList>
        </Section>
      )}

      {/* Resource Version Info */}
      {involvedObject.resourceVersion && (
        <Section title="Resource Version" icon={Hash} defaultExpanded={false}>
          <PropertyList>
            <Property label="Object Version" value={involvedObject.resourceVersion} />
            {involvedObject.apiVersion && <Property label="API Version" value={involvedObject.apiVersion} />}
          </PropertyList>
        </Section>
      )}
    </>
  )
}

function calculateDuration(first: string, last: string): string {
  const firstDate = new Date(first)
  const lastDate = new Date(last)
  const diffMs = lastDate.getTime() - firstDate.getTime()

  if (diffMs < 1000) return 'instant'
  if (diffMs < 60000) return `${Math.floor(diffMs / 1000)}s`
  if (diffMs < 3600000) return `${Math.floor(diffMs / 60000)}m`
  if (diffMs < 86400000) return `${Math.floor(diffMs / 3600000)}h ${Math.floor((diffMs % 3600000) / 60000)}m`
  return `${Math.floor(diffMs / 86400000)}d ${Math.floor((diffMs % 86400000) / 3600000)}h`
}
