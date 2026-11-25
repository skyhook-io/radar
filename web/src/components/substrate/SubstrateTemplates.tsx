import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Bot, FileCode, LayoutDashboard, RefreshCw } from 'lucide-react'
import { Badge, CertaintyGlyph, DetailShell, DimensionValue, FactGrid, FactRow, FoldSection, Input, PaneLoader, SelectMenu, Tooltip, WorkspaceCard, WorkspaceProblemCallout, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateTemplate,
  useSubstrateWorkspace,
  useSubstrateTemplates,
  type SubstrateRef,
  type SubstrateStatus,
  type SubstrateTemplateRow,
  type SubstrateTemplateSpec,
  type SubstrateWorkspace,
} from '../../api/substrateWorkspace'
import { Notice, RefreshFailedNotice, ScreenBody, ScreenEmptyState, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { useConnection } from '../../context/ConnectionContext'
import { NotInContext, useContextPin } from './contextPin'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { type OpenSubject } from './SubstrateAttentionList'
import { SubstrateConnectNotice, SubstrateTierBanner } from './SubstrateConnect'
import { attentionTone, memoryRebuildStep, templateYamlCommand, bytesText, coverageInPart, coverageReason, cpuText, definitiveFailure, withoutReasonCode, fidelityLabel, goldenReading, splitReading, templateFilterValue, tierReading } from './readings'
import { SUBSTRATE_PATH, SUBSTRATE_SCREENS, substrateActorsPath, substrateTemplatePath } from './routes'
import { CommandChip, Name, PoolMatchLinks, SUBSTRATE_TERMS, SUBSTRATE_WIDE, SubstrateCoverageNotice, SubstrateHeader, SubstrateSourceLine, Term, Unread, When, WholeNames } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'

/** `named` says what the chip is about, for a place where it stands alone (a page header), not under a Golden snapshot column. */
export function GoldenBadge({ template, named }: { template: Pick<SubstrateTemplateRow, 'golden' | 'matchingPools' | 'matchingPoolsComplete'>; named?: boolean }) {
  const g = goldenReading(template)
  const severity = template.matchingPoolsComplete && template.matchingPools?.length === 0 ? 'alert' : g.severity
  const label = named && !g.label.startsWith('Can’t') ? `Golden snapshot ${g.label.toLowerCase()}` : g.label
  // Standing alone, the chip is where a page first names the term: its tooltip defines it first.
  const tip = named ? `Golden snapshot: ${SUBSTRATE_TERMS.golden.charAt(0).toLowerCase()}${SUBSTRATE_TERMS.golden.slice(1)} ${g.meaning}` : g.meaning
  return <Tooltip content={tip}><span><Badge severity={severity} size="sm" className="whitespace-normal text-left">{label}</Badge></span></Tooltip>
}

function PoolsCell({ t }: { t: SubstrateTemplateRow }) {
  if (t.matchingPools === null) return <Unread title="Radar could not read enough of the pools to determine matches.">Unknown</Unread>
  return <Tooltip content={t.matchingPools.map((p) => `${p.namespace}/${p.name}`).join(', ') || 'No pool labels match'}>
    <span>{t.matchingPoolsComplete ? '' : '≥'}{t.matchingPools.length} {t.matchingPools.length === 1 ? 'pool' : 'pools'}</span>
  </Tooltip>
}

function resourcesText(t: Pick<SubstrateTemplateRow, 'cpuMillicores' | 'memoryBytes'>): string {
  return `${t.cpuMillicores !== undefined ? `${cpuText(t.cpuMillicores)} CPU` : 'CPU unreported'} · ${t.memoryBytes !== undefined ? bytesText(t.memoryBytes) : 'memory unreported'}`
}

function templateRank(t: SubstrateTemplateRow): number {
  if (t.matchingPoolsComplete && t.matchingPools?.length === 0) return 0
  if (t.golden.state === 'failed') return 1
  if (t.attentionCount) return 2
  return 3
}

export function SubstrateTemplatesScreen({ workspace, status }: { workspace?: SubstrateWorkspace; status?: SubstrateStatus }) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const [search, setSearch] = useState('')
  const [atespace, setAtespace] = useState('')
  const [filter, setFilter] = useState<'all' | 'attention' | 'ready'>('all')
  const tier = workspace?.apiTier
  const connected = tier?.state === 'connected'
  const query = useSubstrateTemplates(connected)
  const data = query.data
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const actorsPartial = coverageInPart(data?.coverage.actors) || coverageInPart(data?.coverage.workers)
  const open = (t: SubstrateRef) => navigate(substrateTemplatePath(t, connection.context || undefined))
  const atespaces = [...new Set(data?.templates.map((t) => t.atespace))].sort()
  const scoped = (data?.templates ?? []).filter((t) => (!atespace || t.atespace === atespace) && templateFilterValue(t).toLowerCase().includes(search.trim().toLowerCase()))
  const needsAttention = (t: SubstrateTemplateRow) => templateRank(t) < 3
  const ready = (t: SubstrateTemplateRow) => t.golden.state === 'ready' && !needsAttention(t)
  const partial = coverageInPart(data?.coverage.templates)
  const verdictsPartial = actorsPartial || !!data && (INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state) || data.templates.some((t) => !t.matchingPoolsComplete))
  const count = (n: number, inPart: boolean) => inPart ? n > 0 ? `≥${n}` : undefined : n
  const rows = scoped.filter((t) => filter === 'all' || (filter === 'attention' ? needsAttention(t) : ready(t)))
    .sort((a, b) => templateRank(a) - templateRank(b) || (b.attentionCount ?? 0) - (a.attentionCount ?? 0) || templateFilterValue(a).localeCompare(templateFilterValue(b)))
  const columns: TableColumn<SubstrateTemplateRow>[] = [
    {
      header: 'Template',
      width: '22%',
      cell: (t) => (
        <>
          <Name name={t.name} />
          <Sub><WholeNames text={t.atespace} /></Sub>
        </>
      ),
    },
    {
      header: <Term term="golden">Startup snapshot</Term>,
      width: wide ? '24%' : '26%',
      cell: (t) => (
        <>
          <GoldenBadge template={t} />
          {t.golden.error ? (
            <Tooltip content={t.golden.error} wrapperClassName="max-w-full">
              <Sub><span className="line-clamp-3 break-words">{withoutReasonCode(t.golden.error)}</span></Sub>
            </Tooltip>
          ) : t.golden.time ? (
            <Sub><When iso={t.golden.time} /></Sub>
          ) : null}
        </>
      ),
    },
    {
      header: 'Actors needing attention',
      width: '19%',
      cell: (t) => t.attentionCount === undefined || (actorsPartial && t.attentionCount === 0)
        ? <Unread>Unknown</Unread>
        : t.attentionCount > 0
          ? <button type="button" onClick={(e) => { e.stopPropagation(); navigate(substrateActorsPath({ template: templateFilterValue(t), attention: '1' })) }} className="text-accent-text hover:underline">
              {actorsPartial && <CertaintyGlyph certainty="lower_bound" title="Some actors or workers were not read" />}{t.attentionCount} →</button>
          : '0',
    },
    { header: 'Label-matched pools', width: wide ? '16%' : '14%', cell: (t) => <PoolsCell t={t} /> },
    { header: 'Per-actor resources', width: '15%', cell: (t) => resourcesText(t) },
    { header: <span className="sr-only">Open template</span>, width: '76px', cell: (t) =>
      <button type="button" aria-label={`Open template ${t.name}`} className="whitespace-nowrap text-xs text-accent-text hover:underline" onClick={(e) => { e.stopPropagation(); open(t) }}>Open →</button> },
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Templates" intro="Cluster-wide · all atespaces" />
      <ScreenBody>
        {!connected && tier ? (
          <>
            <SubstrateTierBanner tier={tier} />
            <SubstrateConnectNotice status={status} tier={tier} />
          </>
        ) : query.isLoading ? (
          <PaneLoader label="Loading templates…" />
        ) : !data ? (
          <Notice>Radar couldn’t read the templates: {query.error instanceof Error ? query.error.message : 'unknown error'}.</Notice>
        ) : (
          <>
            <RefreshFailedNotice queries={[query]} />
            <SubstrateCoverageNotice coverage={data.coverage} kinds={['templates', 'actors', 'workers']} />
            <div className="flex flex-wrap items-center gap-2">
              <Segments label="Template filter" value={filter} onChange={setFilter} options={[
                { id: 'all', label: 'All', count: count(scoped.length, partial) },
                { id: 'attention', label: 'Needs attention', count: count(scoped.filter(needsAttention).length, partial || verdictsPartial) },
                { id: 'ready', label: 'Ready', count: verdictsPartial ? undefined : count(scoped.filter(ready).length, partial) },
              ]} />
              <Input aria-label="Search templates" placeholder="Search templates…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 max-w-52 rounded-md border border-theme-border bg-theme-elevated px-3" />
              <SelectMenu ariaLabel="Atespace" value={atespace} onChange={setAtespace} options={[{ value: '', label: 'Every atespace' }, ...atespaces.map((value) => ({ value, label: value }))]} />
            </div>
            <SectionTable
              columns={columns}
              rows={rows}
              rowKey={(t) => `${t.atespace}/${t.name}`}
              rowResource={(t) => ({ kind: 'SubstrateTemplate', namespace: t.atespace, name: t.name })}
              onInspect={(res) => open({ atespace: res.namespace, name: res.name })}
              empty={data.templates.length && !rows.length ? 'No templates match these filters.' : coverageInPart(data.coverage.templates) ? `No templates: ${coverageReason(data.coverage.templates, 'templates')}.` : 'No templates.'}
              minWidth={wide ? 900 : 720}
            />
          </>
        )}
        {tier && <SubstrateSourceLine tier={tier} status={status} />}
      </ScreenBody>
    </div>
  )
}

