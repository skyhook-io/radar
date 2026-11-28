import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { ArrowRight, ChevronDown, Workflow } from 'lucide-react'
import {
  Badge,
  OpenIssueContext,
  PaneLoader,
  Tooltip,
  WorkspaceProblemCallout,
  formatRelativeAgeTime,
  getFreightAlias,
  getFreightVersions,
  formatFreightVersion,
  getFreightStageRecords,
  prefersReducedMotion,
  sortFreightNewestFirst,
  toneTextClass,
  type HealthLevel,
  type KargoFreight,
} from '@skyhook-io/k8s-ui'
import { ToneBadge } from '@skyhook-io/k8s-ui/components/resources/renderers/KargoShared'
import type { SelectedResource } from '../../types'
import type { KargoFreightRefView, KargoProjectResponse, KargoPromotionView, KargoStageView } from '../../api/kargo'
import { useNavCustomization } from '../../context/NavCustomization'
import { issuesPathForSubject } from '../../utils/page-links'
import { refToSelectedResource } from '../../utils/navigation'
import { Notice, RefreshFailedNotice, ScreenBody, ScreenEmptyState, SectionTable } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { KARGO_GROUP, KARGO_STAGE_PARAM } from './routes'
import {
  kargoIssueProblem,
  lagTone,
  pipelineColumns,
  promotionFailure,
  promotionTone,
  stageHealth,
  stagePhaseTone,
  stageVerification,
  warehouseError,
  warehouseStatus,
} from './presentation'

type Inspect = (resource: SelectedResource) => void

const RECENT_FREIGHT = 10

const kargoRef = (kind: 'stages' | 'freights' | 'promotions' | 'warehouses', namespace: string, name: string): SelectedResource => ({ kind, group: KARGO_GROUP, namespace, name })

export function KargoProject({ query, onInspect, inspected, notice }: {
  query: Pick<UseQueryResult<KargoProjectResponse>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>
  onInspect: Inspect
  inspected: SelectedResource | null
  /** Shown above everything else, such as why another project than the URL's is shown. */
  notice?: ReactNode
}) {
  const data = query.data
  if (!data && query.isLoading) return <PaneLoader label="Loading project…" className="flex-1" />
  if (!data) {
    return (
      <ScreenEmptyState
        icon={Workflow}
        title="Project unavailable"
        detail={query.error instanceof Error ? query.error.message : 'Radar could not load this Kargo project.'}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
      />
    )
  }
  return (
    <ScreenBody>
      {notice}
      <RefreshFailedNotice queries={[query]} />
      <ReadNotices data={data} />
      <Problems data={data} onInspect={onInspect} />
      <Pipeline data={data} onInspect={onInspect} />
      <Warehouses data={data} onInspect={onInspect} inspected={inspected} />
      <RecentFreight data={data} onInspect={onInspect} inspected={inspected} />
    </ScreenBody>
  )
}

function ReadNotices({ data }: { data: KargoProjectResponse }) {
  const stages = data.coverage.stages?.state
  const freight = data.coverage.freight?.state
  return (
    <>
      {stages !== 'full' && stages !== 'partial' && (
        <Notice>{coverageEmpty(data.coverage.stages, 'stages')} The pipeline can’t be shown.</Notice>
      )}
      {(stages === 'full' || stages === 'partial') && !data.freightRead && (
        <Notice>
          {freight === 'denied'
            ? 'You can’t read Freight in this project, so version names (aliases) and how long each stage has held its version aren’t shown. The versions themselves come from the stages.'
            : `${coverageEmpty(data.coverage.freight, 'Freight')} Version names (aliases) and how long each stage has held its version aren’t shown.`}
        </Notice>
      )}
    </>
  )
}

