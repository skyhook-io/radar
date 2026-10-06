import { Fragment, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { classifyCNPGClusterPhase, cnpgBlockedPhaseExplanation, CNPG_BARMAN_OBJECTSTORE_GROUP, CNPG_GROUP } from '../resources/resource-utils-cnpg'
import { CNPG_NO_WAL_ARCHIVE_DESTINATION, cnpgClusterPlugins, cnpgPluginPhase, cnpgReadyInstances, type CNPGFleetRow, type CNPGInstance } from './workspace'
import type { CNPGDimension } from './ha'
import { PrimaryConflictNote } from './primitives'
import { Note } from './CNPGSharedSummary'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { StatusDot, toneTextClass } from '../ui/status-tone'
import { FactGrid, FactRow, FactSource, FactValue, ManagedByText, managedByLabel } from '../facts'
import { ProblemCallout, ProblemList } from '../problems'
import { formatAge } from '../resources/resource-utils'
import { FoldSection, SectionHeading } from '../ui/FoldSection'

function ReadyCount({ row }: { row: CNPGFleetRow }) {
  const r = cnpgReadyInstances(row)
  if (row.instances.ready === null) return <>Not reported by the operator{row.podReadiness && <span className="block text-xs text-theme-text-secondary">{row.podReadiness.ready}{row.instances.desired !== null ? ` of ${row.instances.desired}` : ''} instance Pods ready</span>}</>
  if (!r.note) return <>{r.text} ready</>
  return (
    <Tooltip content={r.note} position="top">
      <span className={clsx('font-medium', toneTextClass(r.tone ?? 'unknown'))}>
        {r.text} Pods ready <Badge severity="warning" size="sm">status says {row.instances.ready}</Badge>
      </span>
    </Tooltip>
  )
}

export interface CNPGSummaryAction {
  label: string
  onClick: () => void
  primary?: boolean
}

function InstancePill({ pod, namespace, onNavigate }: { pod: CNPGInstance; namespace: string; onNavigate?: NavigateToRef }) {
  const tone = pod.ready === true ? 'healthy' : pod.ready === false ? 'unhealthy' : 'unknown'
  const role = pod.role === 'primary' ? 'Primary' : pod.role === 'replica' ? 'Replica' : 'Role unknown'
  const readiness = pod.ready === true ? 'Pod ready' : pod.ready === false ? 'Pod not ready' : 'Pod readiness unknown'
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
        <StatusDot tone={tone} />
        <span className="font-mono">{pod.name}</span>
        <span className="text-theme-text-tertiary">{pod.role === 'primary' ? 'P' : pod.role === 'replica' ? 'R' : '?'}</span>
      </button>
    </Tooltip>
  )
}

/**
 * One dimension's state as a mark beside the tab that explains it: a dot when
 * something needs a look, a hollow ring when it could not be assessed (never a
 * calm colour), nothing when it is fine. The verdict is in the tooltip; the
 * Overview's At a glance has it in words.
 */
export function CNPGDimensionMark({ dimension }: { dimension: CNPGDimension }) {
  if (!dimensionMarked(dimension)) return null
  const label = `${dimension.label}: ${dimension.text}`
  return (
    <Tooltip content={<><div>{label}</div><div className="text-theme-text-tertiary">{dimension.source}</div></>} position="bottom">
      <span role="img" aria-label={label} className="inline-flex shrink-0">
        <DimensionGlyph tone={dimension.tone} />
      </span>
    </Tooltip>
  )
}

function dimensionMarked(dimension: CNPGDimension): boolean {
  return dimension.tone !== 'healthy' && dimension.tone !== 'neutral'
}

function DimensionGlyph({ tone }: { tone: CNPGDimension['tone'] }) {
  return tone === 'unknown' ? (
    <span aria-hidden className="inline-block h-[7px] w-[7px] shrink-0 rounded-full border border-theme-text-tertiary" />
  ) : (
    <StatusDot tone={tone} />
  )
}

/**
 * The verdict behind a tab's mark, as the tab's first line, so a mark always
 * points at words on the tab it marks. Nothing when the dimension is fine.
 */
export function CNPGDimensionVerdict({ dimension, className, alwaysShow = false }: { dimension: CNPGDimension; className?: string; alwaysShow?: boolean }) {
  if (!alwaysShow && !dimensionMarked(dimension)) return null
  return (
    <div className={clsx('flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm', className)}>
      <span className="inline-flex self-center">
        <DimensionGlyph tone={dimension.tone} />
      </span>
      <span className="text-theme-text-secondary">{dimension.label}</span>
      <span className={toneTextClass(dimension.tone)}>{dimension.text}</span>
      <span className="text-xs text-theme-text-tertiary">{dimension.source}</span>
    </div>
  )
}

