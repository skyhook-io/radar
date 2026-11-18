import { useLayoutEffect, useRef, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { AlertBanner, FactGrid, FactRow, SectionHeading, Tooltip, formatAge, toneTextClass } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateActorLogs,
  type SubstrateActorDetail,
  type SubstrateAttention,
  type SubstrateEgress,
  type SubstrateActorLogs,
  type SubstrateLogLine,
  type SubstrateRef,
  type SubstrateWorkerRow,
} from '../../api/substrateWorkspace'
import { type OpenSubject, SubjectLink } from './SubstrateAttentionList'
import { ACTOR_STATE_LABEL, DISCARD_WHY, definitiveFailure, logsCommand, memoryKillText, attentionTone, bytesText, cpuText, durableText, fidelityLabel, refText, resumeConditionSentence, resumeSentence, revertCommand, stateExtra, localTime, relativeTime, servingEvidence, withoutRepeats } from './readings'
import { ActorStateBadge, CommandChip, Term, Unread, When } from './shared'
import { RefreshFailedNotice } from '../workspace/layout'

const CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

const WORKER_STATE: Record<SubstrateWorkerRow['state'], { label: string; tone: 'healthy' | 'degraded' | 'unhealthy' | 'unknown' }> = {
  active: { label: 'active', tone: 'healthy' },
  draining: { label: 'draining', tone: 'degraded' },
  unavailable: { label: 'unavailable', tone: 'unhealthy' },
  unknown: { label: 'state unknown', tone: 'unknown' },
}

/** The crash banner: Substrate's message, what a revert leaves and drops, and one next step with the exact command. */
function CrashBanner({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject?: OpenSubject }) {
  const { actor, template, recovery } = detail
  const at = actor.crash?.time ?? actor.updateTime
  const message = actor.crash?.message
  const crashedOfTemplate = template?.actors?.byState.crashed
  const others = crashedOfTemplate !== undefined ? crashedOfTemplate - 1 : undefined
  // A pending upload that can still finish is worth waiting for; an abandoned one never is.
  const pending = recovery?.discarded.filter((d) => d.why === 'uploadPending') ?? []
  // The same read as the Overview's last lines; a memory-limit kill among them is the crash's cause.
  const logs = useSubstrateActorLogs(actor, { tailLines: 8 })
  const kill = logs.data?.memoryKill
  // The worker's log names the cause and its remedy; without it, Substrate's own crash row says what to do.
  const crashedRow = detail.attention.find((a) => a.rule === 'crashedActors')
  const step = kill?.current && kill.nextStep
    ? { text: kill.nextStep, command: kill.command }
    : crashedRow
      ? { text: crashedRow.nextStep, command: crashedRow.command }
      : recovery
        ? { text: 'Fix the cause in Substrate’s message, then revert the actor.', command: revertCommand(actor) }
        : undefined
  return (
    <AlertBanner variant="error" title={`Crashed${at ? ` ${relativeTime(at)}` : ''}`} className="">
      <div className="mt-1 break-words text-xs text-theme-text-secondary">
        {message ? (
          <>
            Substrate’s message: <span className="font-medium text-theme-text-primary">{message}</span>
          </>
        ) : (
          'Substrate recorded no crash message.'
        )}
      </div>
      {/* Holds the cause's place while the log is read, so the banner doesn't grow under the reader. */}
      {logs.isLoading && <div className="mt-1 text-xs text-theme-text-tertiary">Reading its worker’s log for the cause…</div>}
      {kill?.current && (
        <div className="mt-1 break-words text-xs text-theme-text-secondary">
          Cause, from its worker’s log: <span className="font-medium text-theme-text-primary">{memoryKillText(kill)}</span>
          {kill.time && <> <When iso={kill.time} /></>}, in <span className="font-mono">{kill.pod}</span>.
        </div>
      )}
      {kill && !kill.current && (
        <div className="mt-1 break-words text-xs text-theme-text-secondary">
          A memory-limit kill was recorded{kill.time && <> <When iso={kill.time} /></>} in <span className="font-mono">{kill.pod}</span>, from an earlier run; Radar can’t tie it to this crash.
        </div>
      )}
      <div className="mt-1 break-words text-xs text-theme-text-secondary">
        Substrate stopped the actor and released its worker.
        {recovery && <> <RecoveryText recovery={recovery} newest={detail.row.newestSnapshot?.generation} /></>}
        {others !== undefined && others > 0 && (
          <> {others === 1 ? '1 other actor' : `${others} other actors`} of the same template {others === 1 ? 'is' : 'are'} crashed too, so the cause may be the template rather than this actor.</>
        )}
      </div>
      {recovery && recovery.discarded.length > 0 && (
        <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs text-theme-text-secondary">
          {recovery.discarded.map((d) => (
            <li key={d.generation}>
              A revert drops generation {d.generation}{d.fidelity ? ` (${fidelityLabel(d.fidelity)})` : ''}: {DISCARD_WHY[d.why] ?? d.why}.
            </li>
          ))}
        </ul>
      )}
      {step && (
        <div className="mt-1.5 break-words text-xs text-theme-text-primary">
          <span className="font-medium">Next step: </span>
          {step.text}
          {pending.length > 0 && <> To keep generation {pending[0].generation}, wait for its upload to finish before reverting.</>}
          {step.command && <CommandChip command={step.command} />}
        </div>
      )}
      {others !== undefined && others > 0 && onOpenSubject && (
        <div className="mt-1.5 text-xs">
          <button
            type="button"
            onClick={() => onOpenSubject({ kind: 'Actors', filter: { state: 'crashed', template: `${actor.template.atespace}/${actor.template.name}` } })}
            className="text-accent-text hover:underline"
          >
            The {crashedOfTemplate} crashed actors of this template →
          </button>
        </div>
      )}
    </AlertBanner>
  )
}

