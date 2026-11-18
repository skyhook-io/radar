import { Bot } from 'lucide-react'
import { AlertBanner, Badge, FactGrid, FactRow, FoldSection, PaneLoader, SectionHeading, Tooltip } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateTemplate,
  useSubstrateWorkspace,
  useSubstrateTemplates,
  type SubstrateAttention,
  type SubstrateRef,
  type SubstrateStatus,
  type SubstrateTemplateRow,
  type SubstrateTemplateSpec,
  type SubstrateWorkspace,
} from '../../api/substrateWorkspace'
import { Notice, RefreshFailedNotice, ScreenBody, ScreenEmptyState, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { useConnection } from '../../context/ConnectionContext'
import { NotInContext, useContextPin } from './contextPin'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import type { OpenSubject } from './SubstrateAttentionList'
import { SubstrateConnectPanel, SubstrateTierBanner, showsConnectPanel } from './SubstrateConnect'
import { SubstrateStateBar } from './SubstrateStateBar'
import { OWNERS_ONLY_NOTE, attentionBannerVariant, templateYamlCommand, bytesText, coverageInPart, coverageReason, cpuText, definitiveFailure, withoutReasonCode, fidelityLabel, goldenReading, splitReading, templateFilterValue, tierReading } from './readings'
import { SUBSTRATE_PATH, SUBSTRATE_SCREENS, substrateActorsPath, substrateTemplatePath } from './routes'
import { CommandChip, Name, SUBSTRATE_WIDE, SubstrateCoverageNotice, SubstrateHeader, SubstrateSourceLine, Term, Unread, When, WholeNames } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'

/** `named` says what the chip is about, for a place where it stands alone (a page header), not under a Golden snapshot column. */
export function GoldenBadge({ template, named }: { template: Pick<SubstrateTemplateRow, 'golden' | 'matchingPools' | 'matchingPoolsComplete'>; named?: boolean }) {
  const g = goldenReading(template)
  const label = named && !g.label.startsWith('Can’t') ? `Golden snapshot ${g.label.toLowerCase()}` : g.label
  return <Tooltip content={g.meaning}><span><Badge severity={g.severity} size="sm" className="whitespace-normal text-left">{label}</Badge></span></Tooltip>
}

function poolsText(t: SubstrateTemplateRow): { text: string; unknown?: boolean; none?: boolean } {
  // null: Radar knows no pool list, or none of a partial one matches.
  if (t.matchingPools === null) return { text: 'unknown', unknown: true }
  if (t.matchingPools.length === 0) return { text: 'none match', none: true }
  return { text: t.matchingPools.map((p) => p.name).join(', ') }
}

function PoolsCell({ t }: { t: SubstrateTemplateRow }) {
  const p = poolsText(t)
  if (p.unknown) return <Unread title={t.matchingPoolsComplete ? 'Radar knows no pool list.' : 'None of the pools Radar could read matches; others may.'}>unknown</Unread>
  if (p.none) return <span className="font-medium">none match</span>
  return <span className="text-[12.5px]"><WholeNames text={(t.matchingPools ?? []).map((x) => x.name).join(', ')} /></span>
}

function resourcesText(t: Pick<SubstrateTemplateRow, 'cpuMillicores' | 'memoryBytes'>): string | undefined {
  const parts = [t.cpuMillicores ? `${cpuText(t.cpuMillicores)} CPU` : null, t.memoryBytes ? bytesText(t.memoryBytes) : null].filter(Boolean)
  return parts.length ? parts.join(', ') : undefined
}

export function SubstrateTemplatesScreen({ workspace, status }: { workspace?: SubstrateWorkspace; status?: SubstrateStatus }) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const tier = workspace?.apiTier
  const connected = tier?.state === 'connected'
  const query = useSubstrateTemplates(connected)
  const data = query.data
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const columns: TableColumn<SubstrateTemplateRow>[] = [
    {
      header: 'Template',
      width: wide ? '20%' : '26%',
      cell: (t) => (
        <>
          <Name name={t.name} />
          <Sub><WholeNames text={wide ? t.atespace : [t.atespace, resourcesText(t)].filter(Boolean).join(' · ')} /></Sub>
        </>
      ),
    },
    {
      header: <Term term="golden">Golden snapshot</Term>,
      width: wide ? '24%' : '30%',
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
      header: 'Actors',
      width: wide ? '16%' : '20%',
      cell: (t) =>
        t.actors ? (
          <>
            <span className="whitespace-nowrap">{t.actors.total.toLocaleString('en-US')}{t.actors.byState.crashed ? `, ${t.actors.byState.crashed} crashed` : ''}</span>
            <div className="pt-1"><SubstrateStateBar counts={t.actors} compact /></div>
          </>
        ) : (
          <Unread>unknown</Unread>
        ),
    },
    { header: 'Pools matching its selector', width: wide ? '22%' : '24%', cell: (t) => <PoolsCell t={t} /> },
    ...(wide
      ? [{
          header: 'Per actor',
          width: '18%',
          cell: (t: SubstrateTemplateRow) => (
            <>
              <span className="whitespace-nowrap">{resourcesText(t) ?? <Unread>not set</Unread>}</span>
              <Sub>{fidelityLabel(t.preferredFidelity)} snapshots</Sub>
            </>
          ),
        }]
      : []),
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Templates" />
      <ScreenBody>
        {tier && <SubstrateSourceLine tier={tier} status={status} />}
        {!connected && tier ? (
          <>
            <SubstrateTierBanner tier={tier} />
            {status && showsConnectPanel(tier) ? <SubstrateConnectPanel status={status} tier={tier} /> : <Notice>{tierReading(tier).detail}</Notice>}
          </>
        ) : query.isLoading ? (
          <PaneLoader label="Loading templates…" />
        ) : !data ? (
          <Notice>Radar couldn’t read the templates: {query.error instanceof Error ? query.error.message : 'unknown error'}.</Notice>
        ) : (
          <>
            <RefreshFailedNotice queries={[query]} />
            {(coverageInPart(data.coverage.templates) || coverageInPart(data.coverage.actors)) && (
              <SubstrateCoverageNotice coverage={data.coverage} kinds={['templates', 'actors']} />
            )}
            <SectionTable
              columns={columns}
              rows={data.templates}
              rowKey={(t) => `${t.atespace}/${t.name}`}
              rowResource={(t) => ({ kind: 'SubstrateTemplate', namespace: t.atespace, name: t.name })}
              onInspect={(res) => navigate(substrateTemplatePath({ atespace: res.namespace, name: res.name }, connection.context || undefined))}
              empty={coverageInPart(data.coverage.templates) ? `No templates: ${coverageReason(data.coverage.templates, 'templates')}.` : 'No templates.'}
              minWidth={wide ? 900 : 720}
            />
          </>
        )}
      </ScreenBody>
    </div>
  )
}

