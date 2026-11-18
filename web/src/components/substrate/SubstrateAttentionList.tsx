import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Collapse, CollapseChevron, Tooltip, toneTextClass, useDisclosure } from '@skyhook-io/k8s-ui'
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
        {n.oldest && <span className="ml-1.5 text-theme-text-tertiary"><When iso={n.oldest} prefix="waiting for at least" held /></span>}
      </span>
    ),
  }))
  // A crash row split by remedy names its templates beside each step already.
  const byGroup = groups.length > 1 && !splitsByRemedy(row)
  if (!byGroup && messages.length <= 1 && nodes.length === 0) return null
  return (
    <div className="grid gap-x-8 gap-y-2 text-xs text-theme-text-secondary lg:grid-cols-2">
      {byGroup && <TallyList title={groupTitle} items={groups} />}
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

/** A crash row's groups by remedy: those whose cause names one, and the rest, which share the row's general step. */
function crashRemedies(row: SubstrateAttention) {
  const remedies = groupRemedies(row)
  const rest = (row.groups ?? []).filter((g) => !g.nextStep)
  return { remedies, rest, restCount: rest.reduce((n, g) => n + g.count, 0) }
}

/** Whether a row splits its crashes by remedy: one remedy of a known cause shows, the others wait under Details. */
function splitsByRemedy(row: SubstrateAttention): boolean {
  const { remedies, rest } = crashRemedies(row)
  return remedies.length > 1 || (remedies.length === 1 && rest.length > 0)
}

function StepLine({ lead, step, command }: { lead: ReactNode; step: string; command?: string }) {
  return (
    <div className="mt-1 break-words text-xs text-theme-text-primary">
      <span className="font-medium">{lead}</span>
      {step}
      {command && <CommandChip command={command} />}
    </div>
  )
}

const CAUSE_WORDS: Record<NonNullable<Tally['cause']>, string> = { memoryLimit: 'killed over their memory limit' }

// A remedy names its templates on the visible line up to this many; more wait under Details.
const NAMED_REMEDY_GROUPS = 2

/** Which actors a remedy is for, by template: "on research / lg", "11 on research / lg and 1 on sandbox-dev / mcp", or "on 3 templates". */
function RemedyGroups({ row, groups, onOpen }: { row: SubstrateAttention; groups: Tally[]; onOpen?: OpenSubject }) {
  if (groups.length > NAMED_REMEDY_GROUPS) return <>on {groups.length} templates</>
  if (groups.length === 1) return <>on <GroupName row={row} group={groups[0]} onOpen={onOpen} /></>
  return (
    <>
      {groups.map((g, i) => (
        <span key={i}>
          {i > 0 && ' and '}
          {g.count} on <GroupName row={row} group={g} onOpen={onOpen} />
        </span>
      ))}
    </>
  )
}

/**
 * A row's one visible next step. A crash row whose groups name a cause shows
 * that cause's remedy, with its command, and one line for the rest, whose
 * general step waits under Details: no one command is offered as though every
 * crash shared a remedy.
 */
function NextStep({ row, onOpen, onShowDetails }: { row: SubstrateAttention; onOpen?: OpenSubject; onShowDetails: () => void }) {
  if (!splitsByRemedy(row)) return <StepLine lead="Next step: " step={row.nextStep} command={row.command} />
  const { remedies, rest } = crashRemedies(row)
  const first = remedies[0]
  const firstCount = first.groups.reduce((n, g) => n + g.count, 0)
  const others = row.count - firstCount
  const one = rest.length === 1 && remedies.length === 1 ? groupFilter(row, rest[0]) : undefined
  // A template lands in the rest when any of its crashes is not of the named
  // cause, so its others may be of it: they "include" other causes. A template
  // with one crash in the rest is that crash, so where every one has one, each
  // crashed for another reason.
  const otherCause = remedies.length === 1 && first.cause === 'memoryLimit'
  const otherWords = others === 1 ? 'The other one' : `The other ${others}`
  const otherLead = !otherCause
    ? `${otherWords} crashed`
    : rest.every((g) => g.count === 1)
      ? `${otherWords} crashed for another reason`
      : `${otherWords} include crashes for other reasons`
  // The step names which actors it is for, by template, so the concentration above and this count reconcile on sight.
  const lead = (
    <>
      Next step for {firstCount}{first.cause ? ` ${CAUSE_WORDS[first.cause]}` : ''}, <RemedyGroups row={row} groups={first.groups} onOpen={onOpen} />:{' '}
    </>
  )
  return (
    <>
      <StepLine lead={lead} step={first.nextStep} command={first.command} />
      {others > 0 && (
        <div className="mt-0.5 break-words text-xs text-theme-text-secondary">
          {otherLead}:{' '}
          {one && onOpen ? (
            <button type="button" onClick={() => onOpen({ kind: 'Actors', filter: one })} className="text-accent-text hover:underline">show {others === 1 ? 'it' : 'them'} →</button>
          ) : (
            <button type="button" onClick={onShowDetails} className="text-accent-text hover:underline">see Details</button>
          )}
        </div>
      )}
    </>
  )
}

/** What a split crash row leaves for Details: each other remedy, and the general step for the rest, naming their templates. */
function OtherSteps({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  if (!splitsByRemedy(row)) return null
  const { remedies, rest, restCount } = crashRemedies(row)
  return (
    <>
      {remedies[0].groups.length > NAMED_REMEDY_GROUPS && (
        <div className="break-words text-xs text-theme-text-secondary">
          {remedies[0].cause ? `${CAUSE_WORDS[remedies[0].cause][0].toUpperCase()}${CAUSE_WORDS[remedies[0].cause].slice(1)}` : 'With that step'}: <GroupNames row={row} groups={remedies[0].groups} onOpen={onOpen} />
        </div>
      )}
      {remedies.slice(1).map((r) => (
        <StepLine
          key={r.nextStep}
          lead={<>Next step for <GroupNames row={row} groups={r.groups} onOpen={onOpen} />{r.cause ? `, ${CAUSE_WORDS[r.cause]}` : ''}: </>}
          step={r.nextStep}
          command={r.command}
        />
      ))}
      {rest.length > 0 && (
        <StepLine lead={<>For the other {restCount === 1 ? 'one' : restCount}, on <GroupNames row={row} groups={rest} onOpen={onOpen} />: </>} step={row.nextStep} />
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

/** A row's Details fold: a compact header under the step, so a row costs one line for it. */
function RowDetails({ open, onToggle, children }: { open: boolean; onToggle: () => void; children: ReactNode }) {
  const d = useDisclosure(open)
  return (
    <div className="mt-1">
      <button type="button" {...d.buttonProps} onClick={onToggle} className="inline-flex items-center gap-1 rounded text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary hover:text-theme-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent">
        <CollapseChevron open={open} className="h-3 w-3" />
        Details
      </button>
      <Collapse open={open} id={d.panelId}>
        <div className="space-y-1.5 pb-0.5 pl-4 pt-1">{children}</div>
      </Collapse>
    </div>
  )
}

/**
 * One row, in the anatomy Velero's and Agent Sandbox's Needs attention rows
 * share, kept to what an operator decides on: the title in its severity's
 * tone; one line with what it is about, its age and the way to the affected
 * objects; the cause in a sentence; one next step with at most one command.
 * Everything else folds under Details: the rest of the reading (mechanics,
 * epochs, "Latest: …", Substrate's raw messages), secondary remedies, related
 * objects and the breakdowns. A crash row's visible cause is where its
 * crashes concentrate; its "Latest: …" reading is provenance.
 */
export function AttentionRow({ row, onOpen }: { row: SubstrateAttention; onOpen?: OpenSubject }) {
  const [open, setOpen] = useState(false)
  const related = (row.related ?? [])
    // A worker is its Pod: the subject already links it.
    .filter((r) => !(row.subject.kind === 'Worker' && r.kind === 'Pod' && r.name === row.subject.name))
  const crash = row.rule === 'crashedActors'
  const [first, others] = splitReading(row.reading)
  const lead = crash ? undefined : first
  const rest = crash ? row.reading || undefined : others
  const hasDetails = related.length > 0 || splitsByRemedy(row) || (row.groups?.length ?? 0) > 1 || (row.messages?.length ?? 0) > 1 || (row.nodes?.length ?? 0) > 0 || (row.sample?.length ?? 0) > 0 || !!rest
  return (
    <li className="min-w-0 text-sm" data-rule={row.rule} data-severity={row.severity}>
      <div className={clsx('font-medium break-words', toneTextClass(attentionTone(row)))}>{row.title}</div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
        <SubjectLink subject={row.subject} onOpen={onOpen} count={row.subject.kind === 'Actors' ? row.count : undefined} short />
        <WorkerActorsLink row={row} onOpen={onOpen} />
        {row.since && <When iso={row.since} prefix="for at least" held hint="Newest change among them: the condition has held at least since" />}
      </div>
      <Concentration row={row} onOpen={onOpen} />
      {lead && <div className="break-words text-xs text-theme-text-secondary">{lead}</div>}
      <NextStep row={row} onOpen={onOpen} onShowDetails={() => setOpen(true)} />
      {hasDetails && (
        <RowDetails open={open} onToggle={() => setOpen((v) => !v)}>
          {rest && <div className="break-words text-xs text-theme-text-secondary">{rest}</div>}
          <OtherSteps row={row} onOpen={onOpen} />
          {related.length > 0 && (
            <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
              {related.map((r) => <SubjectLink key={`${r.kind}/${r.namespace}/${r.name}`} subject={r} onOpen={onOpen} short />)}
            </div>
          )}
          <AffectedActors row={row} onOpen={onOpen} />
          <Tallies row={row} onOpen={onOpen} />
        </RowDetails>
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
      <ul className="space-y-3">
        {shown.map((r) => <AttentionRow key={r.id} row={r} onOpen={onOpen} />)}
      </ul>
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <ul className="space-y-3 pt-3">
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
