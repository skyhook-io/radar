import { useId, useLayoutEffect, useRef, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { DimensionValue, FactGrid, FactRow, FoldSection, WorkspaceCard, WorkspaceProblemCallout, formatAge, toneFillClass, toneTextClass, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateActorLogs,
  type SubstrateActorDetail,
  type SubstrateAttention,
  type SubstrateEgress,
  type SubstrateActorLogs,
  type SubstrateLogLine,
  type SubstrateRef,
} from '../../api/substrateWorkspace'
import { type OpenSubject, SubjectLink } from './SubstrateAttentionList'
import { ACTOR_STATE_LABEL, kagentRecoverStep, DISCARD_WHY, definitiveFailure, killLineLabel, killTie, killTiming, memoryKillText, attentionTone, bytesText, coverageReason, cpuText, fidelityLabel, refText, resumeConditionSentence, resumeSentence, revertCommand, localTime, relativeTime, servingEvidence, splitReading, withoutRepeats, workerImpactTone } from './readings'
import { CommandChip, Unread, When } from './shared'
import { RefreshFailedNotice } from '../workspace/layout'

const WORKER_RULES = new Set<SubstrateAttention['rule']>(['workerUnavailable', 'workerDraining', 'workerRestarting'])

function crashPlan(detail: SubstrateActorDetail, kill?: SubstrateActorLogs['memoryKill']) {
  const { actor, recovery } = detail
  if (actor.kagent) return { text: kagentRecoverStep(actor.kagent) }
  const golden = detail.attention.find((a) => a.rule === 'goldenSnapshotFailed')
  if (golden) return { text: `This actor built ${refText(actor.template)}’s golden snapshot. ${golden.nextStep}` }
  const crash = detail.attention.find((a) => a.rule === 'crashedActors')
  const remedy = kill?.current && kill.nextStep ? kill : crash
  if (remedy) return { text: remedy.nextStep, command: remedy.command, thenCommand: 'thenCommand' in remedy ? remedy.thenCommand : undefined }
  return recovery ? { text: 'Fix the crash cause, then revert the actor.', command: revertCommand(actor) } : undefined
}

function actorProblem(detail: SubstrateActorDetail, logs?: SubstrateActorLogs): { problem: WorkspaceProblem; additional: SubstrateAttention[] } | undefined {
  const { actor, workerImpact } = detail
  const subject = { kind: 'Actor', group: '', namespace: actor.atespace, name: actor.name }
  const origin = { label: 'Substrate API' }
  const base = { source: 'reading' as const, subject, origin }
  const additional = detail.attention.filter((a) => !WORKER_RULES.has(a.rule) && a.rule !== 'crashedActors' && !(actor.state === 'crashed' && a.rule === 'goldenSnapshotFailed'))
  if (actor.state === 'crashed') {
    const kill = logs?.memoryKill
    const tie = kill && killTiming(kill, actor.crash?.time)
    const at = actor.crash?.time ?? actor.updateTime
    const message = actor.crash?.message
    return { additional, problem: {
      ...base, id: 'crash', category: 'crash', tone: 'unhealthy',
      title: at ? `Actor crashed ${relativeTime(at)}` : 'Actor crashed',
      detail: kill?.current ? `Cause, from its worker’s log: ${memoryKillText(kill)}${tie ? `, ${tie}` : ''}.` : message ? `Substrate’s message: ${splitReading(message)[0]}` : 'Substrate recorded no crash message.',
      rawDetail: kill?.current ? [`Read from the worker’s log in ${kill.namespace}/${kill.pod}. Radar ties it to this crash by timing: the record names this actor${tie ? `, came ${tie},` : ''} and the lines read show no restart between them.`, message].filter(Boolean).join(' ')
        : [message && splitReading(message)[1], kill && `A memory-limit kill attempt was recorded in ${kill.namespace}/${kill.pod}${tie ? ` ${tie}` : ''}. Substrate reported ${message ? splitReading(message)[0] : 'no crash cause'}.`].filter(Boolean).join(' ') || undefined,
      rawDetailLabel: 'Details',
      origin: kill?.current ? { label: 'Worker Pod logs' } : origin,
      nextStep: crashPlan(detail, kill)?.text ?? 'Investigate the crash cause before trying to recover the actor.',
    } }
  }
  if (workerImpact) {
    const title = { unavailable: 'Worker unavailable', draining: 'Worker draining', restarted: 'Worker restarted and sandbox was lost' }[workerImpact.kind]
    const [lead, rest] = splitReading(workerImpact.reading)
    const evidence = workerImpact.kind === 'restarted' ? undefined : servingEvidence(logs?.lines)
    return { additional, problem: {
      ...base, id: 'worker', category: 'worker', tone: workerImpactTone(workerImpact.kind) as WorkspaceProblem['tone'], title,
      detail: [lead, `Recorded as ${ACTOR_STATE_LABEL[actor.state].toLowerCase()}` + (evidence?.at
        ? `; its last log line was ${relativeTime(evidence.at)}${evidence.recent ? '.' : ', so it may not be serving.'}`
        : evidence ? '; Radar found no output from it in the worker’s log, so it may not be serving.' : '.'),
        workerImpact.deadline && `The Pod is due to be gone ${relativeTime(workerImpact.deadline)}.`].filter(Boolean).join(' '),
      rawDetail: rest, rawDetailLabel: 'Details',
      nextStep: workerImpact.nextStep,
    } }
  }
  const [first, ...rest] = additional
  if (!first) return undefined
  const { title, message } = actorAttentionText(first, detail)
  const nextStep = first.nextStep
  const distinct = withoutRepeats(message, nextStep)
  const [lead, rawDetail] = distinct ? splitReading(distinct) : []
  return { additional: rest, problem: {
    ...base, id: first.id, category: first.rule, tone: attentionTone(first), title, detail: lead, rawDetail, rawDetailLabel: 'Details', nextStep,
  } }
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

const EGRESS_DENIED = 'so Substrate denies all outbound traffic'

export function egressText(e: SubstrateEgress): { text: ReactNode; tone: 'neutral' | 'unknown' } {
  switch (e.state) {
    case 'set': {
      if (e.rules === 0) return { text: `A policy with no rules, ${EGRESS_DENIED}`, tone: 'neutral' }
      const dest = e.destinations?.length ? ` · ${e.destinations.join(', ')}${e.more ? ` and ${e.more} more` : ''}` : ''
      return { text: `${e.rules} ${e.rules === 1 ? 'rule' : 'rules'}${dest}`, tone: 'neutral' }
    }
    case 'none':
      return { text: `No egress policy, ${EGRESS_DENIED}`, tone: 'neutral' }
    case 'denied':
      return { text: `Substrate denied permission to read this actor’s egress policy${e.message ? `: ${e.message}` : ''}`, tone: 'unknown' }
    default:
      return { text: `Couldn’t be read${e.message ? `: ${e.message}` : ''}`, tone: 'unknown' }
  }
}

function ActorAbout({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject?: OpenSubject }) {
  const { actor, template } = detail
  return (
    <FactGrid>
      <FactRow label="Template">
        <SubjectLink subject={{ kind: 'ActorTemplate', ...actor.template }} onOpen={onOpenSubject} withKind={false} />
        {template && <div className="text-xs text-theme-text-tertiary">
          {template.cpuMillicores !== undefined ? `${cpuText(template.cpuMillicores)} CPU` : 'CPU unreported'} · {template.memoryBytes !== undefined ? bytesText(template.memoryBytes) : 'memory unreported'} per actor
        </div>}
      </FactRow>
      <FactRow label="Sandbox class">{actor.assignment?.sandboxClass ?? detail.row.newestSnapshot?.sandboxClass ?? <Unread>Unknown</Unread>}</FactRow>
      <FactRow label="Seeded from">{actor.sourceTag ? <span className="font-mono">{refText(actor.sourceTag)}</span> : 'Template'}</FactRow>
      <FactRow label="Created">{actor.createTime ? <When iso={actor.createTime} /> : <Unread>Unknown</Unread>}</FactRow>
    </FactGrid>
  )
}

function ActorPlacement({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject?: OpenSubject }) {
  const { actor, row, worker } = detail
  const as = actor.assignment
  const node = row.node ? <SubjectLink subject={{ kind: 'Node', name: row.node }} onOpen={onOpenSubject} withKind={false} /> : undefined
  const pool = as?.pool ? <SubjectLink subject={{ kind: 'WorkerPool', namespace: as.namespace, name: as.pool }} label={as.pool} onOpen={onOpenSubject} withKind={false} /> : undefined
  return (
    <DimensionValue tone={detail.workerImpact ? workerImpactTone(detail.workerImpact.kind) : worker?.state === 'active' ? 'healthy' : as ? 'unknown' : 'neutral'}
      source={as ? <>Substrate assignment · namespace {as.namespace} · {worker ? `worker ${worker.state}` : 'worker state unknown'}
        {!worker && coverageReason(detail.coverage.workers, 'workers') && ` · ${coverageReason(detail.coverage.workers, 'workers')}`}
        {pool && <> · Pool {pool}</>}{node && <> · Node {node}</>}</>
        : <>No worker assignment in Substrate{node && <> · Last known node {node}</>}</>}>
      {as ? <SubjectLink subject={{ kind: 'Pod', namespace: as.namespace, name: as.pod }} label={as.pod} onOpen={onOpenSubject} withKind={false} />
        : actor.state === 'crashed' ? 'Unassigned while crashed' : 'Unassigned'}
    </DimensionValue>
  )
}

/** A line LogLines marks in place: the worker's memory-limit kill, of this crash or an earlier run. */
export interface MarkedLine {
  index: number
  /** The kill is tied to the present crash: an error rail; otherwise a label alone. */
  current: boolean
  label: string
  /** The line's element id, for "Jump to line". */
  id: string
}

export function LogLines({ lines, showPod, mark }: { lines: (SubstrateLogLine & { pod?: string; namespace?: string })[]; showPod?: boolean; mark?: MarkedLine }) {
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
      {lines.map((l, i) => {
        const marked = mark?.index === i ? mark : undefined
        return (
          <div key={i} id={marked?.id} className={clsx('whitespace-pre-wrap break-words', marked && 'relative')} data-marked={marked ? (marked.current ? 'current' : 'earlier') : undefined}>
            {marked?.current && <span aria-hidden className={clsx('absolute -left-2 bottom-0.5 top-0.5 w-0.5 rounded-sm', toneFillClass('unhealthy'))} />}
            {l.time && <span className="text-theme-text-tertiary">{new Date(l.time).toLocaleTimeString()} </span>}
            {showPod && l.pod && <span className="text-theme-text-tertiary">{l.namespace}/{l.pod} </span>}
            <span className="text-theme-text-tertiary">{l.kind === 'container' ? l.container ?? 'container' : l.kind} </span>
            {l.message ?? l.text}
            {marked && <span className={clsx('ml-2 font-sans text-[11px] font-medium', marked.current ? toneTextClass('unhealthy') : 'text-theme-text-tertiary')}>{marked.label}</span>}
          </div>
        )
      })}
    </pre>
  )
}

/** The loaded line that is the worker's memory-limit kill record, newest first; -1 when the lines read don't hold it. */
export function memoryKillLine(lines: (SubstrateLogLine & { pod?: string; namespace?: string })[], kill: NonNullable<SubstrateActorLogs['memoryKill']>): number {
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i]
    if (l.kind === 'worker' && l.memoryLimitBytes !== undefined && l.pod === kill.pod && l.namespace === kill.namespace && (!kill.time || l.time === kill.time)) return i
  }
  return -1
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

    </div>
  )
}


