// kagent (api.kagent.dev). Mirrors pkg/kagent, whose readings Radar's Issues
// and AI summaries use; pkg/kagent/testdata/status.json pins both. The UI
// shows the reading; the Issues it raises are the server's.

import type { HealthLevel, StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'
import { isApiGroup } from './resource-utils-cnpg'

export const KAGENT_GROUP = 'api.kagent.dev'

export type KagentState =
  | 'deleting' | 'notReported' | 'stale' | 'ready' | 'preparing' | 'blocked' | 'failed' | 'notReady' | 'unknown' | 'discoveryOff'

export interface KagentEgress {
  reason: string
  message?: string
  summary: string
}

/**
 * How one kagent object reads. `text` and `tone` are the badge: on a Ready
 * object whose egress grants are not applied they say so, while `state` and
 * `summary` keep the readiness verdict.
 */
export interface KagentReading {
  state: KagentState
  tone: HealthLevel
  text: string
  summary: string
  condition?: string
  reason?: string
  message?: string
  egress?: KagentEgress
  /** Set when the status was written for an earlier generation: what it said then. */
  previous?: KagentReading
}

export function isKagentResource(resource: any): boolean {
  return isApiGroup(resource?.apiVersion, KAGENT_GROUP)
}

export function isKagentKind(resource: any, kind: string): boolean {
  return resource?.kind === kind && isKagentResource(resource)
}

export function isKagentAgent(resource: any): boolean {
  return isKagentKind(resource, 'Agent')
}

interface Condition {
  status?: string
  reason: string
  message: string
  observedGeneration?: number
  found: boolean
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function num(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function findCondition(resource: any, type: string): Condition & { since?: number } {
  const conditions: any[] = Array.isArray(resource?.status?.conditions) ? resource.status.conditions : []
  const c = conditions.find((x: any) => x?.type === type)
  if (!c) return { reason: '', message: '', found: false }
  const since = Date.parse(str(c.lastTransitionTime))
  return {
    status: str(c.status) || undefined,
    reason: str(c.reason),
    // Mirrors pkg/kagent findCondition: kagent quotes raw errors, URLs included.
    message: redactKagentURLsInText(str(c.message).trim()),
    observedGeneration: num(c.observedGeneration),
    since: Number.isFinite(since) ? since : undefined,
    found: true,
  }
}

function staleGeneration(resource: any, c: Condition): number | undefined {
  const statusGen = num(resource?.status?.observedGeneration)
  const observed = statusGen && statusGen > 0 ? statusGen : c.observedGeneration
  const generation = num(resource?.metadata?.generation)
  return generation && observed && observed > 0 && observed < generation ? observed : undefined
}

/** The 12-hex prefix kagent uses in names and labels. */
export function shortRevision(revision: unknown): string {
  const s = str(revision)
  return s.length > 12 ? s.slice(0, 12) : s
}

const DELETING: KagentReading = { state: 'deleting', tone: 'degraded', text: 'Deleting', summary: 'Deleting' }
const NOT_REPORTED: KagentReading = { state: 'notReported', tone: 'unknown', text: 'Not reported', summary: "kagent hasn't reported status yet" }

function staleOf(previous: KagentReading, observed: number): KagentReading {
  return {
    state: 'stale', tone: 'unknown', text: 'Updating',
    summary: 'Status not yet updated for the latest change',
    message: `Last reported for generation ${observed}: ${previous.summary}`,
    egress: previous.egress,
    previous,
  }
}

function readEgress(resource: any): KagentEgress | undefined {
  const c = findCondition(resource, 'EgressPoliciesApplied')
  if (!c.found || c.status !== 'False') return undefined
  return { reason: c.reason || 'EgressNotApplied', message: c.message || undefined, summary: c.message ? `Egress not applied: ${c.message}` : 'Egress not applied' }
}

function withEgress(r: KagentReading, egress: KagentEgress | undefined): KagentReading {
  if (!egress) return r
  return r.tone === 'healthy' ? { ...r, egress, text: 'Egress not applied', tone: 'alert' } : { ...r, egress }
}

function fromCondition(condition: string, c: Condition) {
  return { condition, reason: c.reason || undefined, message: c.message || undefined }
}

const PREPARING_REASONS = new Set(['ActorTemplatePending', 'ActorTemplateRetrying'])
const PREPARATION_FAILED_REASONS = new Set(['ActorTemplateRejected', 'ActorTemplateConflict', 'RuntimePreparationFailed'])

// Mirrors pkg/kagent preparing: Ready's transition time survives a reason
// change, so a pending build claims no duration; a retrying one (a golden
// build failed) reads orange at once.
function preparing(ready: Condition & { since?: number }, summary: string): KagentReading {
  return { state: 'preparing', tone: ready.reason === 'ActorTemplateRetrying' ? 'alert' : 'degraded', text: 'Preparing', summary, ...fromCondition('Ready', ready) }
}

function notReady(c: Condition): KagentReading {
  return { state: 'notReady', tone: 'alert', text: c.reason || 'Not ready', summary: c.message ? `Not ready: ${c.message}` : 'Not ready', ...fromCondition('Ready', c) }
}

// ============================================================================
// AGENT
// ============================================================================

export interface KagentAgentRevisions {
  desired?: string
  latestSuccessful?: string
  workloadImage?: string
}

/** New sessions pin latestSuccessful, so a change kagent can't prepare leaves them on the last one it did. */
export function getKagentAgentRevisions(agent: any): KagentAgentRevisions {
  return {
    desired: str(agent?.status?.desiredRevision) || undefined,
    latestSuccessful: str(agent?.status?.latestSuccessfulRevision) || undefined,
    workloadImage: str(agent?.status?.workloadImage) || undefined,
  }
}

function agentSessions(rev: KagentAgentRevisions, otherwise: string): string {
  if (rev.latestSuccessful) return `; new sessions still start on revision ${shortRevision(rev.latestSuccessful)}`
  return `; no session can start${otherwise ? ` ${otherwise}` : ''}`
}

function readAgentReady(agent: any, ready: Condition & { since?: number }): KagentReading {
  const rev = getKagentAgentRevisions(agent)
  if (ready.status === 'True') return { state: 'ready', tone: 'healthy', text: 'Ready', summary: 'Ready to start sessions', ...fromCondition('Ready', ready) }
  if (ready.status !== 'False') return { state: 'unknown', tone: 'unknown', text: 'Unknown', summary: "kagent can't tell whether the agent is ready", ...fromCondition('Ready', ready) }
  const refs = findCondition(agent, 'ResolvedRefs')
  if (refs.status === 'False') {
    const summary = `Latest change blocked${refs.message ? `: ${refs.message}` : ''}${agentSessions(rev, '')}`
    return { state: 'blocked', tone: rev.latestSuccessful ? 'alert' : 'unhealthy', text: 'Blocked', summary, ...fromCondition('ResolvedRefs', refs) }
  }
  const compatible = findCondition(agent, 'Compatible')
  if (compatible.status === 'False') return agentFailed('Compatible', compatible, rev)
  if (PREPARING_REASONS.has(ready.reason)) {
    const what = rev.desired ? `revision ${shortRevision(rev.desired)}` : 'the next revision'
    const lead = ready.reason === 'ActorTemplateRetrying' ? 'Retrying a failed build of ' : 'Preparing '
    return preparing(ready, `${lead}${what}${agentSessions(rev, 'until it is ready')}`)
  }
  if (PREPARATION_FAILED_REASONS.has(ready.reason)) return agentFailed('Ready', ready, rev)
  return notReady(ready)
}

// Mirrors pkg/kagent agentFailed: a prior prepared revision keeps new sessions starting.
function agentFailed(condition: string, c: Condition, rev: KagentAgentRevisions): KagentReading {
  const summary = `${c.message ? `Failed: ${c.message}` : 'Failed'}${agentSessions(rev, '')}`
  return { state: 'failed', tone: rev.latestSuccessful ? 'alert' : 'unhealthy', text: 'Failed', summary, ...fromCondition(condition, c) }
}

// `_now` is kept for callers: no reading depends on the clock today.
export function readKagentAgent(agent: any, _now = Date.now()): KagentReading {
  if (agent?.metadata?.deletionTimestamp) return DELETING
  const ready = findCondition(agent, 'Ready')
  if (!ready.found) return NOT_REPORTED
  const current = withEgress(readAgentReady(agent, ready), readEgress(agent))
  const observed = staleGeneration(agent, ready)
  return observed !== undefined ? staleOf(current, observed) : current
}

// ============================================================================
// SANDBOX TEMPLATE
// ============================================================================

export function readKagentSandboxTemplate(template: any, _now = Date.now()): KagentReading {
  if (template?.metadata?.deletionTimestamp) return DELETING
  const ready = findCondition(template, 'Ready')
  if (!ready.found) return NOT_REPORTED
  let current: KagentReading
  if (ready.status === 'True') current = { state: 'ready', tone: 'healthy', text: 'Ready', summary: 'Ready to start sandboxes', ...fromCondition('Ready', ready) }
  else if (ready.status !== 'False') current = { state: 'unknown', tone: 'unknown', text: 'Unknown', summary: "kagent can't tell whether the template is ready", ...fromCondition('Ready', ready) }
  else if (ready.reason === 'Preparing' || PREPARING_REASONS.has(ready.reason)) {
    current = preparing(ready, ready.reason === 'ActorTemplateRetrying' ? 'Retrying a failed build of the sandbox runtime' : 'Preparing the sandbox runtime')
  } else if (ready.reason === 'WorkerPoolNotFound') {
    const pool = str(template?.spec?.substrate?.workerPoolRef?.name)
    const summary = pool ? `Blocked: WorkerPool ${template?.metadata?.namespace ?? ''}/${pool} not found` : `Blocked: ${ready.message}`
    current = { state: 'blocked', tone: 'unhealthy', text: 'Blocked', summary, ...fromCondition('Ready', ready) }
  } else if (ready.reason === 'PreparationFailed') {
    current = { state: 'failed', tone: 'unhealthy', text: 'Failed', summary: ready.message ? `Failed: ${ready.message}` : 'Failed', ...fromCondition('Ready', ready) }
  } else current = notReady(ready)
  current = withEgress(current, readEgress(template))
  const observed = staleGeneration(template, ready)
  return observed !== undefined ? staleOf(current, observed) : current
}

// ============================================================================
// MODEL CONFIG, MODEL PROVIDER CONFIG, REMOTE MCP SERVER
// ============================================================================

function configProblem(state: KagentState, text: string, lead: string, condition: string, c: Condition): KagentReading {
  return { state, tone: 'alert', text, summary: c.message ? `${lead}: ${c.message}` : lead, ...fromCondition(condition, c) }
}

export function readKagentModelConfig(config: any): KagentReading {
  if (config?.metadata?.deletionTimestamp) return DELETING
  const accepted = findCondition(config, 'Accepted')
  const refs = findCondition(config, 'ResolvedRefs')
  if (!accepted.found && !refs.found) return NOT_REPORTED
  let current: KagentReading
  if (accepted.status === 'False') current = configProblem('failed', 'Rejected', 'Not accepted', 'Accepted', accepted)
  else if (refs.status === 'False') current = configProblem('blocked', 'Blocked', 'References not resolved', 'ResolvedRefs', refs)
  else if (accepted.status === 'True' && refs.status === 'True') current = { state: 'ready', tone: 'healthy', text: 'Accepted', summary: 'Accepted; its Secrets and ConfigMaps resolve', ...fromCondition('ResolvedRefs', refs) }
  else current = { state: 'unknown', tone: 'unknown', text: 'Unknown', summary: "kagent can't tell whether the ModelConfig is usable", ...fromCondition('Accepted', accepted) }
  const observed = staleGeneration(config, refs.found ? refs : accepted)
  return observed !== undefined ? staleOf(current, observed) : current
}

export function readKagentModelProviderConfig(config: any): KagentReading {
  if (config?.metadata?.deletionTimestamp) return DELETING
  const ready = findCondition(config, 'Ready')
  // No kagent 1.0 controller reconciles the kind, so an empty status is not a problem.
  if (!ready.found) return { state: 'notReported', tone: 'neutral', text: 'No status', summary: "kagent doesn't report status for this kind" }
  let current: KagentReading
  if (ready.status === 'True') current = { state: 'ready', tone: 'healthy', text: 'Ready', summary: 'Ready for model discovery', ...fromCondition('Ready', ready) }
  else if (ready.status === 'False') current = configProblem('notReady', ready.reason || 'Not ready', 'Not ready', 'Ready', ready)
  else current = { state: 'unknown', tone: 'unknown', text: 'Unknown', summary: "kagent can't tell whether the provider is ready", ...fromCondition('Ready', ready) }
  const observed = staleGeneration(config, ready)
  return observed !== undefined ? staleOf(current, observed) : current
}

/** The tool names kagent's discovery published in status. */
export function getKagentDiscoveredTools(server: any): string[] {
  const tools: any[] = Array.isArray(server?.status?.discoveredTools) ? server.status.discoveredTools : []
  return tools.map((t: any) => str(t?.name)).filter(Boolean)
}

export function readKagentRemoteMCPServer(server: any): KagentReading {
  if (server?.metadata?.deletionTimestamp) return DELETING
  const accepted = findCondition(server, 'Accepted')
  if (!accepted.found) return NOT_REPORTED
  let current: KagentReading
  if (accepted.status === 'True' && accepted.reason === 'DiscoveryDisabled') {
    current = { state: 'discoveryOff', tone: 'neutral', text: 'Discovery off', summary: accepted.message || 'Tools are discovered at run time', ...fromCondition('Accepted', accepted) }
  } else if (accepted.status === 'True') {
    const n = getKagentDiscoveredTools(server).length
    current = { state: 'ready', tone: 'healthy', text: 'Ready', summary: `Discovered ${n === 1 ? '1 tool' : `${n} tools`}`, ...fromCondition('Accepted', accepted) }
  } else if (accepted.status === 'False' && (accepted.reason === 'DiscoveryFailed' || accepted.reason === 'InvalidDiscovery')) {
    current = configProblem('failed', 'Discovery failed', "kagent's controller could not discover its tools", 'Accepted', accepted)
  } else if (accepted.status === 'False') {
    current = configProblem('notReady', accepted.reason || 'Not accepted', 'Not accepted', 'Accepted', accepted)
  } else current = { state: 'unknown', tone: 'unknown', text: 'Unknown', summary: "kagent can't tell whether discovery works", ...fromCondition('Accepted', accepted) }
  const observed = staleGeneration(server, accepted)
  return observed !== undefined ? staleOf(current, observed) : current
}

/** The reading of a kagent kind that reports status; null for the spec-only kinds and anything outside api.kagent.dev. */
export function readKagent(resource: any, now = Date.now()): KagentReading | null {
  if (!isKagentResource(resource)) return null
  switch (resource.kind) {
    case 'Agent': return readKagentAgent(resource, now)
    case 'SandboxTemplate': return readKagentSandboxTemplate(resource, now)
    case 'ModelConfig': return readKagentModelConfig(resource)
    case 'ModelProviderConfig': return readKagentModelProviderConfig(resource)
    case 'RemoteMCPServer': return readKagentRemoteMCPServer(resource)
  }
  return null
}

export function kagentBadge(reading: KagentReading): StatusBadge {
  return { text: reading.text, color: healthColors[reading.tone], level: reading.tone }
}

/** The badge of a kagent kind that reports status, with its summary when it isn't healthy; null otherwise. */
export function getKagentStatus(resource: any, now = Date.now()): (StatusBadge & { message?: string }) | null {
  const reading = readKagent(resource, now)
  if (!reading) return null
  const badge = kagentBadge(reading)
  // A Ready object's badge reads its egress grants, so its message does too.
  const message = reading.state === 'ready' && reading.egress ? reading.egress.summary : reading.summary
  return reading.tone === 'healthy' ? badge : { ...badge, message }
}

// ============================================================================
// DECLARED REFERENCES
// ============================================================================

export interface KagentRef {
  kind: string
  group: string
  namespace: string
  name: string
}

/**
 * A model or MCP server reference, defaulting to kagent's kind in the owner's
 * namespace. 1.0.x wrote MCP server references with apiGroup and a required
 * kind; later releases write group. A reference to another group's kind is
 * kept with that group, so nothing links it to a kagent object.
 */
export function kagentReference(ref: any, defaultKind: string, ownerNamespace: string): KagentRef | undefined {
  const name = str(ref?.name)
  if (!name) return undefined
  return {
    kind: str(ref?.kind) || defaultKind,
    group: str(ref?.group) || str(ref?.apiGroup) || KAGENT_GROUP,
    namespace: str(ref?.namespace) || ownerNamespace,
    name,
  }
}

/** A reference's key for a host's map of reads: namespace/name. */
export function kagentRefKey(ref: KagentRef): string {
  return `${ref.namespace}/${ref.name}`
}

/** Whether a model reference names a kagent ModelConfig, the only kind Radar reads for it. */
export function isKagentModelRef(ref: KagentRef | undefined): boolean {
  return !!ref && ref.group === KAGENT_GROUP && ref.kind === 'ModelConfig'
}

/** Whether a tool binding's server reference names a kagent RemoteMCPServer, the only kind Radar reads for it. */
export function isKagentServerRef(ref: KagentRef | undefined): boolean {
  return !!ref && ref.group === KAGENT_GROUP && ref.kind === 'RemoteMCPServer'
}

export interface KagentToolBinding {
  server?: KagentRef
  /** The selected tools; empty exposes every tool the server has. */
  tools: string[]
  requireApproval: boolean
  subAgent?: { name: string; template: string }
}

export interface KagentTemplateDeclarations {
  model?: KagentRef
  tools: KagentToolBinding[]
  skills: string[]
  plugins: number
}

/** What an AgentTemplate spec (an Agent's inline template, or an AgentTemplate's spec) declares. */
export function getKagentTemplateDeclarations(spec: any, namespace: string): KagentTemplateDeclarations {
  const tools: any[] = Array.isArray(spec?.tools) ? spec.tools : []
  return {
    model: kagentReference(spec?.modelConfig, 'ModelConfig', namespace),
    tools: tools.map((t: any): KagentToolBinding => {
      if (t?.subAgent) return { tools: [], requireApproval: false, subAgent: { name: str(t.subAgent.name), template: str(t.subAgent.templateRef?.name) } }
      const selected = Array.isArray(t?.mcp?.tools) ? t.mcp.tools.filter((x: unknown) => typeof x === 'string' && x) : []
      return { server: kagentReference(t?.mcp?.server, 'RemoteMCPServer', namespace), tools: selected, requireApproval: t?.mcp?.requireApproval === true }
    }),
    skills: (Array.isArray(spec?.skills) ? spec.skills : []).map((s: any) => str(s?.name)).filter(Boolean),
    plugins: Array.isArray(spec?.plugins) ? spec.plugins.length : 0,
  }
}

export type KagentHarnessType = 'kagent' | 'codex' | 'claude' | 'byo'

export interface KagentHarnessDeclarations {
  type?: KagentHarnessType
  workerPool?: string
  image?: string
  summarizerModel?: KagentRef
  memoryModel?: KagentRef
  env: string[]
}

export function getKagentHarnessDeclarations(harness: any): KagentHarnessDeclarations {
  const spec = harness?.spec ?? {}
  const namespace = str(harness?.metadata?.namespace)
  const type = (['kagent', 'codex', 'claude', 'byo'] as const).find(t => spec[t] && typeof spec[t] === 'object')
  return {
    type,
    workerPool: str(spec?.substrate?.workerPoolRef?.name) || undefined,
    image: str(spec?.workload?.image) || undefined,
    summarizerModel: kagentReference(spec?.kagent?.compaction?.summarizer?.modelConfigRef, 'ModelConfig', namespace),
    memoryModel: kagentReference(spec?.kagent?.memory?.modelConfigRef, 'ModelConfig', namespace),
    env: (Array.isArray(spec?.env) ? spec.env : []).map((e: any) => str(e?.name)).filter(Boolean),
  }
}

export function getKagentAgentTemplateRef(agent: any): string | undefined {
  return str(agent?.spec?.template?.ref?.name) || undefined
}

export function getKagentAgentHarness(agent: any): string | undefined {
  return str(agent?.spec?.harnessRef?.name) || undefined
}

export function getKagentAgentEgressPolicies(agent: any): string[] {
  const refs: any[] = Array.isArray(agent?.spec?.egressPolicyRefs) ? agent.spec.egressPolicyRefs : []
  return refs.map((r: any) => str(r?.name)).filter(Boolean)
}

/** Shown in place of a literal value that may be a credential. */
export const KAGENT_LITERAL_HIDDEN = 'Set inline (not shown)'

const REDACTED = '[REDACTED]'

/** Mirrors pkg/kagent StripURLCredentials: blanks a URL's user info, query and fragment; testdata/redaction.json pins both. */
export function stripKagentURLCredentials(raw: string): string {
  const s = raw.trim()
  if (!s) return raw
  let scheme = ''
  let rest = s
  const sep = s.indexOf('://')
  if (sep > 0) { scheme = s.slice(0, sep + 3); rest = s.slice(sep + 3) }
  const end = rest.search(/[/?#]/)
  let authority = end >= 0 ? rest.slice(0, end) : rest
  let tail = end >= 0 ? rest.slice(end) : ''
  const at = authority.lastIndexOf('@')
  if (at >= 0) authority = `${REDACTED}@${authority.slice(at + 1)}`
  const q = tail.search(/[?#]/)
  if (q >= 0) tail = tail.slice(0, q + 1) + REDACTED
  return scheme + authority + tail
}

const URL_IN_TEXT = /\b[a-z][a-z0-9+.-]*:\/\/[^\s"'<>]+/gi
const SENSITIVE_QUERY = /(?:^|[?&#;])[^=&#]*(?:key|token|sig|secret|passw|auth|credential|session)[^=&#]*=/i
const SENSITIVE_FLAG = /^(--?[a-z0-9_.-]*(?:token|key|secret|passw|credential|auth)[a-z0-9_.-]*)(=.*)?$/i

/** Mirrors pkg/kagent RedactURLsInText: user info of every URL in text, and a query that names a credential. */
export function redactKagentURLsInText(text: string): string {
  if (!text.includes('://')) return text
  return text.replace(URL_IN_TEXT, (match) => {
    const u = match.replace(/[.,;:!?)\]}]+$/, '')
    const trailing = match.slice(u.length)
    let stripped = stripKagentURLCredentials(u)
    const q = u.search(/[?#]/)
    if (q >= 0 && !SENSITIVE_QUERY.test(u.slice(q))) {
      const sq = stripped.search(/[?#]/)
      if (sq >= 0) stripped = stripped.slice(0, sq) + u.slice(q)
    }
    return stripped + trailing
  })
}

const ASSIGNMENT = /^([a-z_][a-z0-9_]*)=.+$/i
const CREDENTIAL_WORD = /token|key|secret|passw|credential|auth/i

const SHELL_COMMAND_FLAG = /^-[a-z]*c$/

/** A credential flag's or assignment's whole value, redacted; `next` marks a flag whose value is the following word. Null for any other argument. */
function redactCredentialArg(a: string): { red: string; next: boolean } | null {
  const flag = SENSITIVE_FLAG.exec(a)
  if (flag) return flag[2] ? { red: `${flag[1]}=${REDACTED}`, next: false } : { red: a, next: true }
  const assign = ASSIGNMENT.exec(a)
  if (assign && CREDENTIAL_WORD.test(assign[1])) return { red: `${assign[1]}=${REDACTED}`, next: false }
  return null
}

/** Mirrors pkg/kagent shellWords: POSIX-ish words with quotes and escapes; an unclosed quote runs to the end. */
function shellWords(s: string): { start: number; end: number; text: string }[] {
  const out: { start: number; end: number; text: string }[] = []
  const isSpace = (c: string) => c === ' ' || c === '\t' || c === '\n'
  let i = 0
  while (i < s.length) {
    while (i < s.length && isSpace(s[i])) i++
    if (i >= s.length) break
    const start = i
    let text = ''
    let quote = ''
    while (i < s.length) {
      const c = s[i]
      if (quote && c === quote) quote = ''
      else if (quote === '"' && c === '\\' && i + 1 < s.length) text += s[++i]
      else if (quote) text += c
      else if (c === "'" || c === '"') quote = c
      else if (c === '\\' && i + 1 < s.length) text += s[++i]
      else if (isSpace(c)) break
      else text += c
      i++
    }
    out.push({ start, end: i, text })
  }
  return out
}

function redactShellPayload(payload: string): string {
  let out = ''
  let last = 0
  let blankNext = false
  for (const w of shellWords(payload)) {
    out += payload.slice(last, w.start)
    last = w.end
    const raw = payload.slice(w.start, w.end)
    if (blankNext && !w.text.startsWith('-')) { out += REDACTED; blankNext = false; continue }
    blankNext = false
    const cred = redactCredentialArg(w.text)
    if (cred) { out += cred.next ? raw : cred.red; blankNext = cred.next; continue }
    out += redactKagentURLsInText(raw)
  }
  return out + payload.slice(last)
}

/** Mirrors pkg/kagent RedactArgs: a -c payload is read as shell words; any other credential argument loses its whole value. */
export function redactKagentArgs(args: string[]): string[] {
  let blankNext = false
  return args.map((a, i) => {
    if (blankNext && !a.startsWith('-')) { blankNext = false; return REDACTED }
    blankNext = false
    if (i > 0 && SHELL_COMMAND_FLAG.test(args[i - 1])) return redactShellPayload(a)
    const cred = redactCredentialArg(a)
    if (cred) { blankNext = cred.next; return cred.red }
    if (/[ \t\n]/.test(a)) return redactShellPayload(a)
    return redactKagentURLsInText(a)
  })
}

/** Mirrors pkg/kagent RedactPrinterCell: a URL column loses user info and query; other text the URL rule. */
export function redactKagentPrinterCell(jsonPath: string, value: string): string {
  return ['.spec.url', '.spec.endpoint'].includes(jsonPath.trim()) ? stripKagentURLCredentials(value) : redactKagentURLsInText(value)
}

const URL_FIELDS: Record<string, string[][]> = {
  ModelConfig: [['openAI', 'baseUrl'], ['anthropic', 'baseUrl'], ['mistral', 'baseUrl'], ['gemini', 'baseUrl'], ['azureOpenAI', 'azureEndpoint'], ['ollama', 'host'], ['sapAICore', 'baseUrl'], ['sapAICore', 'authUrl'], ['foundry', 'endpoint']],
  ModelProviderConfig: [['endpoint']],
  RemoteMCPServer: [['url']],
  Harness: [['substrate', 'snapshotPolicy', 'location']],
  SandboxTemplate: [['substrate', 'snapshotPolicy', 'location']],
}

function urlsInNode(node: any): any {
  if (typeof node === 'string') return redactKagentURLsInText(node)
  if (Array.isArray(node)) return node.map(urlsInNode)
  if (node && typeof node === 'object') return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, urlsInNode(v)]))
  return node
}

function stripAt(node: any, path: string[]) {
  for (const key of path.slice(0, -1)) {
    node = node?.[key]
    if (!node || typeof node !== 'object') return
  }
  const last = path[path.length - 1]
  if (typeof node[last] === 'string' && node[last]) node[last] = stripKagentURLCredentials(node[last])
}

function stripArtifactURLs(templateSpec: any) {
  for (const field of ['skills', 'plugins']) {
    for (const item of Array.isArray(templateSpec?.[field]) ? templateSpec[field] : []) {
      stripAt(item?.source, ['git', 'url'])
      stripAt(item?.source, ['bucket', 's3', 'endpoint'])
    }
  }
}

function blankValues(list: unknown) {
  for (const e of Array.isArray(list) ? list : []) {
    if (e && typeof e === 'object' && typeof (e as any).value === 'string' && (e as any).value) (e as any).value = KAGENT_LITERAL_HIDDEN
  }
}

/**
 * A copy without what can carry a credential under any name: literal header
 * and environment values, URL user info and queries, values after a
 * credential-named flag in a Harness's command and args, and a ModelConfig's
 * status.secretHash. Mirrors pkg/ai/context redactKagentLiterals;
 * presentation only, the read policy decides who sees the object.
 */
export function redactKagentLiterals<T>(resource: T): T {
  const r: any = resource
  if (!isKagentResource(r)) return resource
  const out: any = { ...r }
  if (r.metadata?.annotations?.['kubectl.kubernetes.io/last-applied-configuration'] !== undefined) {
    const annotations = { ...r.metadata.annotations }
    delete annotations['kubectl.kubernetes.io/last-applied-configuration']
    out.metadata = { ...r.metadata, annotations }
  }
  const spec: any = r.spec && typeof r.spec === 'object' ? urlsInNode(r.spec) : r.spec
  if (r.status && typeof r.status === 'object') {
    out.status = urlsInNode(r.status)
    if (r.kind === 'ModelConfig') delete out.status.secretHash
  }
  if (spec && typeof spec === 'object') {
    for (const path of URL_FIELDS[r.kind] ?? []) stripAt(spec, path)
    switch (r.kind) {
      case 'RemoteMCPServer': blankValues(spec.headersFrom); break
      case 'ModelConfig':
        if (spec.defaultHeaders && typeof spec.defaultHeaders === 'object') {
          for (const [k, v] of Object.entries(spec.defaultHeaders)) if (typeof v === 'string' && v) spec.defaultHeaders[k] = KAGENT_LITERAL_HIDDEN
        }
        if (typeof spec.azureOpenAI?.azureAdToken === 'string' && spec.azureOpenAI.azureAdToken) spec.azureOpenAI.azureAdToken = KAGENT_LITERAL_HIDDEN
        break
      case 'Harness':
        blankValues(spec.env)
        for (const field of ['command', 'args']) {
          const list = spec.workload?.[field]
          if (Array.isArray(list) && list.every((a: unknown) => typeof a === 'string')) spec.workload[field] = redactKagentArgs(list)
        }
        break
      case 'SandboxTemplate': blankValues(spec.env); break
      case 'AgentTemplate': stripArtifactURLs(spec); break
      case 'Agent': stripArtifactURLs(spec.template?.inline); break
    }
    out.spec = spec
  }
  return out
}

// ============================================================================
// AGENT TABLE
// ============================================================================

/** Filter text for the Agent table's columns other than status, matching what the cells show; undefined for any other resource or column. */
export function getKagentAgentColumnValue(resource: any, column: string): string | undefined {
  if (!isKagentAgent(resource)) return undefined
  switch (column) {
    case 'kagentTemplate': return getKagentAgentTemplateRef(resource) ?? (resource?.spec?.template?.inline ? 'Inline' : '-')
    case 'kagentHarness': return getKagentAgentHarness(resource) ?? '-'
    case 'kagentModel': {
      const model = getKagentTemplateDeclarations(resource?.spec?.template?.inline, resource?.metadata?.namespace ?? '').model
      return model ? model.name : getKagentAgentTemplateRef(resource) ? 'From template' : '-'
    }
    case 'kagentRevision': return shortRevision(getKagentAgentRevisions(resource).latestSuccessful) || '-'
  }
  return undefined
}