/**
 * What the template runs, read-only: its containers and images, volumes and
 * snapshot settings. Env values Radar deems secret arrive redacted. A
 * template is immutable, so this is exactly what each of its actors runs.
 */
function TemplateSpec({ spec, unavailable, template }: { spec?: SubstrateTemplateSpec; unavailable?: string; template: SubstrateRef }) {
  return (
    <div>
      {!spec ? (
        <p className="text-sm text-theme-text-tertiary">Radar couldn’t read the spec{unavailable ? `: ${unavailable}` : ''}.</p>
      ) : (
        <div className="space-y-3">
          <FactGrid>
            {spec.createTime && <FactRow label="Created"><When iso={spec.createTime} /></FactRow>}
            <FactRow label="Snapshots">
              {fidelityLabel(spec.snapshot.preferredFidelity)} · stored in {spec.snapshot.storageLocation ? <span className="font-mono">{spec.snapshot.storageLocation}</span> : 'the API server’s default location'} · golden snapshot {spec.snapshot.golden}
              <div className="text-xs text-theme-text-tertiary">The most a snapshot of its actors holds: a ceiling, not a guarantee. An actor’s own snapshot can hold less; its Snapshots tab says what each holds.</div>
            </FactRow>
            {spec.goldenEgressRules !== undefined && <FactRow label="Warm-up egress">{spec.goldenEgressRules} {spec.goldenEgressRules === 1 ? 'rule' : 'rules'} while the golden actor warms up</FactRow>}
            <FactRow label="Volumes">
              {spec.volumes.length ? spec.volumes.map((v) => `${v.name} (${v.kind}${v.image ? ` ${v.image}` : ''})`).join(', ') : <span className="text-theme-text-secondary">none</span>}
            </FactRow>
          </FactGrid>
          <div className="space-y-2">
            {spec.containers.map((c) => (
              <div key={c.name} className="card-inner">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-mono text-sm font-medium text-theme-text-primary">{c.name}</span>
                  <span className="min-w-0 break-all font-mono text-xs text-theme-text-secondary">{c.image}</span>
                </div>
                <dl className="mt-1 grid grid-cols-[7rem_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-xs">
                  {c.command?.length ? <><dt className="text-theme-text-tertiary">Command</dt><dd className="break-all font-mono">{c.command.join(' ')}</dd></> : null}
                  {c.args?.length ? <><dt className="text-theme-text-tertiary">Args</dt><dd className="break-all font-mono">{c.args.join(' ')}</dd></> : null}
                  {c.env?.length ? (
                    <>
                      <dt className="text-theme-text-tertiary">Env</dt>
                      <dd className="break-all font-mono">{c.env.map((e) => <div key={e.name}>{e.name}={e.redacted ? '(redacted)' : e.value}</div>)}</dd>
                    </>
                  ) : null}
                  {c.volumeMounts?.length ? <><dt className="text-theme-text-tertiary">Mounts</dt><dd className="break-all font-mono">{c.volumeMounts.map((m) => `${m.name} → ${m.mountPath}`).join(', ')}</dd></> : null}
                  {c.wakeupProbe ? <><dt className="text-theme-text-tertiary">Wake-up probe</dt><dd className="font-mono">{c.wakeupProbe}</dd></> : null}
                </dl>
              </div>
            ))}
          </div>
        </div>
      )}
      <p className="mt-3 text-xs text-theme-text-tertiary">The template as Substrate stores it:</p>
      <CommandChip command={templateYamlCommand(template)} />
    </div>
  )
}

