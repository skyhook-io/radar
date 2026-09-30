import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { CNPG_BARMAN_OBJECTSTORE_GROUP, CNPG_GROUP } from '../resources/resource-utils-cnpg'
import { cnpgReadyInstances, type CNPGFleetRow, type CNPGInstance } from './workspace'
import type { CNPGDimension } from './ha'
import { CNPGConnectSection } from './CNPGConnectSection'
import {
  FactGrid,
  FactRow,
  FactSource,
  FactValue,
  PrimaryConflictNote,
  ProblemCallout,
  ProblemList,
  RefLink,
  SummaryHeading,
  ToneDot,
  toneTextClass,
  CNPG_PRIMARY_BUTTON,
  CNPG_SECONDARY_BUTTON,
  type CNPGNavigate,
} from './primitives'

function ReadyCount({ row }: { row: CNPGFleetRow }) {
  const r = cnpgReadyInstances(row)
  if (!r.note) return <>{r.text}</>
  return (
    <Tooltip content={r.note} position="top">
      <span className={clsx('font-medium', toneTextClass(r.tone ?? 'unknown'))}>
        {r.text} Pods <Badge severity="warning" size="sm">status says {row.instances.ready}</Badge>
      </span>
    </Tooltip>
  )
}

export interface CNPGSummaryAction {
  label: string
  onClick: () => void
  primary?: boolean
}

function InstancePill({ pod, namespace, onNavigate }: { pod: CNPGInstance; namespace: string; onNavigate?: CNPGNavigate }) {
  const tone = pod.ready === true ? 'healthy' : pod.ready === false ? 'unhealthy' : 'unknown'
  const role = pod.role === 'primary' ? 'Primary' : pod.role === 'replica' ? 'Replica' : 'Role unknown'
  const readiness = pod.ready === true ? 'Ready' : pod.ready === false ? 'Not ready' : 'Readiness unknown'
  return (
    <Tooltip content={`${pod.name} · ${role} · ${readiness}${pod.node ? ` · ${pod.node}` : ''}`} position="top">
      <button
        type="button"
        onClick={() => onNavigate?.({ kind: 'Pod', group: '', namespace, name: pod.name })}
        className={clsx(
          'inline-flex items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-2 py-0.5 text-xs',
          onNavigate ? 'hover:border-accent' : 'cursor-default',
        )}
      >
        <ToneDot tone={tone} />
        <span className="font-mono">{pod.name}</span>
        <span className="text-theme-text-tertiary">{pod.role === 'primary' ? 'P' : pod.role === 'replica' ? 'R' : '?'}</span>
      </button>
    </Tooltip>
  )
}

const CHIP = 'inline-flex items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-2 py-0.5 text-xs'

function DimensionChips({ dimensions, onSelect }: { dimensions: CNPGDimension[]; onSelect?: (id: CNPGDimension['id']) => void }) {
  return (
    <div className="mb-3 flex flex-wrap gap-1.5" aria-label="Health by dimension">
      {dimensions.map((d) => {
        const body = (
          <>
            <ToneDot tone={d.tone} />
            <span className="text-theme-text-secondary">{d.label}</span>
            <span className={toneTextClass(d.tone)}>{d.text}</span>
          </>
        )
        return (
          <Tooltip key={d.id} content={d.source} position="top">
            {onSelect ? (
              <button
                type="button"
                onClick={() => onSelect(d.id)}
                aria-label={`${d.label}: ${d.text}. Open ${d.label.toLowerCase()} details`}
                className={clsx(CHIP, 'hover:border-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent')}
              >
                {body}
              </button>
            ) : (
              <span className={CHIP}>{body}</span>
            )}
          </Tooltip>
        )
      })}
    </div>
  )
}

/** "+N more" that opens the rest of the problems in place, when the host links nowhere else. */
function MoreProblems({ count, open, onToggle, panelId }: { count: number; open: boolean; onToggle: () => void; panelId: string }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-controls={panelId}
      onClick={onToggle}
      className="inline-flex items-center gap-1 text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent rounded"
    >
      <CollapseChevron open={open} className="h-3 w-3" />
      {open ? 'Hide' : `+${count} more`}
    </button>
  )
}