const SPEC_CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

/**
 * What the template runs, read-only: its containers and images, volumes and
 * snapshot settings. Env values Radar deems secret arrive redacted. A
 * template is immutable, so this is exactly what each of its actors runs.
 */
function TemplateSpec({ spec, unavailable, template }: { spec?: SubstrateTemplateSpec; unavailable?: string; template: SubstrateRef }) {
  return (
    <section className={SPEC_CARD}>
      <SectionHeading hint="read-only; a change is a new template">Spec</SectionHeading>
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
                      <dd className="break-all font-mono">{c.env.map((e) => `${e.name}=${e.redacted ? '(redacted)' : e.value}`).join(' ')}</dd>
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
    </section>
  )
}

/**
 * A failed golden snapshot: the cause Substrate's row reads from the error,
 * once, then the step. Substrate's own message and that it never retries
 * fold under Details.
 */
function GoldenFailedBanner({ error, row }: { error?: string; row?: SubstrateAttention }) {
  const message = withoutReasonCode(error ?? '')
  const [lead] = row ? splitReading(row.reading) : []
  // The row's reading leads with Substrate's message when it names no cause; then the message is the lead.
  const cause = lead && !lead.startsWith('Substrate') ? lead : undefined
  return (
    // The tone of its Needs attention row: new actors boot cold, nothing is lost.
    <AlertBanner variant={row ? attentionBannerVariant(row) : 'warning'} title="No golden snapshot: new actors from the template’s default boot cold" className="">
      <div className="mt-1 break-words text-xs text-theme-text-secondary">
        {cause ?? <>Substrate’s message: <span className="font-medium text-theme-text-primary">{message}</span>.</>}
      </div>
      <div className="mt-1 text-xs text-theme-text-primary">
        <span className="font-medium">Next step: </span>
        {row?.nextStep ?? 'Fix the cause in Substrate’s message, then recreate the template: templates can’t be edited, and each new one gets a golden snapshot build.'}
        {row?.command && <CommandChip command={row.command} />}
      </div>
      <div className="mt-1.5">
        <FoldSection title="Details" summary="" attention={false}>
          <div className="break-words text-xs text-theme-text-secondary">
            {cause && <>Substrate’s message: <span className="font-medium text-theme-text-primary">{message}</span>. </>}
            Substrate does not retry a failed build.
          </div>
        </FoldSection>
      </div>
    </AlertBanner>
  )
}