export function workerLogsAvailable(logs: SubstrateActorLogs): boolean {
  return logs.reasonCode === 'candidates' || logs.reasonCode === 'noMatch' ||
    (logs.origin === 'worker' && logs.reasonCode === undefined)
}

/** What a log read could not give, in product terms, from the server's reason code. */
function logGap(logs: SubstrateActorLogs, actor: SubstrateActorDetail['actor']): string {
  const node = logs.node ? `node ${logs.node}` : 'its node'
  switch (logs.reasonCode) {
    case 'workerPodGone':
      return `The worker Pod ${logs.worker?.pod ?? ''} is gone; Substrate still lists the worker, so its lines went with the Pod.`
    case 'nodeGone':
      return `The node it last ran on, ${logs.node ?? ''}, is gone from the cluster, and its worker Pods’ logs with it.`
    case 'noWorkerPods':
      return `No worker Pod runs on ${node} now: the one it ran in is gone, and its lines with it.`
    case 'podsDenied':
      return `Finding the worker Pods on ${node} requires list on pods in the worker namespaces.`
    case 'logsDenied':
      return `Reading the worker Pod logs on ${node} requires get on pods/log in the worker namespaces.`
    case 'podsNotCollected':
      return `Radar doesn’t hold Pods in every namespace, so it can’t search the worker Pods on ${node}.`
    case 'noWorker':
    default:
      return `A ${ACTOR_STATE_LABEL[actor.state].toLowerCase()} actor runs on no worker, and Substrate names no node it ran on.`
  }
}