/** Whether the cluster serves writes, on its title line: the headline the tabs do not carry. */
export function CNPGServingStatus({ dimension, onSelect }: { dimension: CNPGDimension; onSelect?: () => void }) {
  const body = (
    <>
      <StatusDot tone={dimension.tone} />
      <span className="text-theme-text-secondary">{dimension.label}</span>
      <span className={toneTextClass(dimension.tone)}>{dimension.text}</span>
    </>
  )
  const className = 'inline-flex items-center gap-1.5 whitespace-nowrap text-sm'
  return (
    <Tooltip content={dimension.source} position="bottom">
      {onSelect ? (
        <button
          type="button"
          onClick={onSelect}
          aria-label={`${dimension.label}: ${dimension.text}. Open its details`}
          className={clsx(className, 'rounded hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent')}
        >
          {body}
        </button>
      ) : (
        <span className={className}>{body}</span>
      )}
    </Tooltip>
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
  initialProblemsExpanded = false,
  stateFacts,
  operationalFacts,
  dimensionLinkLabel,
  onOpenOperator,
  framed = false,
}: {
  row: CNPGFleetRow
  onNavigate?: NavigateToRef
  actions?: CNPGSummaryAction[]
  /** Link to the complete list of this cluster's findings, shown when more than one exists. */
  problemsLink?: (count: number) => ReactNode
  extra?: ReactNode
  /** Rendered first, above the problem callout: standing states such as maintenance mode. */
  lead?: ReactNode
  /** Serving · Replication · Storage · Backups, each from its own source (see cnpgDimensions); the At a glance rows. */
  dimensions?: CNPGDimension[]
  /** Open with every problem listed below the callout (e.g. arriving from the fleet's "+N more"). */
  initialProblemsExpanded?: boolean
  /** Makes each dimension row open where that dimension is explained (its tab). */
  onSelectDimension?: (id: CNPGDimension['id']) => void
  /** The name of the place onSelectDimension opens, for the row's link; the dimension's own label when unset. */
  dimensionLinkLabel?: (id: CNPGDimension['id']) => string
  /** Opens the operator's own diagnosis, offered beside a controller phase that is not healthy. */
  onOpenOperator?: () => void
  /** Extra FactRows appended to About. */
  stateFacts?: ReactNode
  /** Extra FactRows appended to At a glance, e.g. live instance operations. */
  operationalFacts?: ReactNode
  /** Cards for the full page; drawers keep flat sections. */
  framed?: boolean
}) {
  const top = row.problems[0]
  const rest = row.problems.length - 1
  const ns = row.namespace
  const radarFindings = row.problems.some((x) => x.severity !== 'posture')
  const phase = typeof row.cluster?.status?.phase === 'string' ? row.cluster.status.phase : ''
  const blocked = classifyCNPGClusterPhase(phase) === 'terminal' ? cnpgBlockedPhaseExplanation(phase, row.cluster?.status?.phaseReason) : null
  const [showRest, setShowRest] = useState(initialProblemsExpanded)
  const restDisclosure = useDisclosure(showRest)
  const Frame = framed ? 'section' : Fragment
  const frameProps = framed ? { className: 'mb-4 last:mb-0 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm' } : {}

  return (
    <div className="px-4 py-4">
      {lead}
      {top && (
        <div className="[&_.mt-5]:mt-2"><ProblemCallout
          rootKind="Cluster"
          problem={top}
          onNavigate={onNavigate}
          more={
            rest > 0
              ? problemsLink?.(row.problems.length) ?? (
                  <MoreProblems count={rest} open={showRest} onToggle={() => setShowRest((v) => !v)} panelId={restDisclosure.panelId} />
                )
              : null
          }
        /></div>
      )}
      {top && rest > 0 && !problemsLink && (
        <Collapse open={showRest} id={restDisclosure.panelId}>
          <div className="mb-4 rounded-lg border border-theme-border bg-theme-base p-3">
            <ProblemList rootKind="Cluster" problems={row.problems.slice(1)} onNavigate={onNavigate} />
          </div>
        </Collapse>
      )}

      {actions && actions.length > 0 && (
        <div className="mb-4 flex flex-wrap gap-2">
          {actions.map((a) => (
            <button key={a.label} type="button" onClick={a.onClick} className={clsx(a.primary ? 'btn-brand' : 'btn-secondary', 'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-3 py-1.5 text-sm font-medium')}>
              {a.label}
            </button>
          ))}
        </div>
      )}

      <Frame {...frameProps}>
        <SectionHeading>At a glance</SectionHeading>
        <FactGrid>
          {dimensions?.map((d) => (
            <FactRow key={d.id} label={d.label}>
              <DimensionValue dimension={d} linkLabel={dimensionLinkLabel?.(d.id) ?? d.label} onOpen={onSelectDimension ? () => onSelectDimension(d.id) : undefined} />
            </FactRow>
          ))}
          <FactRow label="Instances">
            <div>
              <span>
                <ReadyCount row={row} />
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
          <FactRow label="Controller phase">
            <span className="inline-flex flex-wrap items-center gap-2">
              <Badge severity={'neutral'} size="sm">
                {phase || 'Not reported'}
              </Badge>
              <span className="text-xs text-theme-text-tertiary">
                {radarFindings ? 'reported by CNPG · Radar findings above are separate' : 'reported by CNPG'}
              </span>
              {onOpenOperator && (row.controllerStatus.level === 'unhealthy' || row.controllerStatus.level === 'degraded') && (
                <button type="button" onClick={onOpenOperator} className="text-xs text-accent-text hover:underline">
                  Operator and plugins →
                </button>
              )}
            </span>
            {blocked && (
              <div className="mt-0.5 text-[11.5px] text-theme-text-secondary">
                {blocked.body}
                {cnpgPluginPhase(row.cluster) &&
                  ` Plugins this cluster uses: ${cnpgClusterPlugins(row.cluster).join(', ') || 'none listed'}. The Operator view shows whether each is running and when it last restarted.`}
              </div>
            )}
          </FactRow>
          {operationalFacts}
        </FactGrid>
      </Frame>

      <Frame {...frameProps}>
        <SectionHeading>About</SectionHeading>
        <FactGrid>
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
          {row.managedBy && managedByLabel(row.managedBy) && (
            <FactRow label="Declared in">
              <ManagedByText refTo={row.managedBy} onNavigate={onNavigate} />
            </FactRow>
          )}
          {stateFacts}
        </FactGrid>
      </Frame>

      {extra}
    </div>
  )
}