/** A template's page: its golden snapshot, the actors of it by state, and the pools that can run it. */
export function SubstrateTemplatePage({ template, onOpenSubject }: { template: SubstrateRef; onOpenSubject: OpenSubject }) {
  const navigate = useWorkspaceNavigate()
  const pin = useContextPin()
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'spec' ? 'spec' : 'overview'
  const query = useSubstrateTemplate(pin.mismatch ? null : template)
  // The same read the workspace frame holds; shared, not repeated.
  const workspace = useSubstrateWorkspace(!pin.mismatch)
  const code = substrateErrorCode(query.error)
  // A refresh that says the template is gone or no longer readable replaces what was kept.
  const t = definitiveFailure(code) ? undefined : query.data?.template
  const crumb = (
    <nav aria-label="Location" className="flex flex-wrap items-center gap-1.5 text-sm text-theme-text-tertiary">
      <Bot className="h-3.5 w-3.5" />
      <button type="button" onClick={() => navigate(SUBSTRATE_PATH)} className="hover:text-theme-text-primary hover:underline">Agent Substrate</button>
      <span>/</span>
      <button type="button" onClick={() => navigate(SUBSTRATE_SCREENS.templates.path)} className="hover:text-theme-text-primary hover:underline">Templates</button>
      <span>/</span>
    </nav>
  )
  if (pin.mismatch) {
    return <NotInContext name={template.name} what={`template ${template.atespace} / ${template.name}`} pinnedContext={pin.pinned!} activeContext={pin.active} />
  }
  if (!t && query.isLoading) return <PaneLoader label={`Loading ${template.name}…`} className="flex-1" />
  if (!t) {
    const tier = code && code !== 'notFound' && code !== 'denied' ? tierReading({ state: 'failed', code, reason: query.error instanceof Error ? query.error.message : undefined }) : undefined
    const title = code === 'notFound' ? `Substrate has no template ${template.name}` : code === 'denied' ? `Substrate denied reading ${template.name}` : tier?.title ?? `${template.name} couldn’t be read`
    const detail = code === 'notFound'
      ? `There is no template ${template.name} in atespace ${template.atespace}. It may have been deleted since this link was made.`
      : tier
        ? `${tier.detail}${tier.nextStep ? ` ${tier.nextStep}` : ''}`
        : query.error instanceof Error ? query.error.message : 'Radar could not read the template.'
    return (
      <ScreenEmptyState
        icon={Bot}
        title={title}
        detail={detail}
        action={<button type="button" onClick={() => navigate(SUBSTRATE_SCREENS.templates.path)} className="btn-secondary mt-3 px-3 py-1.5 text-sm">Go to Templates</button>}
      />
    )
  }
  const golden = goldenReading(t)
  const declaredNoPool = t.matchingPoolsComplete && t.matchingPools?.length === 0
  const problems = (workspace.data?.needsAttention ?? []).filter((a) => a.subject.kind === 'ActorTemplate' && a.subject.atespace === t.atespace && a.subject.name === t.name)
  const finding = problems[0]
  const crashGroup = workspace.data?.needsAttention.find((a) => a.rule === 'crashedActors')?.groups?.find((g) => g.subject?.kind === 'ActorTemplate' && g.subject.atespace === t.atespace && g.subject.name === t.name)
  const rebuild = t.golden.state === 'failed' && t.golden.cause === 'memoryLimit' && crashGroup?.cause === 'memoryLimit' && !crashGroup.kagent
  const crashStep = rebuild ? memoryRebuildStep(crashGroup!.count) : crashGroup?.nextStep
  const subject = { kind: 'ActorTemplate', namespace: t.atespace, group: '', name: t.name }
  const [lead, rest] = finding ? splitReading(finding.reading) : []
  const problem: WorkspaceProblem | undefined = finding ? {
    id: finding.id, category: finding.rule, source: 'reading', origin: { label: 'Substrate API and WorkerPool labels' },
    tone: attentionTone(finding), title: finding.rule === 'goldenSnapshotFailed' ? 'Startup snapshot failed' : 'No worker pool matches',
    subject, detail: lead, rawDetail: rest || (t.golden.error ? withoutReasonCode(t.golden.error) : undefined),
    nextStep: rebuild && finding.rule === 'goldenSnapshotFailed' ? crashStep : finding.nextStep, rawDetailLabel: 'Details',
  } : declaredNoPool || t.golden.state === 'failed' ? {
    id: 'template', category: 'startup', source: 'reading', subject, origin: { label: 'Substrate API and WorkerPool labels' },
    tone: declaredNoPool ? 'alert' : 'degraded',
    title: declaredNoPool ? 'No worker pool matches' : 'Startup snapshot failed',
    detail: declaredNoPool ? 'No pool’s labels match this template’s worker selector.' : 'New actors without a seed tag start from a cold boot. Substrate does not retry a failed build.',
    rawDetail: t.golden.error ? withoutReasonCode(t.golden.error) : undefined, rawDetailLabel: 'Details',
  } : undefined
  const actorCoverage = query.data?.coverage.actors
  const actorsPartial = coverageInPart(actorCoverage) || coverageInPart(query.data?.coverage.workers)
  const actorNote = t.attentionCount === undefined || (actorsPartial && t.attentionCount === 0) ? 'Unknown' : `${actorsPartial ? '≥' : ''}${t.attentionCount} need attention`
  const selector = Object.entries(t.workerSelector ?? {}).map(([k, v]) => `${k}=${v}`).join(', ')
  const config = <TemplateSpec spec={query.data?.spec} unavailable={query.data?.specUnavailable} template={t} />
  return (
    <DetailShell
      identity={<div className="flex flex-wrap items-center gap-2">
        {crumb}
        <h1 className="min-w-0 break-all font-mono text-lg font-semibold text-theme-text-primary">{t.name}</h1>
        <GoldenBadge template={t} named />
        <span className="text-sm text-theme-text-secondary"><Term term="atespace">Atespace</Term>: {t.atespace}</span>
      </div>}
      headerActions={<button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} aria-label="Refresh" className="rounded p-1.5 text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"><RefreshCw size={18} /></button>}
      tabs={[{ id: 'overview', label: 'Overview', icon: <LayoutDashboard size={16} /> }, { id: 'spec', label: 'Spec', icon: <FileCode size={16} /> }]}
      activeTab={tab}
      onTabChange={(id) => setParams((p) => {
        const next = new URLSearchParams(p)
        if (id === 'overview') next.delete('tab')
        else next.set('tab', id)
        return next
      }, { replace: true })}
    >
      <ScreenBody>
        <RefreshFailedNotice queries={[query]} />
        {tab === 'spec' ? <WorkspaceCard title="Template configuration">{config}</WorkspaceCard> : <>
          {problem && <WorkspaceProblemCallout problem={problem} rootKind="ActorTemplate" subjectIsSelf more={
            (finding?.command || problems.length > 1) && <FoldSection title={problems.length > 1 ? `Details · +${problems.length - 1} more` : 'Recovery steps'} summary="" attention={false}>
              {finding?.command && <CommandChip command={finding.command} />}
              {problems.slice(1).map((p) => <p key={p.id} className="text-xs text-theme-text-secondary">{p.title}. {p.reading}</p>)}
            </FoldSection>
          } />}
          <WorkspaceCard title="At a glance">
            <FactGrid>
              <FactRow label={<Term term="golden">Startup snapshot</Term>}>
                <DimensionValue tone={declaredNoPool ? 'alert' : golden.severity === 'success' ? 'healthy' : golden.severity === 'warning' ? 'degraded' : 'neutral'}
                  source={<>Substrate template{t.golden.time && <> · <When iso={t.golden.time} prefix="updated" /></>}{t.golden.state === 'ready' && ' · New actors start from the golden snapshot'}</>}>
                  {t.golden.state === 'failed' ? 'Failed · cold startup' : t.golden.state === 'ready' ? 'Ready' : golden.label}
                </DimensionValue>
              </FactRow>
              <FactRow label="Placement">
                <DimensionValue tone={declaredNoPool ? 'alert' : t.matchingPools === null ? 'unknown' : 'neutral'}
                  source="WorkerPool labels and template selector · placement also requires capacity and a compatible runtime">
                  <PoolsCell t={t} />
                </DimensionValue>
                {t.matchingPools && <PoolMatchLinks pools={t.matchingPools} workspace={workspace.data} />}
              </FactRow>
              <FactRow label="Actors">
                <DimensionValue tone={t.actors?.byState.crashed ? 'unhealthy' : t.attentionCount ? 'alert' : actorNote === 'Unknown' ? 'unknown' : 'neutral'}
                  source={t.actors && (!coverageInPart(actorCoverage) || t.actors.total > 0)
                    ? `Reported by Substrate · ${coverageInPart(actorCoverage) ? '≥' : ''}${t.actors.total} actors` : coverageReason(actorCoverage, 'actors')}
                  linkLabel="Actors needing attention" onOpen={() => onOpenSubject({ kind: 'Actors', filter: { template: templateFilterValue(t), attention: '1' } })}>
                  {actorNote}
                </DimensionValue>
                {!!t.actors?.byState.crashed && <button type="button" className="text-xs text-accent-text hover:underline"
                  onClick={() => onOpenSubject({ kind: 'Actors', filter: { template: templateFilterValue(t), state: 'crashed' } })}>
                  {coverageInPart(actorCoverage) ? '≥' : ''}{t.actors.byState.crashed} reported crashed →
                </button>}
                {crashGroup && crashStep && <FoldSection title="Crash recovery steps" summary={crashGroup.cause === 'memoryLimit' ? t.memoryBytes ? `Killed over the ${bytesText(t.memoryBytes)} memory limit per actor` : 'Killed over their memory limit' : 'Review the crash causes'} attention={false}>
                  {!rebuild && <p className="text-xs text-theme-text-secondary">{crashStep}</p>}
                  {!crashGroup.kagent && crashGroup.command && <CommandChip command={crashGroup.command} />}
                  {!crashGroup.kagent && crashGroup.command && crashGroup.thenCommand && <CommandChip command={crashGroup.thenCommand} />}
                </FoldSection>}
              </FactRow>
            </FactGrid>
          </WorkspaceCard>
          <WorkspaceCard title="About">
            <FactGrid>
              <FactRow label="Per-actor resources">{resourcesText(t)}</FactRow>
              <FactRow label="Worker selector">{selector ? <span className="font-mono">{selector}</span> : 'No label constraints'}</FactRow>
              <FactRow label="Snapshot fidelity">{fidelityLabel(t.preferredFidelity)}</FactRow>
            </FactGrid>
          </WorkspaceCard>

        </>}
      </ScreenBody>
    </DetailShell>
  )
}