function SearchedPods({ data }: { data: SubstrateActorLogs }) {
  if (data.searched.length === 0 && !data.morePods) return null
  return (
    <ul aria-label="Worker Pods searched" className="text-xs text-theme-text-tertiary">
      {data.searched.map((p) => (
        <li key={`${p.namespace}/${p.pod}`}>
          <span className="font-mono">{p.namespace}/{p.pod}</span>: {p.outcome === 'read' ? `read, ${p.matched} matching lines` : p.outcome === 'gone' ? 'gone'
            : p.outcome === 'denied' ? p.message ?? `requires get pods/log in ${p.namespace}` : `couldn’t be read${p.message ? `: ${p.message}` : ''}`}
        </li>
      ))}
      {data.morePods ? <li>{data.morePods} more worker {data.morePods === 1 ? 'Pod' : 'Pods'} left unsearched</li> : null}
    </ul>
  )
}

type LogsRead = { data?: SubstrateActorLogs; isLoading: boolean; error: unknown; isRefetchError?: boolean; dataUpdatedAt?: number }

export function ActorLogsBody({ actor, logs, compact, killNoticeAbove }: { actor: SubstrateActorDetail['actor']; logs: LogsRead; compact?: boolean; killNoticeAbove?: boolean }) {
  const lineId = useId()
  // A refresh that says the log can't be read any more replaces the lines kept from before.
  const data = definitiveFailure(substrateErrorCode(logs.error)) ? undefined : logs.data
  const stale = data && logs.isRefetchError ? <RefreshFailedNotice queries={[{ isRefetchError: true, error: logs.error instanceof Error ? logs.error : null, dataUpdatedAt: logs.dataUpdatedAt ?? 0 }]} /> : null
  if (logs.isLoading) return <p className="text-sm text-theme-text-tertiary">Reading the worker Pod’s log…</p>
  if (logs.error && !data) return <p className="text-sm text-theme-text-tertiary">Radar couldn’t read the log: {logs.error instanceof Error ? logs.error.message : 'unknown error'}.</p>
  if (!data) return <p className="text-sm text-theme-text-tertiary">Worker logs have not been read.</p>
  const crashedAt = actor.state === 'crashed' ? actor.crash?.time : undefined
  if (data.lines.length > 0) {
    const pods = [...new Set(data.lines.map((l) => `${l.namespace}/${l.pod}`))]
    const kill = data.memoryKill
    const at = kill ? memoryKillLine(data.lines, kill) : -1
    // Only a kill tied to this crash is its cause; an earlier run's is labelled as one.
    const mark = kill && at >= 0 ? { index: at, current: kill.current, label: killLineLabel(kill, crashedAt), id: lineId } : undefined
    // The cause in full is the Overview banner's; here one short line points at the line, with the timing that ties it.
    const tie = kill ? killTie(kill, crashedAt) : undefined
    const jump = mark && !compact ? (
      <button type="button" onClick={() => document.getElementById(mark.id)?.scrollIntoView({ block: 'center', behavior: 'smooth' })} className="ml-2 whitespace-nowrap text-xs font-medium text-accent-text hover:underline">
        Jump to line
      </button>
    ) : null
    return (
      <div className="space-y-1.5">
        {stale}
        {/* One provenance line: where these lines come from. The Overview's heading already says it for a current worker. */}
        {data.origin === 'worker' && data.worker && !compact && (
          <p className="text-xs text-theme-text-secondary">From its current worker Pod <span className="font-mono">{data.worker.namespace}/{data.worker.pod}</span>.</p>
        )}
        {data.origin === 'lastNode' && (
          <p className="text-xs text-theme-text-secondary">
            Candidates from {pods.length === 1 ? <span className="font-mono">{pods[0]}</span> : `${pods.length} worker Pods`} on <span className="font-mono">{data.node}</span>, the node it last ran on{crashedAt ? <>, before it crashed <When iso={crashedAt} /></> : ''}: Substrate doesn’t record which Pod it ran in{pods.length > 1 ? ', so each line names its Pod' : ''}.
          </p>
        )}
        {kill && !compact && !killNoticeAbove && (
          <p className="text-sm text-theme-text-secondary">
            {kill.current
              ? <>{at >= 0 ? 'Marked below: the worker’s memory-limit kill' : 'The worker’s log records a memory-limit kill'}{tie ? `, ${tie}` : ''}.</>
              : <>A memory-limit kill attempt was recorded{kill.time && <> <When iso={kill.time} /></>} in {kill.namespace}/{kill.pod}.{actor.state === 'crashed' ? ' Substrate’s crash cause is reported separately.' : ''}</>}
            {jump}
          </p>
        )}
        {data.partial && <p className="text-xs text-theme-text-secondary">Some worker Pod logs on this node could not be searched.</p>}
        {!compact && data.partial && <SearchedPods data={data} />}
        <LogLines lines={data.lines} showPod={data.origin === 'lastNode' && pods.length > 1} mark={mark} />
        {data.windowFull && <p className="text-xs text-theme-text-tertiary">Radar reached its log read limit. More lines may exist outside this window.</p>}
      </div>
    )
  }
  const noMatch = data.reasonCode === 'noMatch' || (data.origin === 'worker' && !data.reasonCode)
  const showSearch = !compact && (!noMatch || data.origin === 'lastNode' || data.morePods || data.searched.some((p) => p.outcome !== 'read'))
  return (
    <div className="space-y-1.5">
      {stale}
      <p className="text-sm text-theme-text-secondary">{noMatch ? 'No matching log lines.' : logGap(data, actor)}
        {!noMatch && crashedAt && <> It crashed <When iso={crashedAt} />.</>}
      </p>
      {noMatch && data.origin === 'lastNode' && <p className="text-xs text-theme-text-secondary">Radar searched worker Pods on {data.node}, the node where the actor last ran. {data.partial && 'Some worker Pod logs there could not be searched.'}</p>}
      {noMatch && data.windowFull && <p className="text-xs text-theme-text-tertiary">Radar scanned the newest lines. Older lines may exist outside that window.</p>}
      {showSearch && <SearchedPods data={data} />}
      {!compact && (!noMatch || data.windowFull || data.origin === 'lastNode') && <LogHandoff actor={actor} />}
    </div>
  )
}