const WORKER_RULES = new Set<SubstrateAttention['rule']>(['workerUnavailable', 'workerDraining', 'workerRestarting'])

const IMPACT_TITLE: Record<NonNullable<SubstrateActorDetail['workerImpact']>['kind'], string> = {
  unavailable: 'Its worker is unavailable',
  draining: 'Its worker is draining: Substrate crashes it when the Pod is gone',
  restarted: 'Its worker restarted under it: its sandbox is gone',
}

/** What the worker's trouble means for this actor, first, before the worker's own row. */
function WorkerImpactBanner({ actor, impact }: { actor: SubstrateActorDetail['actor']; impact: NonNullable<SubstrateActorDetail['workerImpact']> }) {
  // The same read as the Overview's last lines: the actor's own output is the evidence it still runs.
  const logs = useSubstrateActorLogs(actor, { tailLines: 8 })
  const evidence = impact.kind === 'restarted' ? undefined : servingEvidence(logs.data?.lines)
  return (
    <AlertBanner variant="warning" title={IMPACT_TITLE[impact.kind]} className="">
      <div className="mt-1 break-words text-xs text-theme-text-secondary">
        {impact.reading}
        {evidence?.recent && evidence.at && <> Its last log line was <When iso={evidence.at} />, so it is still running for now.</>}
        {evidence && !evidence.recent && (evidence.at
          ? <> Its last log line was <When iso={evidence.at} />, so it may not be serving.</>
          : <> Radar found no output from it in the worker’s log, so it may not be serving.</>)}
      </div>
      {impact.deadline && <div className="mt-1 text-xs text-theme-text-secondary">Its Pod is due to be gone <When iso={impact.deadline} />.</div>}
      <div className="mt-1 break-words text-xs text-theme-text-primary">
        <span className="font-medium">Next step: </span>
        {impact.nextStep}
        {impact.command && <CommandChip command={impact.command} />}
      </div>
    </AlertBanner>
  )
}

/** What a revert leaves: the newest stored generation, or nothing. */
function RecoveryText({ recovery, newest }: { recovery: NonNullable<SubstrateActorDetail['recovery']>; newest?: number }) {
  if (recovery.generation === undefined) {
    return <>A revert returns it to suspended with nothing stored, so it boots fresh.</>
  }
  // At a glance already says how its newest snapshot resumes; only an older target needs saying here.
  return (
    <>
      A revert returns it to suspended at generation {recovery.generation}, its newest stored snapshot.
      {recovery.generation !== newest && <> {resumeConditionSentence(recovery.resumesWith, recovery.resumeCondition)}</>}
    </>
  )
}

/**
 * A Needs attention row on one actor's page, said about this actor: the row
 * itself is about every actor it covers, and its reading names the longest.
 */