/** A template's page: its golden snapshot, the actors of it by state, and the pools that can run it. */
export function SubstrateTemplatePage({ template, onOpenSubject }: { template: SubstrateRef; onOpenSubject: OpenSubject }) {
  const navigate = useWorkspaceNavigate()
  const pin = useContextPin()
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
      <span className="font-mono">{template.atespace}</span>
      <span>/</span>
    </nav>
  )
  if (pin.mismatch) {
    return <NotInContext name={template.name} what={`template ${template.atespace} / ${template.name}`} pinnedContext={pin.pinned!} activeContext={pin.active} />
  }
  if (!t && query.isLoading) return <PaneLoader label={`Loading ${template.name}…`} className="flex-1" />
  if (!t) {
    const title = code === 'ownersOnly' ? 'Templates are for org owners' : code === 'notFound' ? `Substrate has no template ${template.name}` : code === 'denied' ? `Substrate denied reading ${template.name}` : `${template.name} couldn’t be read`
    const detail = code === 'ownersOnly'
      ? OWNERS_ONLY_NOTE
      : code === 'notFound'
        ? `There is no template ${template.name} in atespace ${template.atespace}. It may have been deleted since this link was made.`
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
  const pools = poolsText(t)
  // Substrate's own row for this failure names a cause-specific remedy (a memory kill: raise the limit).
  const goldenRow = workspace.data?.needsAttention.find((a) => a.rule === 'goldenSnapshotFailed' && a.subject.atespace === t.atespace && a.subject.name === t.name)
  // This template's share of the Overview's crashed actors, with the cause Substrate's messages name.
  const crashes = workspace.data?.needsAttention
    .find((a) => a.rule === 'crashedActors')
    ?.groups?.find((g) => g.subject?.kind === 'ActorTemplate' && g.subject.atespace === t.atespace && g.subject.name === t.name)
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-5 pt-4 xl:px-7">
        {crumb}
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="font-mono text-lg font-semibold text-theme-text-primary">{t.name}</h1>
          <Badge tone="structural" size="sm">ActorTemplate</Badge>
          <GoldenBadge template={t} named />
        </div>
      </div>
      <ScreenBody>
        <RefreshFailedNotice queries={[query]} />
        {t.golden.state === 'failed' && <GoldenFailedBanner error={t.golden.error} row={goldenRow} />}
        {crashes && crashes.count > 0 && (
          <AlertBanner variant="error" title={`${crashes.count} of its actors ${crashes.count === 1 ? 'is' : 'are'} crashed`} className="">
            {/* One line: the cause Substrate's messages name, and the way to the actors. */}
            <div className="mt-1 flex flex-wrap items-baseline gap-x-3 text-xs text-theme-text-secondary">
              {crashes.cause === 'memoryLimit' && <span>{t.memoryBytes ? `Killed over the ${bytesText(t.memoryBytes)} memory limit it sets per actor.` : 'Killed over their memory limit.'}</span>}
              <button type="button" onClick={() => onOpenSubject({ kind: 'Actors', filter: { state: 'crashed', template: templateFilterValue(t) } })} className="text-accent-text hover:underline">
                {crashes.count === 1 ? 'The crashed actor →' : `The ${crashes.count} crashed actors →`}
              </button>
            </div>
            {crashes.nextStep && (
              <div className="mt-1 text-xs text-theme-text-primary">
                <span className="font-medium">Next step: </span>
                {crashes.nextStep}
                {crashes.command && <> <CommandChip command={crashes.command} inline /></>}
              </div>
            )}
          </AlertBanner>
        )}
        {/* At risk, nothing lost yet: the alert of its Needs attention row and its golden-snapshot chip. */}
        {pools.none && (
          <AlertBanner variant="alert" title="No pool can run it" message={`No pool’s labels match its worker selector, so no worker can run its actors${t.golden.state === 'building' ? ', and its golden snapshot can’t be built' : ''}.`} className="">
            <div className="mt-1 text-xs text-theme-text-primary"><span className="font-medium">Next step: </span>Label a pool to match the template’s worker selector, or recreate the template with a selector a pool matches.</div>
          </AlertBanner>
        )}
        <section className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
          <SectionHeading>At a glance</SectionHeading>
          <FactGrid>
            <FactRow label={<Term term="golden">Golden snapshot</Term>}>
              <span className="inline-flex flex-wrap items-center gap-x-2">
                <GoldenBadge template={t} />
                {t.golden.time && <span className="text-theme-text-secondary"><When iso={t.golden.time} prefix={t.golden.state === 'ready' ? 'taken' : 'changed'} /></span>}
              </span>
              {/* A banner above already says what a failed or blocked build means. */}
              {t.golden.state !== 'failed' && !pools.none && <div className="text-xs text-theme-text-secondary">{goldenReading(t).meaning}</div>}
              {t.golden.tag && <div className="text-xs text-theme-text-tertiary">tag <span className="font-mono">{t.golden.tag.atespace} / {t.golden.tag.name}</span></div>}
            </FactRow>
            <FactRow label="Pools matching its selector">
              {pools.unknown ? (
                <Unread>unknown</Unread>
              ) : pools.none ? (
                'none match'
              ) : (
                (t.matchingPools ?? []).map((pl, i) => (
                  <span key={`${pl.namespace}/${pl.name}`}>
                    {i > 0 && ', '}
                    <button type="button" onClick={() => onOpenSubject({ kind: 'WorkerPool', namespace: pl.namespace, name: pl.name })} className="whitespace-nowrap font-mono text-accent-text hover:underline">{pl.name}</button>
                  </span>
                ))
              )}
            </FactRow>
            <FactRow label="Worker selector">{t.workerSelector && Object.keys(t.workerSelector).length ? <span className="font-mono">{Object.entries(t.workerSelector).map(([k, v]) => `${k}=${v}`).join(', ')}</span> : 'none: every pool is eligible'}</FactRow>
            <FactRow label="Per actor">{resourcesText(t) ?? <Unread>not set</Unread>}</FactRow>
            {/* The Spec section below shows these in full when it was read. */}
            {!query.data?.spec && (
              <>
            <FactRow label="Snapshots">{fidelityLabel(t.preferredFidelity)}<div className="text-xs text-theme-text-tertiary">The most a snapshot of its actors holds: a ceiling, not a guarantee. An actor’s own snapshot can hold less; its Snapshots tab says what each holds.</div></FactRow>
            <FactRow label="Stored in">{t.storageLocation ? <span className="font-mono">{t.storageLocation}</span> : 'the API server’s default location'}</FactRow>
            <FactRow label="Containers">{t.containers?.length ? <span className="font-mono">{t.containers.join(', ')}</span> : <Unread>not listed</Unread>}</FactRow>
              </>
            )}
          </FactGrid>
        </section>
        <section className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
          <SectionHeading hint={<button type="button" onClick={() => navigate(substrateActorsPath({ template: templateFilterValue(t) }))} className="text-accent-text hover:underline">All its actors →</button>}>
            <Term term="actor">Actors</Term>
          </SectionHeading>
          {t.actors ? (
            <SubstrateStateBar counts={t.actors} onSelectState={(state) => onOpenSubject({ kind: 'Actors', filter: { template: templateFilterValue(t), state } })} />
          ) : (
            <Unread>unknown: actors were not read</Unread>
          )}
        </section>
        <TemplateSpec spec={query.data?.spec} unavailable={query.data?.specUnavailable} template={t} />
      </ScreenBody>
    </div>
  )
}
