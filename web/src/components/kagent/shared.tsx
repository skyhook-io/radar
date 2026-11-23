import { shortRevision } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import { useEffect, type ReactNode } from 'react'
import { Bot } from 'lucide-react'
import { CertaintyGlyph, FactSource, FactValue, PaneLoader, StatusDot, TruncatedText, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import {
  useKagentSessionSummary,
  type KagentCount,
  type KagentCredential,
  type KagentFinding,
  type KagentImpact,
  type KagentReading,
  type KagentRef,
  type KagentSessionRow,
} from '../../api/kagent'
import { useSubstrateStatus } from '../../api/substrateWorkspace'
import { issueReasonTitle } from '@skyhook-io/k8s-ui/components/issues'
import { useConnection } from '../../context/ConnectionContext'
import { coverageLabel, INCOMPLETE_COVERAGE_STATES, type WorkspaceKindCoverage } from '../workspace/coverage'
import { GrantText, Notice, ScreenEmptyState, Sub } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { tierReading } from '../substrate/readings'
import { sessionsFailure } from './KagentAgentSessions'
import { KAGENT_GENERIC_AGENTS_PATH, kagentPath } from './routes'

export const refKey = (ref: KagentRef) => `${ref.kind}/${ref.namespace}/${ref.name}`
export const refName = (ref: Pick<KagentRef, 'namespace' | 'name'>) => `${ref.namespace}/${ref.name}`
export const revisionText = (revision: string | undefined) => shortRevision(revision) || 'No revision prepared'
export const countText = (count: KagentCount) => `${count.lowerBound ? '≥' : ''}${count.n.toLocaleString('en-US')}`
export const changeText = (state: string) =>
  ({
    prepared: 'Prepared',
    preparing: 'Processing',
    blocked: 'Blocked',
    failed: 'Failed',
    notProcessed: 'Not yet processed',
    notReported: 'Not reported',
    notReady: 'Not ready',
    deleting: 'Deleting',
    unknown: 'Unknown',
  })[state] ?? 'Unknown'
export const roleText = (role: string) =>
  ({
    model: 'Agent model',
    subagentModel: 'Subagent model',
    summarizer: 'Summarizer',
    embedding: 'Embedding',
    mcpServer: 'MCP server',
    subagentMcpServer: 'Subagent MCP server',
    template: 'Template',
    subagentTemplate: 'Subagent template',
    harness: 'Harness',
    workerPool: 'Worker pool',
    egressPolicy: 'Egress policy',
    systemPrompt: 'System prompt',
    promptSource: 'Prompt source',
    outputSchema: 'Output schema',
  })[role] ?? role
export const effectText = (effect: string) =>
  effect === 'blocked' ? 'blocked (first reported cause)' : effect === 'mayRunWithoutTools' ? 'may run without its tools' : 'uses it'

export function KagentCountValue({ count }: { count: KagentCount }) {
  return (
    <span className="inline-flex items-center gap-1">
      {count.lowerBound && <CertaintyGlyph certainty="lower_bound" />}
      {count.n.toLocaleString('en-US')}
    </span>
  )
}
export function KagentName({ object, onOpen }: { object: KagentRef; onOpen?: (ref: KagentRef) => void }) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const path = kagentPath(object, connection.context)
  return (
    <TruncatedText text={object.name}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          if (onOpen) onOpen(object)
          else if (path) navigate(path)
        }}
        className="text-left font-mono text-xs text-accent-text hover:underline"
      >
        {object.name}
      </button>
    </TruncatedText>
  )
}
export function Reading({
  reading,
  text,
  stateOnly = false,
}: {
  stateOnly?: boolean
  reading: Pick<KagentReading, 'state' | 'text' | 'tone' | 'condition' | 'summary'>
  text?: string
}) {
  const fact = {
    text: text ?? reading.text,
    tone: reading.tone,
    source: `kagent${reading.condition ? ` · ${reading.condition}` : ''}`,
    detail: stateOnly ? undefined : reading.summary,
  }
  return (
    <div className="inline-flex max-w-full items-baseline gap-1.5">
      <StatusDot tone={reading.tone} />
      <div>
        <FactValue fact={fact} />
        <FactSource fact={fact} />
      </div>
    </div>
  )
}
export function CredentialText({ credential }: { credential: KagentCredential }) {
  return credential.secret ? (
    <span className="break-words font-mono text-xs">
      {refName(credential.secret)}
      {credential.key ? ` · key ${credential.key}` : ' · key not named'}
    </span>
  ) : (
    <span>
      {{
        actorJwt: 'Actor identity',
        tokenPassthrough: 'Token passthrough',
        none: 'No credential declared',
      }[credential.mode] ?? credential.mode}
    </span>
  )
}
export function consumersText(impact: KagentImpact) {
  if (impact.consumers === 'noneSeen') return 'No Agent consumers seen in what Radar can read'
  if (impact.consumers === 'none') return 'No Agent uses it — may be staged'
  return `${impact.lowerBound ? '≥' : ''}${impact.count} ${impact.count === 1 && !impact.lowerBound ? 'Agent' : 'Agents'}`
}
export function CoverageNotice({ coverage }: { coverage: Record<string, WorkspaceKindCoverage> }) {
  const gaps = Object.entries(coverage).filter(([, c]) => INCOMPLETE_COVERAGE_STATES.has(c.state))
  if (!gaps.length) return null
  const resources: Record<string, string> = {
    agents: 'agents',
    agentTemplates: 'agenttemplates',
    harnesses: 'harnesses',
    models: 'modelconfigs',
    mcpServers: 'remotemcpservers',
    egressPolicies: 'egresspolicies',
    modelProviders: 'modelproviderconfigs',
    sandboxTemplates: 'sandboxtemplates',
  }
  return (
    <Notice>
      <p>Radar could not read every kagent object. Counts are lower bounds in what Radar can read.</p>
      <ul className="mt-1 space-y-1">
        {gaps.map(([key, c]) => (
          <li key={key}>
            {resources[key]}: {coverageLabel(c)}
            {c.state === 'denied' && (
              <>
                . Needs{' '}
                <GrantText
                  grant={{
                    verb: 'list',
                    resource: resources[key],
                    group: 'api.kagent.dev',
                    ...(c.deniedNamespaces?.length === 1 ? { namespace: c.deniedNamespaces[0] } : {}),
                  }}
                />
              </>
            )}
          </li>
        ))}
      </ul>
    </Notice>
  )
}
export function findingProblem(finding: KagentFinding, readingsByRef?: ReadonlyMap<string, KagentReading>): WorkspaceProblem {
  const reading = readingsByRef?.get(refKey(finding.subject))
  const affected = finding.affects
  const impact = affected?.count === 0 ? ` ${consumersText(affected)}.` : ''
  const readings: Record<string, string> = {
    KagentToolDiscoveryFailed: "kagent's controller can't discover this server's tools; sessions may run without them.",
    KagentAPIKeySecretMissing: 'kagent cannot resolve the API key Secret or key this model names.',
    KagentModelConfigRejected: "kagent did not accept this model's configuration.",
    KagentModelConfigReferenceMissing: 'kagent could not resolve a Secret or ConfigMap this model names.',
    KagentNoPreparedRevision: 'No revision has been prepared, so no session can start.',
    KagentChangeBlocked: 'kagent could not prepare the latest change. New sessions still use the last prepared revision.',
    KagentChangeFailed: 'kagent failed to prepare the latest change. New sessions still use the last prepared revision.',
    KagentEgressNotApplied: 'The declared egress grants were not applied. Sessions may run without them.',
  }
  const detail =
    readings[finding.reason] ??
    (finding.reason.startsWith('KagentDependency') ? finding.message : 'kagent reported a problem with this configuration.')
  return {
    id: finding.id,
    category: finding.reason,
    source: 'reading',
    tone: finding.severity === 'critical' ? 'unhealthy' : 'alert',
    subject: finding.subject,
    title:
      finding.reason === 'KagentDependencyMissing'
        ? 'Declared dependency missing'
        : finding.reason === 'KagentDependencyNotPermitted'
          ? 'Reference not permitted'
          : finding.kagentReason === 'APIKeySecretKeyNotFound'
            ? 'API key Secret key missing'
            : (issueReasonTitle(finding.reason) ?? finding.subject.kind + ' needs attention'),
    detail: detail + impact,
    nextStep: finding.action,
    rawDetail: finding.reason.startsWith('KagentDependency') ? undefined : (reading?.message ?? finding.message),
    rawDetailLabel: "kagent's message",
  }
}
export function KagentGate({
  support,
  checking = true,
  loading,
  error,
  installed,
  children,
}: {
  support: string
  checking?: boolean
  loading: boolean
  error?: unknown
  installed?: boolean
  children: ReactNode
}) {
  const navigate = useWorkspaceNavigate()
  if ((support === 'unknown' && checking) || (support === 'supported' && loading))
    return <PaneLoader label="Reading kagent configuration…" className="flex-1" />
  if (support !== 'supported')
    return (
      <ScreenEmptyState
        icon={Bot}
        title={support === 'unknown' ? 'Could not check kagent workspace support' : 'kagent workspace needs a newer Radar'}
        detail="This Radar keeps the existing Agents list and drawer."
        action={
          <button type="button" onClick={() => navigate(KAGENT_GENERIC_AGENTS_PATH)} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            Open Agents
          </button>
        }
      />
    )
  if (error)
    return <ScreenEmptyState icon={Bot} title="Could not read kagent" detail={error instanceof Error ? error.message : 'Try refreshing.'} />
  if (installed === false)
    return <ScreenEmptyState icon={Bot} title="kagent is not installed" detail="This cluster serves no api.kagent.dev resources." />
  return <>{children}</>
}
export function useKagentRuntime(namespaces: string[], enabled = true) {
  const feature = useRadarFeature('kagentSessionSummary')
  const status = useSubstrateStatus(enabled && feature.support === 'supported')
  const summary = useKagentSessionSummary(namespaces, enabled && status.data?.apiTier.state === 'connected')
  useEffect(() => {
    if (
      enabled &&
      status.data?.apiTier.state === 'connected' &&
      summary.hasNextPage &&
      !summary.isFetchingNextPage &&
      !summary.isFetchNextPageError
    )
      void summary.fetchNextPage()
  }, [
    enabled,
    status.data?.apiTier.state,
    summary.hasNextPage,
    summary.isFetchingNextPage,
    summary.isFetchNextPageError,
    summary.data?.pages.length,
  ])
  const currentTier = status.data?.apiTier
  const pages = currentTier && currentTier.state !== 'connected' ? undefined : summary.data?.pages
  const rows = [...new Map((pages?.flatMap((p) => p.rows) ?? []).map((row) => [`${row.agent.namespace}/${row.agent.name}`, row])).values()]
  const tier = currentTier && currentTier.state !== 'connected' ? currentTier : (pages?.[0].apiTier ?? currentTier)
  const unavailable =
    feature.support === 'unsupported'
      ? 'Sessions need a newer Radar in this cluster.'
      : feature.support === 'unknown'
        ? feature.upgrade
          ? 'Session support could not be checked.'
          : 'Checking session support…'
        : tier && tier.state !== 'connected'
          ? (() => {
              const r = tierReading(tier)
              return `${r.title}${r.detail ? `: ${r.detail}` : ''}`
            })()
          : summary.error || status.error
            ? sessionsFailure(summary.error ?? status.error, {
                namespace: namespaces.join(', ') || 'this scope',
                name: '',
              })
            : 'Reading sessions from Substrate…'
  return {
    query: summary,
    rows,
    tier,
    unavailable,
    completePage: !!pages && !summary.hasNextPage && !summary.isFetchNextPageError,
    lowerBound: !pages || pages.length > 1 || pages.some((p) => p.lowerBound),
  }
}
export type KagentRuntime = ReturnType<typeof useKagentRuntime>
export function SessionValue({ row, runtime }: { row?: KagentSessionRow; runtime: KagentRuntime }) {
  if (!row)
    return (
      <span className="text-xs text-theme-text-tertiary">
        {runtime.completePage ? 'No summary for this Agent in what Radar could read.' : runtime.unavailable}
      </span>
    )
  return (
    <>
      <KagentCountValue count={{ n: row.sessions.total, lowerBound: !row.complete }} />{' '}
      {row.sessions.total === 1 && row.complete ? 'session' : 'sessions'}
      <Sub>
        <KagentCountValue count={{ n: row.crashed, lowerBound: !row.complete }} /> crashed
      </Sub>
    </>
  )
}

export function KagentRuntimeRead({ runtime }: { runtime: KagentRuntime }) {
  const tier = runtime.tier
  if (!tier?.readAt) return null
  return (
    <div className="space-y-1 text-xs text-theme-text-tertiary">
      <FactValue
        fact={{
          text: 'Substrate snapshot',
          tone: tier.lastError ? 'degraded' : 'neutral',
          source: 'Substrate API',
          at: tier.readAt,
          detail: tier.readStartedAt ? `Read from ${tier.readStartedAt} to ${tier.readAt}` : undefined,
        }}
      />
      {tier.refreshing && !tier.lastError && <span> · refreshing…</span>}
      {tier.lastError && <p>Last Substrate read failed: {tier.lastError.message}. Showing the previous snapshot.</p>}
    </div>
  )
}