export function actorAttentionText(row: SubstrateAttention, detail: SubstrateActorDetail): { title: string; message?: string } {
  const { actor } = detail
  const held = actor.updateTime ? formatAge(actor.updateTime) : undefined
  const others = row.count - 1
  const alsoText = others > 0 ? `${others === 1 ? '1 other actor is' : `${others} other actors are`} in the same state.` : undefined
  switch (row.rule) {
    case 'stuckTransition':
      return {
        title: `${ACTOR_STATE_LABEL[actor.state]}${held ? ` for at least ${held}` : ''}, longer than a transition should take`,
        message: [detail.row.waitingOn?.reading, alsoText].filter(Boolean).join(' ') || undefined,
      }
    case 'uploadOverdue': {
      const snap = detail.row.newestSnapshot
      return {
        title: `Snapshot${snap ? ` generation ${snap.generation}` : ''} not stored yet${held ? `, for at least ${held}` : ''}`,
        message: [`Until the upload finishes, its state exists only on ${snap?.locality ? `node ${snap.locality}` : 'its node'}; losing that node could lose it.`, alsoText].filter(Boolean).join(' '),
      }
    }
  }
  return { title: row.title, message: row.reading }
}

function AttentionBanner({ row, detail }: { row: SubstrateAttention; detail: SubstrateActorDetail }) {
  const text = actorAttentionText(row, detail)
  return (
    <AlertBanner variant={attentionTone(row) === 'unhealthy' ? 'error' : 'warning'} title={text.title} message={withoutRepeats(text.message, row.nextStep)} className="">
      <div className="mt-1 break-words text-xs text-theme-text-primary">
        <span className="font-medium">Next step: </span>
        {row.nextStep}
        {row.command && <CommandChip command={row.command} />}
      </div>
    </AlertBanner>
  )
}

export function egressText(e: SubstrateEgress): { text: ReactNode; tone: 'neutral' | 'unknown' } {
  switch (e.state) {
    case 'set': {
      const dest = e.destinations?.length ? ` · ${e.destinations.join(', ')}${e.more ? ` and ${e.more} more` : ''}` : ''
      return { text: `${e.rules} ${e.rules === 1 ? 'rule' : 'rules'}${dest}`, tone: 'neutral' }
    }
    case 'none':
      return { text: 'No policy: outbound traffic is denied', tone: 'neutral' }
    case 'denied':
      return { text: `Substrate denied reading it${e.message ? `: ${e.message}` : ''}`, tone: 'unknown' }
    default:
      return { text: `Couldn’t be read${e.message ? `: ${e.message}` : ''}`, tone: 'unknown' }
  }
}