export function SubstrateActorOverview({
  detail,
  onOpenSubject,
  onShowLogs,
  onShowSnapshots,
  onShowEgress,
  framed = true,
  actions,
}: {
  detail: SubstrateActorDetail
  onOpenSubject?: OpenSubject
  onShowLogs?: () => void
  onShowSnapshots?: () => void
  onShowEgress?: () => void
  framed?: boolean
  actions?: ReactNode
}) {
  const { actor, row, recovery } = detail
  const logs = useSubstrateActorLogs(actor, { tailLines: 8, pollMs: actor.assignment?.pod && actor.assignment.namespace ? undefined : false })
  const assessment = actorProblem(detail, logs.data)
  const plan = actor.state === 'crashed' ? crashPlan(detail, logs.data?.memoryKill) : undefined
  const otherRemedy = detail.workerImpact ?? detail.attention.find((a) => a.id === assessment?.problem.id)
  const command = actor.kagent ? undefined : plan ? plan.command : otherRemedy?.command
  const then = actor.kagent ? undefined : plan ? plan.thenCommand : otherRemedy && 'thenCommand' in otherRemedy ? otherRemedy.thenCommand : undefined
  const egress = egressText(detail.egress)
  const matchingLines = logs.data?.lines.length ?? 0
  const logsUnavailable = logs.data && !workerLogsAvailable(logs.data)
  const logValue = logs.isLoading ? 'Reading worker logs…'
    : logsUnavailable ? 'Worker logs unavailable'
    : logs.error ? 'Worker logs couldn’t be read'
    : !logs.data ? 'Worker logs not read'
    : matchingLines > 0 ? `${matchingLines} recent ${matchingLines === 1 ? 'line' : 'lines'} read${logs.data.partial ? ' from available worker Pods' : ''}`
    : logs.data.partial ? 'No matches in the worker logs Radar could search'
    : 'No matches in the recent log window'
  const restore = { memory: 'with memory', filesystem: 'with saved files', volumes: 'with saved volumes only', cold: 'as a fresh boot', unknown: 'with unreported fidelity' }[row.resumesWith]
  const recoveryText = row.resumeGeneration === undefined ? 'Boots fresh'
    : `Resumes from snapshot generation ${row.resumeGeneration} (${row.resumeCondition === 'compatibleRuntime' ? 'with memory, if the runtime is compatible' : restore})`
  const discarded = recovery?.discarded ?? []
  const golden = detail.attention.some((a) => a.rule === 'goldenSnapshotFailed')
  const pending = discarded.find((d) => d.why === 'uploadPending')
  const crashCount = detail.template?.actors?.byState.crashed
  const crashesPartial = detail.coverage.actors.state !== 'full'
  return (
    <div className="space-y-4">
      {assessment && (
        <WorkspaceProblemCallout problem={assessment.problem} rootKind="Actor" subjectIsSelf
          action={actor.state === 'crashed' && !!crashCount && crashCount > 1 && onOpenSubject && (
            <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenSubject({ kind: 'Actors', filter: { state: 'crashed', template: `${actor.template.atespace}/${actor.template.name}` } })}>
              {crashesPartial ? '≥' : ''}{crashCount} reported crashed on this template →
            </button>
          )}
          more={assessment.additional.length > 0 && (
            <FoldSection title={`+${assessment.additional.length} more`} summary="" attention={false}>
              <ul className="space-y-1 text-xs text-theme-text-secondary">
                {assessment.additional.map((a) => <li key={a.id}>{actorAttentionText(a, detail).title}</li>)}
              </ul>
            </FoldSection>
          )} />
      )}
      {actions}
      <WorkspaceCard framed={framed} title="At a glance">
        <FactGrid>
          <FactRow label="Recovery">
            <DimensionValue tone={row.resumesWith === 'unknown' ? 'unknown' : 'neutral'}
              source={actor.kagent ? 'Recovery is managed by kagent' : <>Current template · {resumeSentence(row)}</>} linkLabel="Snapshots" onOpen={onShowSnapshots}>
              {actor.kagent ? 'Recover through kagent' : recoveryText}
            </DimensionValue>
            {(command || (recovery && !actor.kagent && !golden)) && (
              <div className="mt-2">
                <FoldSection title="Recovery steps" summary="" attention={false}>
                  <div className="space-y-1.5 text-xs text-theme-text-secondary">
                    <p>Target actor <span className="font-mono">{refText(actor)}</span>.</p>
                    {recovery && !actor.kagent && !golden && (
                      <>
                        <p><RecoveryText recovery={recovery} newest={row.newestSnapshot?.generation} /></p>
                        {pending && <p>To keep generation {pending.generation}, wait for its upload to finish before reverting.</p>}
                        {discarded.length > 0 && <ul className="list-disc pl-4">
                          {discarded.map((d) => <li key={d.generation}>A revert drops generation {d.generation}{d.fidelity ? ` (${fidelityLabel(d.fidelity)})` : ''}: {DISCARD_WHY[d.why] ?? d.why}.</li>)}
                        </ul>}
                      </>
                    )}
                    {command && <CommandChip command={command} />}
                    {command && then && <CommandChip command={then} />}
                  </div>
                </FoldSection>
              </div>
            )}
          </FactRow>
          <FactRow label="Placement"><ActorPlacement detail={detail} onOpenSubject={onOpenSubject} /></FactRow>
          <FactRow label="Recent logs">
            <DimensionValue tone={!logs.data || logsUnavailable || logs.error ? 'unknown' : 'neutral'} source={logsUnavailable ? logGap(logs.data!, actor) : "Actor-filtered records from Kubernetes worker Pods"} linkLabel="Logs" onOpen={onShowLogs}>
              {logValue}
            </DimensionValue>
            {matchingLines > 0 && (
              <div className="mt-2">
                <FoldSection title="Recent lines" summary="" attention={false}><ActorLogsBody actor={actor} logs={logs} compact /></FoldSection>
              </div>
            )}
          </FactRow>
          <FactRow label="Outbound policy">
            <DimensionValue tone={egress.tone} source={detail.egress.state === 'none' || (detail.egress.state === 'set' && detail.egress.rules === 0) ? egress.text : 'Actor egress policy from Substrate'} linkLabel="Egress" onOpen={onShowEgress}>
              {detail.egress.state === 'none' || (detail.egress.state === 'set' && detail.egress.rules === 0) ? 'Default deny' : egress.text}
            </DimensionValue>
          </FactRow>
        </FactGrid>
      </WorkspaceCard>
      <WorkspaceCard framed={framed} title="About"><ActorAbout detail={detail} onOpenSubject={onOpenSubject} /></WorkspaceCard>
    </div>
  )
}