export function CNPGClusterSummary({
  row,
  onNavigate,
  actions,
  problemsLink,
  extra,
  lead,
  dimensions,
  onSelectDimension,
  haSection,
  stateFacts,
}: {
  row: CNPGFleetRow
  onNavigate?: CNPGNavigate
  actions?: CNPGSummaryAction[]
  /** Link to the complete list of this cluster's findings, shown when more than one exists. */
  problemsLink?: (count: number) => ReactNode
  extra?: ReactNode
  /** Rendered first, above the problem callout: standing states such as maintenance mode. */
  lead?: ReactNode
  /** Serving · Replication · Protection · Storage, each from its own source (see cnpgDimensions). */
  dimensions?: CNPGDimension[]
  /** Makes each dimension chip open where that dimension is explained (e.g. Runtime → Replication). */
  onSelectDimension?: (id: CNPGDimension['id']) => void
  /** The host's "HA and instances" section (CNPGClusterHASection), rendered after State. */
  haSection?: ReactNode
  /** Extra FactRows appended to the State grid, e.g. live facts only the host can read. */
  stateFacts?: ReactNode
}) {
  const top = row.problems[0]
  const rest = row.problems.length - 1
  const p = row.protection
  const ns = row.namespace
  const radarFindings = row.problems.some((x) => x.severity !== 'posture')
  const [showRest, setShowRest] = useState(false)
  const restDisclosure = useDisclosure(showRest)

  return (
    <div className="px-4 py-4">
      {lead}
      {dimensions && dimensions.length > 0 && <DimensionChips dimensions={dimensions} onSelect={onSelectDimension} />}
      {top && (
        <ProblemCallout
          problem={top}
          onNavigate={onNavigate}
          more={
            rest > 0
              ? problemsLink?.(row.problems.length) ?? (
                  <MoreProblems count={rest} open={showRest} onToggle={() => setShowRest((v) => !v)} panelId={restDisclosure.panelId} />
                )
              : null
          }
        />
      )}
      {top && rest > 0 && !problemsLink && (
        <Collapse open={showRest} id={restDisclosure.panelId}>
          <div className="mb-4 rounded-lg border border-theme-border bg-theme-base p-3">
            <ProblemList problems={row.problems.slice(1)} onNavigate={onNavigate} />
          </div>
        </Collapse>
      )}

      {actions && actions.length > 0 && (
        <div className="mb-4 flex flex-wrap gap-2">
          {actions.map((a) => (
            <button key={a.label} type="button" onClick={a.onClick} className={a.primary ? CNPG_PRIMARY_BUTTON : CNPG_SECONDARY_BUTTON}>
              {a.label}
            </button>
          ))}
        </div>
      )}

      <SummaryHeading>State</SummaryHeading>
      <FactGrid>
        <FactRow label="Controller phase">
          <span className="inline-flex flex-wrap items-center gap-2">
            <Badge severity={row.controllerStatus.level === 'healthy' ? 'success' : row.controllerStatus.level === 'unhealthy' ? 'error' : row.controllerStatus.level === 'degraded' || row.controllerStatus.level === 'alert' ? 'warning' : 'neutral'} size="sm">
              {row.controllerStatus.text}
            </Badge>
            <span className="text-xs text-theme-text-tertiary">
              {radarFindings ? 'reported by CNPG · Radar findings above are separate' : 'reported by CNPG'}
            </span>
          </span>
        </FactRow>
        <FactRow label="Instances">
          <div>
            <span>
              <ReadyCount row={row} /> ready
              {row.cluster?.status?.currentPrimary && !row.primaryConflict && (
                <span className="text-theme-text-secondary"> · primary <span className="font-mono">{row.cluster.status.currentPrimary}</span></span>
              )}
            </span>
            {row.primaryConflict && <PrimaryConflictNote conflict={row.primaryConflict} />}
            {row.pods.length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {row.pods.map((pod) => (
                  <InstancePill key={pod.name} pod={pod} namespace={ns} onNavigate={onNavigate} />
                ))}
              </div>
            )}
          </div>
        </FactRow>
        <FactRow label="Replication">
          <FactValue fact={row.replication} />
          <FactSource fact={row.replication} />
        </FactRow>
        {row.disk && (
          <FactRow label="Storage">
            <FactValue fact={row.disk} />
            <FactSource fact={row.disk} />
          </FactRow>
        )}
        {row.replicaCluster && (
          <FactRow label="Replica cluster">
            Follows {row.replicaCluster.source ? <span className="font-mono">{row.replicaCluster.source}</span> : 'an external primary'}
          </FactRow>
        )}
        <FactRow label="PostgreSQL">
          {row.pgVersion ?? 'Unknown'}
          {row.catalog && (
            <span className="text-theme-text-secondary">
              {' · '}
              <RefLink
                refTo={{ kind: row.catalog.kind, group: CNPG_GROUP, namespace: row.catalog.kind === 'ClusterImageCatalog' ? '' : ns, name: row.catalog.name }}
                onNavigate={onNavigate}
              >
                {row.catalog.name}
              </RefLink>
            </span>
          )}
        </FactRow>
        <FactRow label="Declarations">
          <FactValue fact={row.declarations.summary} />
        </FactRow>
        <FactRow label="Poolers">
          {row.poolers.length === 0 ? (
            <span className={row.poolersKnown ? 'text-theme-text-secondary' : 'text-theme-text-tertiary'}>
              {row.poolersKnown ? 'None' : 'No access to Poolers'}
            </span>
          ) : (
            <span className="flex flex-wrap gap-x-3">
              {row.poolers.map((name) => (
                <RefLink key={name} refTo={{ kind: 'Pooler', group: CNPG_GROUP, namespace: ns, name }} onNavigate={onNavigate} mono />
              ))}
            </span>
          )}
        </FactRow>
        {row.gitops && (
          <FactRow label="Declared in">
            {row.gitops.tool === 'argocd' ? 'Argo CD' : 'Flux'} <span className="font-mono">{row.gitops.name}</span>
          </FactRow>
        )}
        {stateFacts}
      </FactGrid>

      {haSection}

      <CNPGConnectSection cluster={row.cluster} poolers={row.poolerObjects} poolersKnown={row.poolersKnown} onNavigate={onNavigate} />

      <SummaryHeading>Protection</SummaryHeading>
      <FactGrid>
        <FactRow label="Schedule">
          <FactValue fact={p.schedule} />
          {p.schedule.names.length > 0 && (
            <div className="mt-0.5 flex flex-wrap gap-x-3 text-xs">
              {p.schedule.names.map((name) => (
                <RefLink key={name} refTo={{ kind: 'ScheduledBackup', group: CNPG_GROUP, namespace: ns, name }} onNavigate={onNavigate} mono />
              ))}
            </div>
          )}
        </FactRow>
        <FactRow label="Destination">
          {p.destination.objectStore ? (
            <RefLink refTo={{ kind: 'ObjectStore', group: CNPG_BARMAN_OBJECTSTORE_GROUP, namespace: ns, name: p.destination.objectStore }} onNavigate={onNavigate}>
              ObjectStore {p.destination.objectStore}
            </RefLink>
          ) : (
            <FactValue fact={p.destination} />
          )}
        </FactRow>
        <FactRow label="Last successful backup">
          <FactValue fact={p.lastSuccessfulBackup} />
          <FactSource fact={p.lastSuccessfulBackup} />
        </FactRow>
        <FactRow label="WAL archiving">
          <FactValue fact={p.walArchiving} />
        </FactRow>
        <FactRow label="Recovery window">
          {p.recoveryWindow.from ? (
            <div>
              <span className={p.recoveryWindow.tone === 'degraded' ? toneTextClass('degraded') : undefined}>
                {new Date(p.recoveryWindow.from).toUTCString().replace(' GMT', ' UTC')} → {p.recoveryWindow.to ? new Date(p.recoveryWindow.to).toUTCString().replace(' GMT', ' UTC') : 'unknown'}
              </span>
              <FactSource fact={p.recoveryWindow} />
              {p.recoveryWindow.tone === 'degraded' && (
                <div className="text-[11.5px] text-theme-text-tertiary">A backup failed after the last success, so this window is not advancing.</div>
              )}
            </div>
          ) : (
            <FactValue fact={p.recoveryWindow} />
          )}
        </FactRow>
        <FactRow label="Restore validation">
          {p.restoreValidation.restoredInto ? (
            <RefLink refTo={{ kind: 'Cluster', group: CNPG_GROUP, ...p.restoreValidation.restoredInto }} onNavigate={onNavigate}>
              {p.restoreValidation.text}
            </RefLink>
          ) : (
            <FactValue fact={p.restoreValidation} />
          )}
          <FactSource fact={p.restoreValidation} />
        </FactRow>
      </FactGrid>
      {extra}
    </div>
  )
}