function AtAGlance({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject?: OpenSubject }) {
  const { actor, row, template, worker } = detail
  const as = actor.assignment
  const snap = row.newestSnapshot
  const egress = egressText(detail.egress)
  return (
    <FactGrid>
      <FactRow label="State">
        <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <ActorStateBadge state={actor.state} impact={detail.workerImpact} />
          {actor.updateTime && (
            <span className="text-theme-text-secondary"><When iso={actor.updateTime} prefix="since at least" hint="Substrate records no transition time; its last change to the actor is a lower bound:" /></span>
          )}
        </span>
        {/* A held duration is already in "since at least"; only other facts (a pending upload) add a line. */}
        {actor.state !== 'crashed' && stateExtra(actor.state, row.reason) && !stateExtra(actor.state, row.reason)!.startsWith('for ') && (
          <div className="text-xs text-theme-text-secondary">{stateExtra(actor.state, row.reason)}</div>
        )}
      </FactRow>
      <FactRow label={<Term term="template">Template</Term>}>
        <SubjectLink subject={{ kind: 'ActorTemplate', atespace: actor.template.atespace, name: actor.template.name }} onOpen={onOpenSubject} withKind={false} />
        {template && (template.cpuMillicores || template.memoryBytes) ? (
          <div className="text-xs text-theme-text-secondary">
            {[template.cpuMillicores ? `${cpuText(template.cpuMillicores)} CPU` : null, template.memoryBytes ? bytesText(template.memoryBytes) : null].filter(Boolean).join(', ')} per actor
          </div>
        ) : null}
      </FactRow>
      <FactRow label={<Term term="worker">Worker</Term>}>
        {as ? (
          <>
            <SubjectLink subject={{ kind: 'Pod', namespace: as.namespace, name: as.pod }} label={as.pod} onOpen={onOpenSubject} withKind={false} />
            {worker ? (
              <span className={clsx('ml-1.5', toneTextClass(WORKER_STATE[worker.state].tone))}>{WORKER_STATE[worker.state].label}</span>
            ) : (
              <Unread title="Substrate’s workers were not read."> · state unknown</Unread>
            )}
          </>
        ) : (
          <span className="text-theme-text-secondary">None: a {ACTOR_STATE_LABEL[actor.state].toLowerCase()} actor runs on no worker</span>
        )}
      </FactRow>
      <FactRow label={<Term term="pool">Pool</Term>}>
        {as ? (
          <>
            <SubjectLink subject={{ kind: 'WorkerPool', namespace: as.namespace, name: as.pool }} label={as.pool} onOpen={onOpenSubject} withKind={false} />
            <span className="text-theme-text-secondary"> · namespace <span className="whitespace-nowrap">{as.namespace}</span></span>
            {(as.sandboxClass || snap?.sandboxClass) && <div className="text-xs text-theme-text-secondary">sandbox class <span className="font-mono">{as.sandboxClass ?? snap?.sandboxClass}</span></div>}
          </>
        ) : snap?.sandboxClass ? (
          <span className="text-theme-text-secondary">none now · last ran under sandbox class <span className="font-mono">{snap.sandboxClass}</span></span>
        ) : (
          <span className="text-theme-text-secondary">none now</span>
        )}
      </FactRow>
      <FactRow label="Node">
        {row.node ? (
          <>
            <SubjectLink subject={{ kind: 'Node', name: row.node }} onOpen={onOpenSubject} withKind={false} />
            {!as && <div className="text-xs text-theme-text-secondary">where it was last, or where its snapshot is</div>}
          </>
        ) : (
          <Unread>Substrate names none</Unread>
        )}
      </FactRow>
      <FactRow label={<Term term="snapshot">Newest snapshot</Term>}>
        {snap ? (
          <>
            <span>generation {snap.generation} · {fidelityLabel(snap.fidelity)} · </span>
            <Tooltip content={durableText(snap).title}>
              <span className={durableText(snap).tone === 'healthy' ? undefined : toneTextClass(durableText(snap).tone)}>{durableText(snap).text}</span>
            </Tooltip>
            <div className="text-xs text-theme-text-secondary">{resumeSentence(row)}</div>
          </>
        ) : (
          <span className="text-theme-text-secondary">None yet, so it boots fresh when it resumes</span>
        )}
      </FactRow>
      <FactRow label="Seeded from">
        {actor.sourceTag ? <span className="font-mono">{refText(actor.sourceTag)}</span> : <span className="text-theme-text-secondary">its template, not a tag</span>}
      </FactRow>
      <FactRow label="Egress">
        <span className={egress.tone === 'unknown' ? 'text-theme-text-tertiary' : undefined}>{egress.text}</span>
      </FactRow>
      <FactRow label="Created">
        {actor.createTime ? <When iso={actor.createTime} /> : <Unread>unknown</Unread>}
      </FactRow>
    </FactGrid>
  )
}

export function LogLines({ lines, showPod }: { lines: (SubstrateLogLine & { pod?: string })[]; showPod?: boolean }) {
  // Opens at the newest line and follows new ones, unless the reader has scrolled up to read.
  const ref = useRef<HTMLPreElement>(null)
  const following = useRef(true)
  useLayoutEffect(() => {
    const el = ref.current
    if (el && following.current) el.scrollTop = el.scrollHeight
  }, [lines])
  const onScroll = () => {
    const el = ref.current
    if (el) following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }
  return (
    <pre ref={ref} onScroll={onScroll} className="max-h-[28rem] overflow-auto rounded-lg border border-theme-border bg-theme-base px-3 py-2 font-mono text-[12px] leading-5 text-theme-text-primary">
      {lines.map((l, i) => (
        <div key={i} className="whitespace-pre-wrap break-words">
          {l.time && <span className="text-theme-text-tertiary">{new Date(l.time).toLocaleTimeString()} </span>}
          {showPod && l.pod && <span className="text-theme-text-tertiary">{l.pod} </span>}
          <span className="text-theme-text-tertiary">{l.kind === 'container' ? l.container ?? 'container' : l.kind} </span>
          {l.message ?? l.text}
        </div>
      ))}
    </pre>
  )
}

/** The query an external log backend takes for an actor: Substrate's own log attribute keys (ateattr). */
export function actorLogQuery(actor: SubstrateRef): string {
  return `ate.actor.name="${actor.name}" ate.atespace="${actor.atespace}"`
}

