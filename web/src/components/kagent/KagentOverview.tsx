import { Link } from 'react-router-dom'
import { FoldSection, ProblemList } from '@skyhook-io/k8s-ui'
import type { KagentRef, KagentWorkspace } from '../../api/kagent'
import { KagentAgentsTable } from './KagentLists'
import {
  countText,
  changeText,
  findingProblem,
  KagentCountValue,
  KagentName,
  consumersText,
  effectText,
  refKey,
  refName,
  KagentRuntimeRead,
  type KagentRuntime,
} from './shared'

export function KagentAttention({ data, onOpen, agent }: { data: KagentWorkspace; onOpen: (ref: KagentRef) => void; agent?: KagentRef }) {
  const byId = new Map(data.findings.map((f) => [f.id, f]))
  const readings = new Map([...data.agents, ...data.models, ...data.mcpServers].map((o) => [refKey(o.ref), o.reading]))
  const foldedByLead = new Map<string, typeof data.findings>()
  for (const group of data.groups)
    for (const folded of group.folded ?? []) {
      const finding = byId.get(folded.finding)
      if (!finding) continue
      const rows = foldedByLead.get(folded.under) ?? []
      rows.push(finding)
      foldedByLead.set(folded.under, rows)
    }
  const references = new Set(data.agents.find((a) => agent && refKey(a.ref) === refKey(agent))?.references.map((r) => refKey(r.target)))
  const groups = data.groups.filter(
    (g) =>
      !agent ||
      [...g.findings, ...(g.folded?.map((f) => f.finding) ?? [])].some((id) => {
        const f = byId.get(id)
        return (
          f &&
          (refKey(f.subject) === refKey(agent) ||
            references.has(refKey(f.subject)) ||
            f.affects?.agents.some((a) => refKey(a.agent) === refKey(agent)))
        )
      }),
  )
  const ordered = [...groups].sort((a, b) => {
    const priority = (g: typeof a) => (g.severity === 'critical' ? 0 : g.findings.some((id) => byId.get(id)?.affects?.count) ? 1 : 2)
    return priority(a) - priority(b)
  })
  if (!ordered.length)
    return (
      <p className="text-sm text-theme-text-secondary">
        {data.counts.findings.lowerBound ? 'Nothing in what Radar can read needs attention.' : 'Nothing needs attention.'}
      </p>
    )
  const renderGroup = (g: (typeof ordered)[number]) => (
    <div key={g.id} className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      {g.secret && (
        <p className="mb-2 text-sm font-medium text-theme-text-primary">
          {g.findings.length > 1 ? 'Shared API key Secret' : 'API key Secret'}:{' '}
          <span className="font-mono text-xs">{refName(g.secret)}</span>
        </p>
      )}
      {g.findings.map((id) => {
        const finding = byId.get(id)
        if (!finding) return null
        const problem = {
          ...findingProblem(finding, readings),
          ...(g.severity === 'critical' ? { tone: 'unhealthy' as const } : {}),
        }
        const folded = foldedByLead.get(id) ?? []
        const messages = [problem.rawDetail, ...folded.map((f) => readings.get(refKey(f.subject))?.message)].filter(Boolean)
        const impact = finding.affects
        return (
          <div key={id} className="space-y-1.5">
            <ProblemList
              rootKind="kagent"
              subjectFirst
              hideOrigin
              onNavigate={(r) => onOpen({ ...r, group: r.group ?? '' })}
              problems={[{ ...problem, nextStep: undefined, rawDetail: undefined }]}
            />
            {impact && impact.count > 0 && (
              <div className="text-xs text-theme-text-secondary">
                <p>Affects {consumersText(impact)}:</p>
                <ul className="mt-1 space-y-1">
                  {impact.agents.slice(0, 8).map((a) => (
                    <li key={refKey(a.agent)} className="flex flex-wrap items-baseline gap-x-2">
                      <KagentName object={a.agent} onOpen={onOpen} />
                      <span>
                        {a.agent.namespace} · {effectText(a.effect)}
                        {folded.some((f) => refKey(f.subject) === refKey(a.agent) && f.reason === 'KagentNoPreparedRevision')
                          ? ' · no session can start'
                          : ''}
                      </span>
                    </li>
                  ))}
                </ul>
                {impact.count > 8 && <p className="mt-1">and {impact.count - 8} more Agents.</p>}
              </div>
            )}
            <p className="text-xs text-theme-text-primary">
              <span className="font-medium">Next step: </span>
              {finding.action}
            </p>
            {messages.length > 0 && (
              <FoldSection title="kagent's message" summary="" attention={false}>
                <p className="whitespace-pre-wrap break-words text-xs text-theme-text-secondary">{messages.join('\n')}</p>
              </FoldSection>
            )}
          </div>
        )
      })}
    </div>
  )
  const unused = ordered.filter((g) => !agent && g.severity !== 'critical' && g.findings.every((id) => byId.get(id)?.affects?.count === 0))
  const unusedIds = new Set(unused.map((g) => g.id))
  const active = ordered.filter((g) => !unusedIds.has(g.id))
  const incomplete = unused.some((g) => g.findings.some((id) => byId.get(id)?.affects?.lowerBound))
  const unusedSubjects = new Map(
    unused
      .flatMap((g) => g.findings)
      .flatMap((id) => {
        const f = byId.get(id)
        return f ? [[refKey(f.subject), f.subject] as const] : []
      }),
  )
  const unusedKinds = [...unusedSubjects.values()]
  const unusedCounts = [
    ['RemoteMCPServer', 'MCP server'],
    ['ModelConfig', 'model'],
    ['Agent', 'Agent'],
  ]
    .flatMap(([kind, label]) => {
      const n = unusedKinds.filter((ref) => ref.kind === kind).length
      return n ? [`${n} ${label}${n === 1 ? '' : 's'}`] : []
    })
    .join(', ')
  return (
    <div className="space-y-3">
      {active.map(renderGroup)}
      {unused.length > 0 && (
        <FoldSection
          title={unusedCounts}
          summary={incomplete ? 'no Agent consumers seen — may be staged' : 'no Agent uses — may be staged'}
          attention={false}
        >
          <div className="space-y-3">{unused.map(renderGroup)}</div>
        </FoldSection>
      )}
    </div>
  )
}
export function KagentOverview({
  data,
  runtime,
  onOpen,
}: {
  data: KagentWorkspace
  runtime: KagentRuntime
  onOpen: (ref: KagentRef) => void
}) {
  const states = data.counts.agentsByLatestChange
  const bound = data.counts.agents.lowerBound
  const unread = (key: 'agents' | 'models' | 'mcpServers') => ['denied', 'uncached', 'syncing', 'error'].includes(data.coverage[key].state)
  const credentialProblems = {
    n: new Set(data.findings.filter((f) => f.reason === 'KagentAPIKeySecretMissing').map((f) => refKey(f.subject))).size,
    lowerBound: data.counts.models.lowerBound,
  }
  const rows = runtime.rows
  const runtimePartial = runtime.lowerBound || !runtime.completePage || rows.some((r) => !r.complete)
  const totalSessions = rows.reduce((n, r) => n + r.sessions.total, 0)
  const runtimeText =
    rows.length || runtime.completePage
      ? `${runtimePartial ? '≥' : ''}${totalSessions} ${!runtimePartial && totalSessions === 1 ? 'session' : 'sessions'} · ${runtimePartial ? '≥' : ''}${rows.reduce((n, r) => n + r.crashed, 0)} crashed`
      : runtime.unavailable
  return (
    <>
      <div className="flex flex-wrap gap-x-5 gap-y-2 text-sm text-theme-text-primary" aria-label="kagent status">
        <span>
          Agents:{' '}
          {unread('agents') ? (
            'unknown'
          ) : (
            <>
              {bound ? '≥' : ''}
              {states.prepared ?? 0} prepared · {bound ? '≥' : ''}
              {states.blocked ?? 0} blocked
              {states.failed ? ` · ${bound ? '≥' : ''}${states.failed} failed` : ''}
              {states.preparing ? ` · ${bound ? '≥' : ''}${states.preparing} processing` : ''}
              {Object.entries(states)
                .filter(([state, count]) => !['prepared', 'blocked', 'failed', 'preparing'].includes(state) && count > 0)
                .map(([state, count]) => ` · ${bound ? '≥' : ''}${count} ${changeText(state).toLowerCase()}`)}
            </>
          )}
        </span>
        <span>
          MCP servers:{' '}
          {unread('mcpServers') ? (
            'unknown'
          ) : (
            <>
              <Link to="/kagent/mcpservers?status=problems" className="text-accent-text hover:underline">
                <KagentCountValue count={data.counts.mcpServersFailing} /> failing
              </Link>
            </>
          )}
        </span>
        <span>
          Models:{' '}
          {unread('models') ? (
            'unknown'
          ) : (
            <>
              <Link to="/kagent/models?status=problems" className="text-accent-text hover:underline">
                <KagentCountValue count={credentialProblems} /> with credential problems
              </Link>
            </>
          )}
        </span>
        <span className="text-theme-text-secondary">Substrate: {runtimeText}</span>
      </div>
      <KagentRuntimeRead runtime={runtime} />
      <section className="space-y-2">
        <h2 className="text-sm font-semibold text-theme-text-primary">
          Needs attention{' '}
          <span className="font-normal text-theme-text-tertiary">
            · {countText(data.counts.findings)} configuration{' '}
            {data.counts.findings.n === 1 && !data.counts.findings.lowerBound ? 'finding' : 'findings'} in {data.counts.groups.n}{' '}
            {data.counts.groups.n === 1 ? 'group' : 'groups'}
            {data.counts.groups.incomplete ? ' (grouping incomplete)' : ''}
          </span>
        </h2>
        <KagentAttention data={data} onOpen={onOpen} />
      </section>
      <KagentAgentsTable data={data} runtime={runtime} onOpen={onOpen} />
    </>
  )
}