function Problems({ data, onInspect }: { data: KargoProjectResponse; onInspect: Inspect }) {
  const navigate = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const issuesTakenOver = !!useNavCustomization().fleetTakeoverHref?.('issues')
  if (data.issues.length === 0) return null
  const problems = data.issues.map(kargoIssueProblem)
  return (
    <OpenIssueContext.Provider value={issuesTakenOver ? undefined : (p) => navigate(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="space-y-3">
        {problems.map((problem) => (
          <WorkspaceProblemCallout key={problem.id} problem={problem} rootKind="Project" onNavigate={(ref) => onInspect(refToSelectedResource(ref))} />
        ))}
      </div>
    </OpenIssueContext.Provider>
  )
}

const STAGE_FLASH_MS = 1600

const stageCardId = (project: string, stage: string) => `kargo-stage-${project}-${stage}`

/** Scrolls the URL's Stage (?stage=) into view and flashes it, once per project and Stage, not on every refetch. */
function useFocusedStage(data: KargoProjectResponse): string | undefined {
  const [params] = useSearchParams()
  const requested = params.get(KARGO_STAGE_PARAM)
  const present = !!requested && data.stages.some((s) => s.name === requested)
  const handled = useRef<string | null>(null)
  const [flashing, setFlashing] = useState<string>()
  useEffect(() => {
    const key = present ? `${data.name}/${requested}` : null
    if (!key || !requested || handled.current === key) return
    handled.current = key
    document.getElementById(stageCardId(data.name, requested))?.scrollIntoView({ block: 'center', inline: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' })
    setFlashing(requested)
  }, [data.name, requested, present])
  useEffect(() => {
    if (!flashing) return
    const timer = window.setTimeout(() => setFlashing(undefined), STAGE_FLASH_MS)
    return () => window.clearTimeout(timer)
  }, [flashing])
  return flashing
}

function Pipeline({ data, onInspect }: { data: KargoProjectResponse; onInspect: Inspect }) {
  const columns = pipelineColumns(data.stages)
  const flashing = useFocusedStage(data)
  if (columns.length === 0) {
    if (data.coverage.stages?.state !== 'full' && data.coverage.stages?.state !== 'partial') return null
    return <section><h2 className="mb-2 text-sm font-semibold text-theme-text-primary">Pipeline</h2><div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-5 text-sm text-theme-text-tertiary">This project has no stages yet.</div></section>
  }
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-x-2.5">
        <h2 className="text-sm font-semibold text-theme-text-primary">Pipeline</h2>
        <span className="text-xs text-theme-text-tertiary">Stages in promotion order, from where versions enter to where they end up</span>
      </div>
      <div className="flex flex-col gap-3 md:flex-row md:items-start md:overflow-x-auto md:pb-2">
        {columns.map((column, i) => (
          <Fragment key={column[0].name}>
            {i > 0 && (
              <div className="flex shrink-0 items-center justify-center text-theme-text-tertiary md:self-center" aria-hidden>
                <ArrowRight className="hidden h-4 w-4 md:block" />
                <ChevronDown className="h-4 w-4 md:hidden" />
              </div>
            )}
            <div className="flex min-w-0 flex-col gap-3 md:w-[19rem] md:shrink-0">
              {column.map((stage) => (
                <StageCard key={stage.name} project={data.name} stage={stage} freightRead={data.freightRead} onInspect={onInspect} flash={flashing === stage.name} />
              ))}
            </div>
          </Fragment>
        ))}
      </div>
    </section>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-2 py-1.5 text-sm">
      <div className="text-xs leading-5 text-theme-text-tertiary">{label}</div>
      <div className="min-w-0 text-theme-text-primary">{children}</div>
    </div>
  )
}

function LinkButton({ onClick, children, className }: { onClick: () => void; children: ReactNode; className?: string }) {
  return (
    <button type="button" onClick={onClick} className={clsx('text-left text-accent-text hover:underline', className)}>
      {children}
    </button>
  )
}

function Versions({ versions }: { versions: string[] }) {
  if (versions.length === 0) return <span className="text-theme-text-tertiary">No versions recorded</span>
  return (
    <span className="flex flex-wrap gap-1">
      {versions.map((v) => <Badge key={v} tone="structural" size="sm">{v}</Badge>)}
    </span>
  )
}

function FreightRef({ project, freight, freightRead, onInspect }: { project: string; freight: KargoFreightRefView; freightRead: boolean; onInspect: Inspect }) {
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-baseline gap-x-2">
        {freight.alias ? (
          <LinkButton onClick={() => onInspect(kargoRef('freights', project, freight.name))} className="font-medium">{freight.alias}</LinkButton>
        ) : (
          <Tooltip content={freightRead ? 'Kargo has not named this Freight.' : 'Shown when you can read Freight in this project.'}>
            <LinkButton onClick={() => onInspect(kargoRef('freights', project, freight.name))} className="font-mono text-xs">{freight.name.slice(0, 7)}</LinkButton>
          </Tooltip>
        )}
      </div>
      <Versions versions={freight.versions} />
    </div>
  )
}

function Since({ freight, freightRead }: { freight: KargoFreightRefView; freightRead: boolean }) {
  if (freight.since) return <span className="text-xs text-theme-text-tertiary">since {formatRelativeAgeTime(freight.since)}</span>
  return (
    <Tooltip content={freightRead ? 'Kargo hasn’t recorded when this stage received it.' : 'Shown when you can read Freight in this project.'}>
      <span className="text-xs text-theme-text-tertiary">since unknown</span>
    </Tooltip>
  )
}

function ToneText({ tone, text, detail }: { tone: HealthLevel; text: string; detail?: string }) {
  return (
    <div className="min-w-0">
      <ToneBadge tone={tone} size="sm">{text}</ToneBadge>
      {detail && <Tooltip content={detail}><div className="mt-0.5 line-clamp-3 break-words text-xs text-theme-text-secondary">{detail}</div></Tooltip>}
    </div>
  )
}

function PromotionFailure({ promotion }: { promotion: KargoPromotionView }) {
  const failure = promotionFailure(promotion)
  return (
    <>
      <Tooltip content={failure.full}><div className={clsx('mt-0.5 line-clamp-6 break-words text-xs', toneTextClass('unhealthy'))}>{failure.cause}</div></Tooltip>
      {failure.step && <div className="mt-0.5 break-words text-xs text-theme-text-tertiary">At step {failure.step}</div>}
    </>
  )
}

function StageCard({ project, stage, freightRead, onInspect, flash }: { project: string; stage: KargoStageView; freightRead: boolean; onInspect: Inspect; flash: boolean }) {
  const health = stageHealth(stage)
  const verification = stageVerification(stage)
  const last = stage.lastPromotion
  const running = stage.currentPromotion
  const multiOrigin = stage.current.length > 1 || stage.whyNotHere.length > 1
  return (
    <article id={stageCardId(project, stage.name)} className={clsx('rounded-xl border border-theme-border bg-theme-surface px-3.5 py-3 shadow-theme-sm', flash && 'reach-flash')}>
      <header className="mb-1 flex items-center justify-between gap-2">
        <LinkButton onClick={() => onInspect(kargoRef('stages', project, stage.name))} className="truncate text-sm font-semibold !text-theme-text-primary">
          {stage.name}
        </LinkButton>
        <ToneBadge tone={stagePhaseTone(stage.phase)} size="sm">{stage.phase}</ToneBadge>
      </header>
      {stage.missingUpstream && stage.missingUpstream.length > 0 && (
        <div className="mb-1 text-xs text-theme-text-tertiary">Also takes versions from {stage.missingUpstream.join(', ')}, which Radar can’t read.</div>
      )}
      <div className="divide-y divide-theme-border-subtle">
        <Row label="Version">
          {stage.current.length === 0 ? (
            <span className="text-theme-text-tertiary">No version yet</span>
          ) : (
            <div className="space-y-2">
              {stage.current.map((f) => (
                <div key={f.origin + f.name} className="space-y-0.5">
                  {multiOrigin && <div className="text-xs text-theme-text-tertiary">From {f.origin}</div>}
                  <FreightRef project={project} freight={f} freightRead={freightRead} onInspect={onInspect} />
                  <Since freight={f} freightRead={freightRead} />
                </div>
              ))}
            </div>
          )}
        </Row>
        <Row label="Health"><ToneText {...health} /></Row>
        <Row label="Verification">
          <ToneText {...verification} />
          {stage.verification?.analysisRun && (
            <LinkButton onClick={() => onInspect({ kind: 'analysisruns', group: 'argoproj.io', namespace: stage.verification!.analysisRun!.namespace, name: stage.verification!.analysisRun!.name })} className="mt-0.5 text-xs">
              Open analysis run
            </LinkButton>
          )}
        </Row>
        <Row label="Last promotion">
          {running && (
            <div className="mb-1 flex flex-wrap items-center gap-1.5">
              <ToneBadge tone="neutral" size="sm">Promoting now</ToneBadge>
              <LinkButton onClick={() => onInspect(kargoRef('promotions', project, running.name))} className="text-xs">Open</LinkButton>
            </div>
          )}
          {last ? (
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-1.5">
                <ToneBadge tone={promotionTone(last.phase)} size="sm">{last.phase || 'Unknown'}</ToneBadge>
                {last.finishedAt && <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(last.finishedAt)}</span>}
                <LinkButton onClick={() => onInspect(kargoRef('promotions', project, last.name))} className="text-xs">Open</LinkButton>
              </div>
              {(last.phase === 'Failed' || last.phase === 'Errored') && <PromotionFailure promotion={last} />}
            </div>
          ) : !running && <span className="text-theme-text-tertiary">None yet</span>}
        </Row>
        <Row label="Newest version">
          <div className="space-y-2">
            {stage.whyNotHere.length === 0 && <span className="text-theme-text-tertiary">This stage doesn’t request any versions.</span>}
            {stage.whyNotHere.map((lag) => (
              <div key={lag.origin} className="space-y-1">
                {multiOrigin && <div className="text-xs text-theme-text-tertiary">From {lag.origin}</div>}
                {lag.state === 'current' ? (
                  <span className={clsx('text-sm', toneTextClass('healthy'))}>{lag.reason === 'nothingUpstream' ? 'Nothing newer upstream yet' : 'Already here'}</span>
                ) : (
                  <>
                    {lag.newest && (
                      <div className="flex flex-wrap items-center gap-1.5 text-xs text-theme-text-secondary">
                        <span>Not here yet:</span>
                        <LinkButton onClick={() => onInspect(kargoRef('freights', project, lag.newest!.name))}>{lag.newest.alias ?? lag.newest.name.slice(0, 7)}</LinkButton>
                        <Versions versions={lag.newest.versions} />
                      </div>
                    )}
                    <div className={clsx('break-words text-xs', toneTextClass(lagTone(lag)))}>
                      {lag.state === 'unknown' ? `Why: unknown. ${lag.message}` : lag.message}
                    </div>
                    {lag.analysisRuns?.map((run) => (
                      <LinkButton key={`${run.namespace}/${run.name}`} onClick={() => onInspect({ kind: 'analysisruns', group: 'argoproj.io', namespace: run.namespace, name: run.name })} className="block text-xs">
                        Open analysis run
                      </LinkButton>
                    ))}
                  </>
                )}
              </div>
            ))}
          </div>
        </Row>
        {stage.argoCDApps && stage.argoCDApps.length > 0 && (
          <Row label="Argo CD apps">
            <ul className="space-y-0.5">
              {stage.argoCDApps.map((app) => (
                <li key={`${app.namespace}/${app.name}`} className="flex flex-wrap items-center gap-1.5">
                  <LinkButton onClick={() => onInspect({ kind: 'applications', group: 'argoproj.io', namespace: app.namespace, name: app.name })} className="truncate font-mono text-xs">{app.name}</LinkButton>
                  {app.health && <span className="text-xs text-theme-text-tertiary">{app.health}{app.sync ? `, ${app.sync}` : ''}</span>}
                </li>
              ))}
            </ul>
          </Row>
        )}
      </div>
    </article>
  )
}

function Warehouses({ data, onInspect, inspected }: { data: KargoProjectResponse; onInspect: Inspect; inspected: SelectedResource | null }) {
  const read = data.coverage.warehouses?.state === 'full' || data.coverage.warehouses?.state === 'partial'
  return (
    <SectionTable
      title="Warehouses"
      subtitle="Where new versions come from"
      rows={data.warehouses}
      rowKey={(w) => w.name}
      rowResource={(w) => kargoRef('warehouses', data.name, w.name)}
      onInspect={onInspect}
      inspected={inspected}
      empty={read ? 'This project has no warehouses.' : coverageEmpty(data.coverage.warehouses, 'warehouses')}
      minWidth={640}
      columns={[
        { header: 'Name', width: '22%', cell: (w) => <span className="font-medium">{w.name}</span> },
        {
          header: 'Status',
          width: '30%',
          cell: (w) => {
            const status = warehouseStatus(w)
            const error = warehouseError(w)
            return <ToneText tone={status.level} text={status.text} detail={error} />
          },
        },
        {
          header: 'Newest version',
          cell: (w) => w.latest
            ? <div><div className="text-sm">{w.latest.alias ?? w.latest.name.slice(0, 7)}</div><Versions versions={w.latest.versions} /></div>
            : <span className="text-theme-text-tertiary">{data.freightRead ? 'None yet' : 'Shown when you can read Freight'}</span>,
        },
        { header: 'Freight', width: '12%', cell: (w) => data.freightRead ? w.freightCount : <span className="text-theme-text-tertiary">Unknown</span> },
      ]}
    />
  )
}

function RecentFreight({ data, onInspect, inspected }: { data: KargoProjectResponse; onInspect: Inspect; inspected: SelectedResource | null }) {
  const rows = sortFreightNewestFirst(data.freight).slice(0, RECENT_FREIGHT)
  const subtitle = data.freightTotal > rows.length ? `Newest ${rows.length} of ${data.freightTotal}` : 'Newest first'
  return (
    <SectionTable<KargoFreight>
      title="Recent Freight"
      subtitle={subtitle}
      rows={rows}
      rowKey={(f) => f.metadata?.name ?? ''}
      rowResource={(f) => kargoRef('freights', data.name, f.metadata?.name ?? '')}
      onInspect={onInspect}
      inspected={inspected}
      empty={data.freightRead ? 'No Freight in this project yet.' : coverageEmpty(data.coverage.freight, 'Freight')}
      minWidth={680}
      columns={[
        { header: 'Name', width: '22%', cell: (f) => <span className="font-medium">{getFreightAlias(f) ?? f.metadata?.name?.slice(0, 7)}</span> },
        { header: 'Versions', cell: (f) => <Versions versions={getFreightVersions(f).map(formatFreightVersion)} /> },
        { header: 'From', width: '16%', cell: (f) => f.origin?.name ?? <span className="text-theme-text-tertiary">Unknown</span> },
        {
          header: 'In stages',
          width: '18%',
          cell: (f) => {
            const stages = getFreightStageRecords(f).currentlyIn.map((r) => r.stage)
            return stages.length ? stages.join(', ') : <span className="text-theme-text-tertiary">None</span>
          },
        },
        { header: 'Found', width: '12%', cell: (f) => formatRelativeAgeTime(f.discoveredAt ?? f.metadata?.creationTimestamp) },
      ]}
    />
  )
}