/** What a log backend search needs for an actor, and the kubectl ate command: Substrate's own log attribute keys. */
export function LogHandoff({ actor }: { actor: SubstrateActorDetail['actor'] }) {
  const crashed = actor.state === 'crashed' && actor.crash?.time
  return (
    <div className="space-y-1.5 text-sm text-theme-text-secondary">
      <p className="text-xs">To find its lines in your log backend, search for its attributes{crashed ? <> around <span className="font-mono">{localTime(actor.crash!.time!)}</span> (local time)</> : ''}:</p>
      <CommandChip command={actorLogQuery(actor)} />
      <p className="text-xs">kubectl ate reads only the current worker; it shows lines once the actor runs again:</p>
      <CommandChip command={logsCommand(actor)} />
    </div>
  )
}


/** What a log read could not give, in product terms, from the server's reason code. */
function logGap(logs: SubstrateActorLogs, actor: SubstrateActorDetail['actor']): string {
  const node = logs.node ? `node ${logs.node}` : 'its node'
  const searched = logs.searched.length
  switch (logs.reasonCode) {
    case 'workerPodGone':
      return `The worker Pod ${logs.worker?.pod ?? ''} is gone; Substrate still lists the worker, so its lines went with the Pod.`
    case 'nodeGone':
      return `The node it last ran on, ${logs.node ?? ''}, is gone from the cluster, and its worker Pods’ logs with it.`
    case 'noWorkerPods':
      return `No worker Pod runs on ${node} now: the one it ran in is gone, and its lines with it.`
    case 'denied':
      return `You may not read the logs of the worker Pods on ${node}.`
    case 'noMatch':
      return `None of the ${searched} worker ${searched === 1 ? 'Pod' : 'Pods'} on ${node} holds its lines${logs.windowFull ? ' within the part Radar scanned' : ''}.`
    case 'noWorker':
    default:
      return `A ${ACTOR_STATE_LABEL[actor.state].toLowerCase()} actor runs on no worker, and Substrate names no node it ran on.`
  }
}

/**
 * An actor's log lines, by where Radar found them: its current worker Pod;
 * or, with no worker, the worker Pods on the node it last ran on, each line
 * naming its Pod, as candidates; or what is missing and how to look further.
 */
type LogsRead = { data?: SubstrateActorLogs; isLoading: boolean; error: unknown; isRefetchError?: boolean; dataUpdatedAt?: number }

