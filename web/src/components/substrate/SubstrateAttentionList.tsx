import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Collapse, CollapseChevron, FoldSection, Tooltip, toneTextClass } from '@skyhook-io/k8s-ui'
import type { SubstrateAttention, SubstrateSubject } from '../../api/substrateWorkspace'
import { SUBJECT_KIND_LABEL, attentionTone, splitReading, subjectLabel } from './readings'
import { CommandChip, Tokens, When } from './shared'

/** Opens what a row or link is about: a filtered actor list, an actor, a template, or a Kubernetes object. */
export type OpenSubject = (subject: SubstrateSubject) => void

// Rows shown before folding: every critical row, and at least this many. The
// list keeps the server's ranking (severity first), so the fold never hides
// something more urgent than what shows.
const ATTENTION_BUDGET = 3

const WORKER_RULES = new Set<SubstrateAttention['rule']>(['workerUnavailable', 'workerDraining', 'workerRestarting'])

/** A worker row's actors, as the server linked them: shown under the worker, not as rows of their own. */
function AffectedActors({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const sample = row.sample ?? []
  if (!WORKER_RULES.has(row.rule) || sample.length === 0) return null
  const shown = sample.slice(0, 4)
  const more = row.count - shown.length
  return (
    <div className="mt-0.5 break-words text-xs text-theme-text-secondary">
      Actors on it:{' '}
      {shown.map((a, i) => (
        <span key={`${a.atespace}/${a.name}`}>
          {i > 0 && ', '}
          {onOpen ? (
            <button type="button" onClick={() => onOpen({ kind: 'Actor', atespace: a.atespace, name: a.name })} className="font-mono text-accent-text hover:underline">{a.name}</button>
          ) : (
            <span className="font-mono">{a.name}</span>
          )}
        </span>
      ))}
      {more > 0 && (
        onOpen && row.subject.kind === 'Worker' && row.subject.name ? (
          <> and <button type="button" onClick={() => onOpen({ kind: 'Actors', filter: { worker: row.subject.name! } })} className="text-accent-text hover:underline">{more} more →</button></>
        ) : (
          <> and {more} more</>
        )
      )}
    </div>
  )
}

const NAMESPACED = new Set<SubstrateSubject['kind']>(['WorkerPool', 'Pod', 'Worker'])

/**
 * A link to what a row names. `short` drops the namespace from a pool, worker
 * or Pod (it shows on hover): in a list, every one of them repeating it is noise.
 */
export function SubjectLink({ subject, onOpen, count, withKind = true, label: shown, short }: { subject: SubstrateSubject; onOpen?: OpenSubject; count?: number; withKind?: boolean; label?: string; short?: boolean }) {
  const abbreviated = short && NAMESPACED.has(subject.kind) && subject.name
  const label = shown ?? (abbreviated ? subject.name! : subjectLabel(subject, count))
  const kind = withKind && subject.kind !== 'Actors' ? `${SUBJECT_KIND_LABEL[subject.kind]} ` : ''
  const body = !onOpen ? (
    <span className="font-mono">{label}</span>
  ) : (
    <button type="button" onClick={() => onOpen(subject)} className={clsx('text-left text-accent-text hover:underline', subject.kind !== 'Actors' && 'font-mono break-normal')}>
      {subject.kind === 'Actors' ? label : <Tokens text={label} />}
      {subject.kind === 'Actors' && ' →'}
    </button>
  )
  return (
    <span>
      {kind}
      {abbreviated ? <Tooltip content={subjectLabel(subject)}><span>{body}</span></Tooltip> : body}
    </span>
  )
}

const NODE_STATE: Record<string, string | undefined> = {
  ready: 'Ready',
  notReady: 'not Ready',
  gone: 'gone from the cluster',
}

function TallyList({ title, items }: { title: string; items: { key: string; label: ReactNode; count: number }[] }) {
  return (
    <div className="min-w-0">
      <div className="text-theme-text-tertiary">{title}</div>
      <ul className="mt-0.5 space-y-0.5">
        {items.map((it) => (
          <li key={it.key} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-3">
            <span className="min-w-0 break-words">{it.label}</span>
            <span className="whitespace-nowrap text-theme-text-tertiary">{it.count === 1 ? '1 actor' : `${it.count} actors`}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** The Actors list narrowed to one group of a row: its template, or the worker the actors wait on. */
function groupFilter(row: SubstrateAttention, g: NonNullable<SubstrateAttention['groups']>[number]): Record<string, string> | undefined {
  if (g.subject?.kind === 'ActorTemplate') return { ...row.subject.filter, template: `${g.subject.atespace}/${g.subject.name}` }
  if (g.subject?.kind === 'Worker' && g.subject.name) return { ...row.subject.filter, worker: g.subject.name }
  return undefined
}

/** A row's breakdowns as short lists: actors per template or per worker, Substrate's most common messages, the nodes holding them. */
function Tallies({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const groups = (row.groups ?? []).map((g, i) => {
    const filter = groupFilter(row, g)
    return {
      key: g.subject ? `${g.subject.kind}/${g.subject.atespace ?? g.subject.namespace ?? ''}/${g.subject.name}` : g.text ?? String(i),
      count: g.count,
      label:
        filter && onOpen ? (
          <button type="button" onClick={() => onOpen({ kind: 'Actors', filter })} className="text-left font-mono text-accent-text hover:underline">
            {g.subject?.kind === 'Worker' ? g.subject.name : subjectLabel(g.subject!)}
          </button>
        ) : (
          <span className="font-mono">{g.subject ? subjectLabel(g.subject) : g.text}</span>
        ),
    }
  })
  const groupTitle = row.groups?.[0]?.subject?.kind === 'Worker' ? 'By worker' : 'By template'
  const messages = (row.messages ?? []).map((m, i) => ({ key: m.text ?? String(i), count: m.count, label: m.text }))
  const nodes = (row.nodes ?? []).map((n) => ({
    key: n.node || '(unnamed)',
    count: n.count,
    label: (
      <span>
        {n.node ? (
          onOpen ? (
            <Tooltip content="The actors whose newest snapshot is stored only on this node">
              <button type="button" onClick={() => onOpen({ kind: 'Actors', filter: { node: n.node, nodeHas: 'localSnapshot' } })} className="font-mono text-accent-text hover:underline">{n.node}</button>
            </Tooltip>
          ) : (
            <span className="font-mono">{n.node}</span>
          )
        ) : (
          'a node Substrate does not name'
        )}
        {NODE_STATE[n.state] && <span className={clsx('ml-1.5', n.state === 'ready' ? 'text-theme-text-tertiary' : toneTextClass('unhealthy'))}>{NODE_STATE[n.state]}</span>}
        {n.oldest && <span className="ml-1.5 text-theme-text-tertiary"><When iso={n.oldest} prefix="waiting since" /></span>}
      </span>
    ),
  }))
  if (groups.length <= 1 && messages.length <= 1 && nodes.length === 0) return null
  return (
    <div className="mt-1 grid gap-x-8 gap-y-2 text-xs text-theme-text-secondary lg:grid-cols-2">
      {groups.length > 1 && <TallyList title={groupTitle} items={groups} />}
      {messages.length > 1 && <TallyList title="Substrate’s messages" items={messages} />}
      {nodes.length > 0 && <TallyList title="Nodes holding them" items={nodes} />}
    </div>
  )
}

type Tally = NonNullable<SubstrateAttention['groups']>[number]

/** A row's groups that carry a remedy of their own, one entry per distinct remedy. */
function groupRemedies(row: SubstrateAttention): { nextStep: string; command?: string; cause?: Tally['cause']; groups: Tally[] }[] {
  const byStep = new Map<string, { nextStep: string; command?: string; cause?: Tally['cause']; groups: Tally[] }>()
  for (const g of row.groups ?? []) {
    if (!g.nextStep) continue
    const key = `${g.nextStep}\u0000${g.command ?? ''}`
    const entry = byStep.get(key) ?? { nextStep: g.nextStep, command: g.command, cause: g.cause, groups: [] }
    entry.groups.push(g)
    byStep.set(key, entry)
  }
  return [...byStep.values()]
}

/** A crash group's actors, as a filtered list: the template's name links to its crashed actors. */
function GroupName({ row, group, onOpen }: { row: SubstrateAttention; group: Tally; onOpen?: OpenSubject }) {
  const filter = groupFilter(row, group)
  const label = group.subject ? subjectLabel(group.subject) : group.text
  return filter && onOpen ? (
    <button type="button" onClick={() => onOpen({ kind: 'Actors', filter })} className="font-mono text-accent-text hover:underline">{label}</button>
  ) : (
    <span className="font-mono">{label}</span>
  )
}

function GroupNames({ row, groups, onOpen }: { row: SubstrateAttention; groups: Tally[]; onOpen?: OpenSubject }) {
  return (
    <>
      {groups.map((g, i) => (
        <span key={i}>
          {i > 0 && (i === groups.length - 1 ? ' and ' : ', ')}
          <GroupName row={row} group={g} onOpen={onOpen} /> ({g.count})
        </span>
      ))}
    </>
  )
}

/**
 * Where most of a crash row's actors are: "11 of the 15 use template X". It
 * states a concentration, not a cause; the remedies below say what is known
 * of causes.
 */
function Concentration({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const groups = row.groups ?? []
  const top = groups[0]
  if (row.rule !== 'crashedActors' || groups.length < 2 || !top?.subject || top.subject.kind !== 'ActorTemplate' || top.count * 2 <= row.count) return null
  return (
    <div className="break-words text-xs font-medium text-theme-text-primary">
      {top.count} of the {row.count} use template <GroupName row={row} group={top} onOpen={onOpen} />
    </div>
  )
}

/**
 * A row's next step. A crash row whose groups name a cause gives each its
 * own remedy, and the rest the general step without a command: no one command
 * is offered as though every crash shared a remedy.
 */
function NextSteps({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const remedies = groupRemedies(row)
  const rest = (row.groups ?? []).filter((g) => !g.nextStep)
  if (remedies.length === 0 || (remedies.length === 1 && rest.length === 0)) {
    return (
      <div className="mt-0.5 break-words text-xs text-theme-text-primary">
        <span className="font-medium">Next step: </span>
        {row.nextStep}
        {row.command && <> <CommandChip command={row.command} inline /></>}
      </div>
    )
  }
  const restCount = rest.reduce((n, g) => n + g.count, 0)
  return (
    <>
      {remedies.map((r) => (
        <div key={r.nextStep} className="mt-0.5 break-words text-xs text-theme-text-primary">
          <span className="font-medium">
            Next step for <GroupNames row={row} groups={r.groups} onOpen={onOpen} />
            {r.cause === 'memoryLimit' ? ', killed over their memory limit' : ''}:{' '}
          </span>
          {r.nextStep}
          {r.command && <> <CommandChip command={r.command} inline /></>}
        </div>
      ))}
      {rest.length > 0 && (
        <div className="mt-0.5 break-words text-xs text-theme-text-primary">
          <span className="font-medium">
            For the other {restCount === 1 ? 'one' : restCount}, on <GroupNames row={row} groups={rest} onOpen={onOpen} />:{' '}
          </span>
          {row.nextStep}
        </div>
      )}
    </>
  )
}

/** A worker row's link to the actors on that worker, beside its subject: the row is about them. */
function WorkerActorsLink({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  if (!WORKER_RULES.has(row.rule) || row.subject.kind !== 'Worker' || !row.subject.name || row.count <= 0) return null
  const label = `Show the ${row.count === 1 ? 'actor' : `${row.count} actors`}`
  return onOpen ? (
    <button type="button" onClick={() => onOpen({ kind: 'Actors', filter: { worker: row.subject.name! } })} className="whitespace-nowrap text-accent-text hover:underline">{label} →</button>
  ) : (
    <span>{label}</span>
  )
}

/**
 * One row, in the anatomy Velero's and Agent Sandbox's Needs attention rows
 * share: the title in its tone, the subject, Radar's reading, the next step,
 * and the rest folded last. Here the fold holds the rest of the reading and
 * the breakdowns (by template or worker, Substrate's messages, nodes, the
 * actors on a worker). A worker row's reading shows whole: what happened to
 * its actors must never be behind the fold.
 */
export function AttentionRow({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const related = (row.related ?? [])
    // A worker is its Pod: the subject already links it.
    .filter((r) => !(row.subject.kind === 'Worker' && r.kind === 'Pod' && r.name === row.subject.name))
  const [first, others] = splitReading(row.reading)
  const whole = WORKER_RULES.has(row.rule)
  const lead = whole ? row.reading : first
  const rest = whole ? undefined : others
  const hasDetails = related.length > 0 || (row.groups?.length ?? 0) > 1 || (row.messages?.length ?? 0) > 1 || (row.nodes?.length ?? 0) > 0 || (row.sample?.length ?? 0) > 0 || !!rest
  return (
    <li className="min-w-0 text-sm" data-rule={row.rule}>
      <div className={clsx('font-medium break-words', toneTextClass(attentionTone(row)))}>{row.title}</div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
        <SubjectLink subject={row.subject} onOpen={onOpen} count={row.subject.kind === 'Actors' ? row.count : undefined} short />
        <WorkerActorsLink row={row} onOpen={onOpen} />
        {row.since && <When iso={row.since} prefix="since" hint="Newest change among them: the condition has held at least since" />}
      </div>
      <Concentration row={row} onOpen={onOpen} />
      {lead && <div className="break-words text-xs text-theme-text-secondary">{lead}</div>}
      <NextSteps row={row} onOpen={onOpen} />
      {hasDetails && (
        <FoldSection title="Details" summary="" attention={false}>
          <div className="space-y-1.5">
            {rest && <div className="break-words text-xs text-theme-text-secondary">{rest}</div>}
            {related.length > 0 && (
              <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
                {related.map((r) => <SubjectLink key={`${r.kind}/${r.namespace}/${r.name}`} subject={r} onOpen={onOpen} short />)}
              </div>
            )}
            <AffectedActors row={row} onOpen={onOpen} />
            <Tallies row={row} onOpen={onOpen} />
          </div>
        </FoldSection>
      )}
    </li>
  )
}

/**
 * Needs attention as one list in the server's order (severity first, then
 * the rules that cost most), each row labelled with what it is about. The
 * first rows show; the rest fold.
 */
export function SubstrateAttentionList({ rows, onOpen }: { rows: SubstrateAttention[]; onOpen?: OpenSubject }) {
  const [showAll, setShowAll] = useState(false)
  const visible = Math.max(ATTENTION_BUDGET, rows.filter((r) => r.severity === 'critical').length)
  const shown = rows.slice(0, visible)
  const folded = rows.slice(visible)
  const foldId = 'substrate-attention-more'
  return (
    <div>
      <ul className="space-y-2.5">
        {shown.map((r) => <AttentionRow key={r.id} row={r} onOpen={onOpen} />)}
      </ul>
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <ul className="space-y-2.5 pt-2.5">
              {folded.map((r) => <AttentionRow key={r.id} row={r} onOpen={onOpen} />)}
            </ul>
          </Collapse>
          <button
            type="button"
            aria-expanded={showAll}
            aria-controls={foldId}
            onClick={() => setShowAll((v) => !v)}
            className="mt-2 inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
          >
            <CollapseChevron open={showAll} />
            {showAll ? 'Show fewer' : `Show ${folded.length} more`}
          </button>
        </>
      )}
    </div>
  )
}