function DimensionValue({ dimension: d, linkLabel, onOpen }: { dimension: CNPGDimension; linkLabel: string; onOpen?: () => void }) {
  return (
    <div>
      <span className="inline-flex flex-wrap items-center gap-x-2">
        <StatusDot tone={d.tone} />
        <span className={toneTextClass(d.tone)}>{d.text}</span>
        {onOpen && (
          <button type="button" onClick={onOpen} className="text-xs text-accent-text hover:underline">
            {linkLabel} →
          </button>
        )}
      </span>
      {d.source && <div className="text-[11.5px] text-theme-text-tertiary">{d.source}</div>}
    </div>
  )
}

/** A cluster's recovery evidence as facts: schedule, destination, newest backup, WAL archiving, recovery window and restore validation. */
export function CNPGClusterBackupFacts({ row, onNavigate }: { row: CNPGFleetRow; onNavigate?: NavigateToRef }) {
  const p = row.protection
  const ns = row.namespace
  return (
    <>
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
          <CNPGWALArchivingFact fact={p.walArchiving} />
        </FactRow>
        <FactRow label="Recovery window">
          {p.recoveryWindow.from ? (
            <div>
              <span className={p.recoveryWindow.tone === 'degraded' ? toneTextClass('degraded') : undefined}>
                from {new Date(p.recoveryWindow.from).toUTCString().replace(' GMT', ' UTC')} {p.recoveryWindow.tone === 'degraded' ? '· not advancing' : 'to the newest archived WAL'}
              </span>
              <FactSource fact={p.recoveryWindow} />
              {p.recoveryWindow.tone === 'degraded' && (
                <div className="text-[11.5px] text-theme-text-tertiary">WAL archiving is failing, so nothing written since the last archived WAL can be recovered.</div>
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

    </>
  )
}

export function CNPGWALArchivingFact({ fact, compact = false }: { fact: CNPGFleetRow['protection']['walArchiving']; compact?: boolean }) {
  const c = fact.operatorCondition
  return <div>
    <FactValue fact={fact} className="break-words" />
    <FactSource fact={fact} />
    {fact.detail && <Note>{compact && fact.text === CNPG_NO_WAL_ARCHIVE_DESTINATION ? 'No point-in-time recovery' : fact.detail}</Note>}
    {c && <div className="mt-1" onClick={(e) => e.stopPropagation()}>
      <FoldSection title="Operator report" summary="" attention={false}>
        <div className="break-words text-xs text-theme-text-secondary">{c.type}: {c.status}</div>
        <div className="break-words text-xs text-theme-text-secondary">{c.message || 'No message reported'}</div>
        <div className="text-xs text-theme-text-tertiary">Last transition: {c.lastTransitionTime ? <Tooltip content={c.lastTransitionTime}><time dateTime={c.lastTransitionTime}>{formatAge(c.lastTransitionTime)} ago</time></Tooltip> : 'Not reported'}</div>
      </FoldSection>
    </div>}
  </div>
}