export function ActorLogsBody({ actor, logs, compact }: { actor: SubstrateActorDetail['actor']; logs: LogsRead; compact?: boolean }) {
  // A refresh that says the log can't be read any more replaces the lines kept from before.
  const data = definitiveFailure(substrateErrorCode(logs.error)) ? undefined : logs.data
  const stale = data && logs.isRefetchError ? <RefreshFailedNotice queries={[{ isRefetchError: true, error: logs.error instanceof Error ? logs.error : null, dataUpdatedAt: logs.dataUpdatedAt ?? 0 }]} /> : null
  if (logs.isLoading) return <p className="text-sm text-theme-text-tertiary">Reading the worker Pod’s log…</p>
  if (logs.error && !data) return <p className="text-sm text-theme-text-tertiary">Radar couldn’t read the log: {logs.error instanceof Error ? logs.error.message : 'unknown error'}.</p>
  if (!data) return null
  const crashedAt = actor.state === 'crashed' ? actor.crash?.time : undefined
  if (data.lines.length > 0) {
    const pods = [...new Set(data.lines.map((l) => l.pod))]
    const kill = data.memoryKill
    return (
      <div className="space-y-1.5">
        {stale}
        {kill && !compact && (
          <p className="text-sm text-theme-text-secondary">
            {kill.current
              ? <>In {kill.pod}{kill.time && <> <When iso={kill.time} /></>}, {memoryKillText(kill)}{actor.state === 'crashed' ? ': that is why it crashed.' : '.'}</>
              : <>A memory-limit kill was recorded{kill.time && <> <When iso={kill.time} /></>} in {kill.pod}, from an earlier run: {memoryKillText(kill)}.{actor.state === 'crashed' ? ' Radar can’t tie it to this crash.' : ''}</>}
          </p>
        )}
        {data.origin === 'lastNode' && (
          <p className="text-xs text-theme-text-secondary">
            Found on {pods.length === 1 ? <span className="font-mono">{pods[0]}</span> : `${pods.length} worker Pods`}, on <span className="font-mono">{data.node}</span>, the node it last ran on{crashedAt ? <>, before it crashed <When iso={crashedAt} /></> : ''}. Substrate doesn’t record which Pod it ran in, so these are candidates{pods.length > 1 ? ': each line names its Pod' : ''}.
          </p>
        )}
        <LogLines lines={data.lines} showPod={data.origin === 'lastNode' && pods.length > 1} />
        {data.windowFull && <p className="text-xs text-theme-text-tertiary">Radar read the newest lines of each Pod’s log; older lines of this actor may exist beyond them.</p>}
      </div>
    )
  }
  if (data.origin === 'worker' && !data.reasonCode) {
    return <div className="space-y-1.5">{stale}<p className="text-sm text-theme-text-tertiary">No lines for this actor in its current worker Pod’s log{data.windowFull ? ' within the part Radar scanned' : ''}.</p></div>
  }
  return (
    <div className="space-y-1.5">
      {stale}
      <p className="text-sm text-theme-text-secondary">
        {logGap(data, actor)}
        {crashedAt && <> It crashed <When iso={crashedAt} />.</>}
      </p>
      {!compact && data.searched.length > 0 && (
        <ul className="text-xs text-theme-text-tertiary">
          {data.searched.map((p) => (
            <li key={`${p.namespace}/${p.pod}`}>
              <span className="font-mono">{p.pod}</span>: {p.outcome === 'read' ? `read, ${p.matched} of its lines` : p.outcome === 'gone' ? 'gone' : p.outcome === 'denied' ? 'no access to its log' : `couldn’t be read${p.message ? `: ${p.message}` : ''}`}
            </li>
          ))}
          {data.morePods ? <li>and {data.morePods} more worker {data.morePods === 1 ? 'Pod' : 'Pods'} not searched</li> : null}
        </ul>
      )}
      {!compact && <LogHandoff actor={actor} />}
    </div>
  )
}

function LastLines({ actor, onShowAll }: { actor: SubstrateActorDetail['actor']; onShowAll?: () => void }) {
  const logs = useSubstrateActorLogs(actor, { tailLines: 8 })
  const fromWorker = logs.data?.origin === 'worker'
  return (
    <section>
      <SectionHeading hint={onShowAll && <button type="button" onClick={onShowAll} className="text-accent-text hover:underline">All logs →</button>}>
        {fromWorker ? 'Last lines from its current worker' : 'Last lines'}
      </SectionHeading>
      <ActorLogsBody actor={actor} logs={logs} compact />
    </section>
  )
}

/**
 * An actor's Overview, on its page and in the drawer: what is wrong first
 * (Substrate's crash message, or Radar's reading), then the facts, its last
 * log lines and what it relates to.
 */
export function SubstrateActorOverview({
  detail,
  onOpenSubject,
  onShowLogs,
}: {
  detail: SubstrateActorDetail
  onOpenSubject?: OpenSubject
  /** Opens the Logs tab, or the page at it from the drawer. */
  onShowLogs?: () => void
}) {
  const others = detail.attention.filter((a) => a.rule !== 'crashedActors')
  return (
    <div className="space-y-4">
      {detail.actor.state === 'crashed' && <CrashBanner detail={detail} onOpenSubject={onOpenSubject} />}
      {detail.workerImpact && <WorkerImpactBanner actor={detail.actor} impact={detail.workerImpact} />}
      {others.filter((a) => !(detail.workerImpact && WORKER_RULES.has(a.rule))).map((a) => <AttentionBanner key={a.id} row={a} detail={detail} />)}
      <section className={CARD}>
        <SectionHeading>At a glance</SectionHeading>
        <AtAGlance detail={detail} onOpenSubject={onOpenSubject} />
      </section>
      <section className={CARD}>
        <LastLines actor={detail.actor} onShowAll={onShowLogs} />
      </section>
      {detail.related.length > 0 && (
        <section className={CARD}>
          <SectionHeading>Related</SectionHeading>
          <ul className="space-y-1 text-sm">
            {detail.related.map((s) => (
              <li key={`${s.kind}/${s.namespace ?? s.atespace ?? ''}/${s.name}`} className="text-theme-text-tertiary">
                <SubjectLink subject={s} onOpen={onOpenSubject} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}
