import { formatDuration, type BadgeSeverity, type StatusTone } from '@skyhook-io/k8s-ui'
import type {
  SubstrateAPITier,
  SubstrateActorState,
  SubstrateAttention,
  SubstrateCode,
  SubstrateCoverage,
  SubstrateRef,
  SubstrateResumesWith,
  SubstrateSnapshot,
  SubstrateStateCounts,
  SubstrateSubject,
  SubstrateTemplateRow,
  SubstratePoolRow,
  SubstrateK8sPool,
  SubstrateWorkerImpact,
  SubstrateWorkerRow,
} from '../../api/substrateWorkspace'

// Radar's reading of Substrate values for the screens: labels, tones and the
// sentences that explain them. Pure, so the screens and tests share them.

export const ACTOR_STATE_LABEL: Record<SubstrateActorState, string> = {
  running: 'Running',
  suspended: 'Suspended',
  resuming: 'Resuming',
  suspending: 'Suspending',
  reverting: 'Reverting',
  crashed: 'Crashed',
  deleting: 'Deleting',
  unknown: 'Unknown',
}

/** The states an actor passes through on its way between running and suspended. */
export const IN_PROGRESS_STATES: readonly SubstrateActorState[] = ['resuming', 'suspending', 'reverting', 'deleting']

/** Display order, the state bar's and the legend's. */
export const ACTOR_STATE_ORDER: readonly SubstrateActorState[] = ['crashed', 'resuming', 'suspending', 'reverting', 'deleting', 'running', 'suspended', 'unknown']

// Suspended is where most actors rest, so it reads calm, not amber; the
// transitions read as pending, a crash as a failure.
const STATE_TONE: Record<SubstrateActorState, StatusTone> = {
  running: 'healthy',
  suspended: 'neutral',
  resuming: 'degraded',
  suspending: 'degraded',
  reverting: 'degraded',
  deleting: 'degraded',
  crashed: 'unhealthy',
  unknown: 'unknown',
}

export function actorStateTone(state: SubstrateActorState): StatusTone {
  return STATE_TONE[state] ?? 'unknown'
}

const TONE_SEVERITY: Record<StatusTone, BadgeSeverity> = {
  healthy: 'success',
  neutral: 'info',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  unknown: 'neutral',
}

export function toneSeverity(tone: StatusTone): BadgeSeverity {
  return TONE_SEVERITY[tone]
}

/** The Actors screen's segments: one per state an operator acts on, with the transitions together. */
export type ActorSegment = 'all' | 'crashed' | 'inProgress' | 'suspended' | 'running'

export const ACTOR_SEGMENTS: readonly { id: ActorSegment; label: string; states: readonly SubstrateActorState[] }[] = [
  { id: 'all', label: 'All', states: [] },
  { id: 'crashed', label: 'Crashed', states: ['crashed'] },
  { id: 'inProgress', label: 'In progress', states: IN_PROGRESS_STATES },
  { id: 'suspended', label: 'Suspended', states: ['suspended'] },
  { id: 'running', label: 'Running', states: ['running'] },
]

export function segmentCount(counts: SubstrateStateCounts | undefined, segment: ActorSegment): number | undefined {
  if (!counts) return undefined
  if (segment === 'all') return counts.total
  return ACTOR_SEGMENTS.find((s) => s.id === segment)!.states.reduce((n, s) => n + (counts.byState[s] ?? 0), 0)
}

/** The segment a state filter from a link (`?state=`) falls under. */
export function segmentOfState(state: string | null): ActorSegment {
  if (!state) return 'all'
  if (state === 'inProgress') return 'inProgress'
  const seg = ACTOR_SEGMENTS.find((s) => s.id !== 'all' && s.states.includes(state as SubstrateActorState))
  return seg?.id ?? 'all'
}

const FIDELITY_LABEL: Record<NonNullable<SubstrateSnapshot['fidelity']>, string> = {
  memory: 'memory',
  rootfs: 'root filesystem',
  volumes: 'volumes',
}

/** What a snapshot holds, in a word or two. */
export function fidelityLabel(fidelity: SubstrateSnapshot['fidelity']): string {
  return fidelity ? FIDELITY_LABEL[fidelity] : 'unspecified'
}

/** Which generation a resume starts from, and what it gives back: "resumes where it left off", "resumes from gen 6: …". */
export type ResumeCondition = 'compatibleRuntime' | 'runtimeMismatch'

interface ResumeReading {
  newestSnapshot?: SubstrateSnapshot
  resumeGeneration?: number
  resumesWith: SubstrateResumesWith
  resumeCondition?: ResumeCondition
}

export function resumeText(row: ResumeReading): string {
  if (row.resumeGeneration === undefined) return resumesWithText('cold')
  const older = row.newestSnapshot && row.resumeGeneration !== row.newestSnapshot.generation
  const what = row.resumeCondition === 'compatibleRuntime' ? 'resumes where it left off on a compatible runtime' : resumesWithText(row.resumesWith)
  return older ? `resumes from gen ${row.resumeGeneration}: ${what}` : what
}

const RESUME_SENTENCE: Record<SubstrateResumesWith, string> = {
  memory: 'Resumes where it left off: the snapshot holds its memory.',
  filesystem: 'Resumes by restarting the agent on its saved files: the snapshot holds its root filesystem, not its memory.',
  volumes: 'Resumes by restarting the agent with only its saved volumes.',
  cold: 'Boots fresh: there is no snapshot to resume from.',
  unknown: 'What a resume restores is unknown: Substrate names no fidelity for the snapshot.',
}

/**
 * What a resume restores, in a sentence. Memory holds only on a compatible
 * runtime; a known mismatch restarts from the saved files.
 */
export function resumeConditionSentence(resumesWith: SubstrateResumesWith, condition?: ResumeCondition): string {
  if (condition === 'compatibleRuntime') return 'Snapshot holds memory: resumes where it left off on a compatible runtime, otherwise restarts from its saved files.'
  if (condition === 'runtimeMismatch') return 'Restarts from its saved files: no worker of its class can restore its memory.'
  return RESUME_SENTENCE[resumesWith]
}

/** What the next resume does, as a sentence, naming the generation when it is not the newest. */
export function resumeSentence(row: ResumeReading): string {
  if (row.resumeGeneration === undefined) return RESUME_SENTENCE.cold
  const older = row.newestSnapshot && row.resumeGeneration !== row.newestSnapshot.generation
  const sentence = resumeConditionSentence(row.resumesWith, row.resumeCondition)
  return older ? `${sentence} It resumes from generation ${row.resumeGeneration}: the newest is not stored yet.` : sentence
}

/** Why a revert drops a generation newer than the one it keeps. */
export const DISCARD_WHY: Record<string, string> = {
  localOnly: 'only on its node',
  uploadPending: 'its upload may still finish; waiting for it before reverting keeps it',
  uploadAbandoned: 'Substrate will never record its upload',
  incomplete: 'recorded but never captured',
}

/** A snapshot's own resume, by what it holds. */
export function fidelityResume(fidelity: SubstrateSnapshot['fidelity']): string {
  return RESUME_SENTENCE[fidelity === 'memory' ? 'memory' : fidelity === 'rootfs' ? 'filesystem' : fidelity === 'volumes' ? 'volumes' : 'unknown']
}

/**
 * What a state badge leaves to say: "for 36m", "upload pending for 36m", or
 * the crash message. The badge names the state, so the reason's own state
 * word is not repeated.
 */
export function stateExtra(state: SubstrateActorState, reason: string | undefined): string | undefined {
  if (!reason) return undefined
  const label = ACTOR_STATE_LABEL[state]
  if (label && reason.startsWith(`${label} `)) return reason.slice(label.length + 1)
  if (reason === label) return undefined
  return state === 'crashed' ? reason : reason[0].toLowerCase() + reason.slice(1)
}

/** What a resume from the newest snapshot gives back. */
export function resumesWithText(r: SubstrateResumesWith): string {
  switch (r) {
    case 'memory':
      return 'resumes where it left off'
    case 'filesystem':
      return 'restarts the agent on its saved files'
    case 'volumes':
      return 'restarts the agent with its saved volumes only'
    case 'cold':
      return 'boots fresh'
    default:
      return 'Substrate names no fidelity, so what a resume restores is unknown'
  }
}

/** Where a snapshot's files are kept, in a few words. */
export function durableText(s: SubstrateSnapshot): { text: string; tone: StatusTone; title: string } {
  switch (s.durable) {
    case 'completed':
      return { text: 'stored', tone: 'healthy', title: 'Uploaded to object storage: it survives the loss of the node it was taken on.' }
    case 'pending':
      return {
        text: 'upload pending',
        tone: 'degraded',
        title: s.locality
          ? `Still uploading from node ${s.locality}. Until it completes, the state exists only on that node.`
          : 'Still uploading. Until it completes, the state exists only on the node it was taken on.',
      }
    default:
      return {
        text: s.locality ? `only on ${s.locality}` : 'not stored',
        tone: 'degraded',
        title: s.locality
          ? `Substrate recorded no durable copy: the files exist only on node ${s.locality}.`
          : 'Substrate recorded no durable copy, and names no node holding the files.',
      }
  }
}

/** `kubectl ate revert actor`, which returns a crashed actor to suspended at its newest stored snapshot. */
export function revertCommand(ref: SubstrateRef): string {
  return `kubectl ate revert actor ${ref.name} -a ${ref.atespace}`
}

/** `kubectl ate logs actor`, which reads the actor's lines from its current worker. */
export function logsCommand(ref: SubstrateRef): string {
  return `kubectl ate logs actor ${ref.name} -a ${ref.atespace}`
}

/** `kubectl ate get actor-template … -o yaml`: the template's full spec. */
export function templateYamlCommand(ref: SubstrateRef): string {
  return `kubectl ate get actor-template ${ref.name} -a ${ref.atespace} -o yaml`
}

export function refText(ref: SubstrateRef): string {
  return `${ref.atespace} / ${ref.name}`
}

export function attentionTone(a: Pick<SubstrateAttention, 'severity'>): Exclude<StatusTone, 'healthy'> {
  // info: expected and still within its budget, such as a drain before its Pod's deadline.
  return a.severity === 'critical' ? 'unhealthy' : a.severity === 'info' ? 'neutral' : 'degraded'
}

export type AttentionArea = 'actors' | 'templates' | 'workers' | 'pools' | 'nodes'

const RULE_AREA: Record<SubstrateAttention['rule'], AttentionArea> = {
  crashedActors: 'actors',
  stuckTransition: 'actors',
  uploadOverdue: 'actors',
  goldenSnapshotFailed: 'templates',
  noMatchingPool: 'templates',
  missingTemplate: 'templates',
  workerUnavailable: 'workers',
  workerDraining: 'workers',
  workerRestarting: 'workers',
  poolShort: 'pools',
  poolFull: 'pools',
  nodeUploadsOverdue: 'nodes',
}

/** Which screen a needs-attention row belongs to. */
export function attentionArea(a: Pick<SubstrateAttention, 'rule'>): AttentionArea {
  return RULE_AREA[a.rule] ?? 'actors'
}

/** The areas whose rules need Substrate's API, so a zero there is unknown while it is not read. */
export const API_AREAS: ReadonlySet<AttentionArea> = new Set(['actors', 'templates', 'workers'])

/** The atespace/name of a filter value for the /actors `template` parameter. */
export function templateFilterValue(ref: SubstrateRef): string {
  return `${ref.atespace}/${ref.name}`
}

/** A subject's words for a link: "Actor research / lit-review-0193", "the 14 actors". */
export function subjectLabel(s: SubstrateSubject, count?: number): string {
  switch (s.kind) {
    case 'Actors':
      return count !== undefined ? `Show the ${count === 1 ? 'actor' : `${count} actors`}` : 'Show the actors'
    case 'Actor':
    case 'ActorTemplate':
      return `${s.atespace ?? ''} / ${s.name ?? ''}`
    case 'WorkerPool':
    case 'Pod':
    case 'Worker':
      return `${s.namespace ?? ''} / ${s.name ?? ''}`
    default:
      return s.name ?? ''
  }
}

export const SUBJECT_KIND_LABEL: Record<SubstrateSubject['kind'], string> = {
  Actor: 'Actor',
  ActorTemplate: 'Template',
  Worker: 'Worker',
  WorkerPool: 'Pool',
  Node: 'Node',
  Pod: 'Pod',
  Actors: 'Actors',
}

/** Coverage that is not a full read: counts over it are lower bounds, and its empty list proves nothing. */
export function coverageInPart(cov: SubstrateCoverage | undefined): boolean {
  return !cov || cov.state !== 'full'
}

/** A coverage that could not be read at all. */
export function coverageUnread(cov: SubstrateCoverage | undefined): boolean {
  return !cov || cov.state === 'denied' || cov.state === 'failed'
}

/** Why a kind was not read in full, in a clause: "Substrate denied atespaces a, b". */
export function coverageReason(cov: SubstrateCoverage | undefined, noun: string): string {
  if (!cov) return `${noun} were not read`
  if (cov.state === 'partial') {
    if (cov.code === 'capExceeded') return `Radar read the first ${cov.cap ?? cov.count ?? 'few'} ${noun} and stopped there`
    if (cov.code === 'scoped') return `Substrate would not list atespaces, so Radar read only ${cov.readAtespaces?.length ? cov.readAtespaces.join(', ') : 'the atespaces set in the connection'}; others are unknown`
    if (cov.unreadAtespaces?.length) return `Radar ran out of time before reading ${cov.unreadAtespaces.length === 1 ? 'atespace' : 'atespaces'} ${cov.unreadAtespaces.join(', ')}`
    if (cov.deniedAtespaces?.length) return `Substrate denied ${cov.deniedAtespaces.length === 1 ? 'atespace' : 'atespaces'} ${cov.deniedAtespaces.join(', ')}`
    return `some ${noun} could not be read`
  }
  if (cov.state === 'denied') return `Substrate denied reading ${noun}${cov.message ? `: ${cov.message}` : ''}`
  if (cov.state === 'failed') return `${noun} could not be read${cov.message ? `: ${cov.message}` : ''}`
  return ''
}

/** What each API tier state means for the reader, and what to do about it. */
export interface TierReading {
  title: string
  detail: string
  nextStep?: string
  /** A command that checks or does the next step, shown as a copyable chip. */
  command?: string
  tone: StatusTone
}

/** What a Radar Hub user who isn't an org owner reads where Substrate's own values would be. */
export const OWNERS_ONLY_NOTE = 'In Radar Hub, only org owners can see Substrate’s actors, templates and workers for now. Ask an org owner.'

const CODE_READING: Partial<Record<SubstrateCode, Omit<TierReading, 'detail'> & { detail?: string }>> = {
  notConfigured: { title: 'Not connected', tone: 'unknown', nextStep: 'Choose who Radar reads Substrate as.' },
  targetChanged: {
    title: 'Saved for another cluster',
    tone: 'unknown',
    detail: 'The connection was saved while this context pointed at another cluster, so Radar does not use it here.',
    nextStep: 'Choose the identity again for this cluster.',
  },
  sharedRadar: { title: 'Not available in a shared Radar', tone: 'unknown' },
  mintDenied: {
    title: 'Your access can’t mint the shared identity’s token',
    tone: 'unhealthy',
    detail: 'In Radar Hub, Radar reads Substrate as ate-system/ate-client with a token minted with your own Kubernetes access, and that access can’t create one.',
    nextStep: 'Ask a cluster admin for create on serviceaccounts/token for ate-system/ate-client.',
  },
  notInstalled: { title: 'API server not found', tone: 'unknown', nextStep: 'Check that Substrate’s API server runs as Service ate-system/api, or set its endpoint under Advanced.' },
  tls: { title: 'TLS check failed', tone: 'unhealthy', nextStep: 'Check the CA file and server name under Advanced, or that the cluster’s trust bundle for Substrate exists.' },
  unreachable: { title: 'API server unreachable', tone: 'unhealthy', nextStep: 'Check that the API server’s Pods are running, then retry; with a custom endpoint, check it is reachable from this machine.' },
  unauthenticated: { title: 'Substrate rejected the identity', tone: 'unhealthy', nextStep: 'Check that the token is current, or choose another identity.' },
  denied: { title: 'Substrate denied the read', tone: 'unhealthy', nextStep: 'Ask a Substrate owner to grant this identity the viewer role, or choose another identity.' },
  settingsFile: { title: 'Radar couldn’t read its saved connection', tone: 'unhealthy', nextStep: 'Check that ~/.radar/substrate.json is readable, then choose the identity again.' },
  invalidSettings: { title: 'The connection settings are invalid', tone: 'unhealthy', nextStep: 'Check the token file and CA file paths, then save again.' },
  contextChanged: { title: 'The connection changed during the read', tone: 'unknown', nextStep: 'Retry: Radar reads again through the new connection.' },
  failed: { title: 'Reading Substrate failed', tone: 'unhealthy', nextStep: 'Retry; if it keeps failing, read the API server’s log.' },
}

/**
 * The same codes for a Radar Hub owner, who reads as ate-client with nothing
 * to configure: no Advanced fields, no other identity to choose, no "this
 * machine". Steps name what the owner can ask for instead.
 */
const NO_FORM_READING: Partial<Record<SubstrateCode, Omit<TierReading, 'detail'> & { detail?: string; withReason?: boolean }>> = {
  notInstalled: {
    title: 'Substrate isn’t installed here',
    tone: 'unknown',
    detail: 'Substrate’s shared client identity (ate-system/ate-client) doesn’t exist here.',
    nextStep: 'Check that Substrate is installed in ate-system.',
    command: 'kubectl -n ate-system get sa ate-client',
  },
  tls: {
    title: 'Radar couldn’t verify Substrate’s API server',
    tone: 'unhealthy',
    detail: 'Radar couldn’t verify api.ate-system.svc:443 from inside the cluster.',
    withReason: true,
    nextStep: 'Check Substrate’s API Service and its Pods.',
    command: 'kubectl -n ate-system get svc api',
  },
  unreachable: {
    title: 'Radar couldn’t reach Substrate’s API server',
    tone: 'unhealthy',
    detail: 'Radar couldn’t reach api.ate-system.svc:443 from inside the cluster.',
    withReason: true,
    nextStep: 'Check that Substrate’s API server Pods are running.',
    command: 'kubectl -n ate-system get pods',
  },
  unauthenticated: {
    title: 'Substrate refused the shared identity',
    tone: 'unhealthy',
    detail: 'Substrate refused ate-system/ate-client: it isn’t granted in this install.',
    withReason: true,
    nextStep: 'Ask a Substrate operator to grant it; until then, this Radar can’t read Substrate as it stands.',
  },
  denied: {
    title: 'Substrate refused the shared identity',
    tone: 'unhealthy',
    detail: 'Substrate denied ate-system/ate-client the read: it isn’t granted in this install.',
    withReason: true,
    nextStep: 'Ask a Substrate operator to grant it the viewer role; until then, this Radar can’t read Substrate as it stands.',
  },
}

export function tierReading(tier: SubstrateAPITier): TierReading {
  if (tier.state === 'connected') return { title: 'Connected', detail: '', tone: 'healthy' }
  // A product policy, not a fault: the same short note everywhere, whatever the server's sentence.
  if (tier.code === 'ownersOnly') return { title: 'For org owners', detail: OWNERS_ONLY_NOTE, tone: 'unknown' }
  if (tier.state === 'connecting') return { title: 'Connecting', detail: 'The connection is saved; Radar is reading Substrate’s API for the first time.', tone: 'unknown' }
  const noForm = tier.code ? tier.identity?.mode === 'sharedAsCaller' && NO_FORM_READING[tier.code] : undefined
  if (noForm) {
    // The plain state is Radar's; the server's own words follow as its reason.
    const { withReason, ...reading } = noForm
    return { ...reading, detail: withReason && tier.reason ? `${noForm.detail} ${tier.reason}` : noForm.detail ?? tier.reason ?? '' }
  }
  const known = tier.code ? CODE_READING[tier.code] : undefined
  const detail = tier.reason ?? known?.detail ?? 'Radar has not read Substrate’s API.'
  if (known) return { ...known, detail: known.detail && !tier.reason ? known.detail : detail }
  if (tier.state === 'notConfigured') return { title: 'Not connected', tone: 'unknown', detail, nextStep: CODE_READING.notConfigured!.nextStep }
  return { title: 'Not read', tone: 'unknown', detail }
}

/** The shared identity in words: who Substrate sees, and that it is not the user. */
export function sharedIdentityText(principal: string): string {
  return `${principal}, the identity kubectl ate uses when no --token-file is given. It is not you: Substrate shows Radar what that identity may see.`
}

/** Countable bar segments: one per actor while it is small enough to count, then proportional. */
export const COUNTABLE_LIMIT = 80

export interface BarSegment {
  state: SubstrateActorState
  count: number
  /** Share of the bar, 0–1. */
  share: number
}

export function stateBarSegments(counts: SubstrateStateCounts): { segments: BarSegment[]; countable: boolean } {
  const segments = ACTOR_STATE_ORDER.map((state) => ({ state, count: counts.byState[state] ?? 0 }))
    .filter((s) => s.count > 0)
    .map((s) => ({ ...s, share: counts.total > 0 ? s.count / counts.total : 0 }))
  return { segments, countable: counts.total > 0 && counts.total <= COUNTABLE_LIMIT }
}

/** "2,104"; undefined (not read) never renders as 0. */
export function countText(n: number | undefined, unknown = 'unknown'): string {
  return n === undefined ? unknown : n.toLocaleString('en-US')
}

/** Bytes as Kubernetes writes them: 2Gi, 512Mi. */
export function bytesText(bytes: number): string {
  const units = ['', 'Ki', 'Mi', 'Gi', 'Ti']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const rounded = Number.isInteger(v) ? String(v) : v.toFixed(1).replace(/\.0$/, '')
  return `${rounded}${units[i]}`
}

/** Millicores as Kubernetes writes them: 2, 500m. */
export function cpuText(milli: number): string {
  return milli % 1000 === 0 ? String(milli / 1000) : `${milli}m`
}

export interface GoldenReading {
  label: string
  severity: BadgeSeverity
  /** What the state means, in a sentence. */
  meaning: string
}

/**
 * A template's golden snapshot as the reader should take it. A build no pool
 * can run never finishes (Substrate retries it), so it reads as blocked.
 */
export function goldenReading(t: Pick<SubstrateTemplateRow, 'golden' | 'matchingPools' | 'matchingPoolsComplete'>): GoldenReading {
  switch (t.golden.state) {
    case 'ready':
      return { label: 'Ready', severity: 'success', meaning: 'New actors start from the golden snapshot.' }
    case 'failed':
      return { label: 'Failed', severity: 'error', meaning: 'The build failed and Substrate does not retry it: new actors using the template’s default boot cold. Templates can’t be edited: recreate it to build again.' }
    case 'disabled':
      return { label: 'Disabled', severity: 'neutral', meaning: 'The template turns golden snapshots off: every new actor boots cold.' }
  }
  if (t.golden.blocked === 'noMatchingPool') {
    return { label: 'Can’t build: no pool can run it', severity: 'error', meaning: 'The build runs as an actor on a worker, and no pool’s labels match the template’s worker selector, so it retries without end. New actors can’t run either.' }
  }
  return { label: 'Building', severity: 'warning', meaning: 'Substrate is warming the template up; new actors boot cold until it is ready.' }
}

export interface PoolReading {
  label: string
  severity: BadgeSeverity
  meaning: string
  /** Kubernetes' own phase for the WorkerPool ("Scaling"), shown beside as its cause or context. */
  kubernetes?: string
}

/**
 * One reading of a pool, the same in its list and on its page: Substrate's
 * capacity verdict with the count behind it, and Kubernetes' phase as context.
 */
export function poolReading(p: Pick<SubstratePoolRow, 'capacity' | 'placement' | 'kubernetes'>): PoolReading {
  const kubernetes = p.kubernetes?.healthReason || undefined
  const desired = p.kubernetes?.desired
  switch (p.capacity) {
    case 'short': {
      const [have, word] = p.placement ? [p.placement.workers.active, 'active'] : [p.kubernetes ? poolReadyPods(p.kubernetes) : 0, 'ready']
      // A partial read of Substrate's workers counts what it saw: a floor, not the pool's number.
      const atLeast = p.placement && !p.placement.complete ? 'at least ' : ''
      return {
        label: `Short of workers: ${atLeast}${have} of ${desired ?? '?'} ${word}`,
        severity: 'warning',
        meaning: 'Fewer workers take actors than the pool wants. New and resuming actors may wait or fail to start.',
        kubernetes,
      }
    }
    case 'full':
      return { label: 'No free actor slots', severity: 'alert', meaning: 'No active worker has a free actor slot.', kubernetes }
    case 'free':
      return { label: 'Free actor slots', severity: 'success', meaning: 'At least one active worker has a free actor slot. Whether an actor fits depends on its size and its snapshot’s restore constraints.', kubernetes }
  }
  return { label: 'Unknown', severity: 'neutral', meaning: 'Neither Kubernetes nor Substrate was read well enough to say.', kubernetes }
}

/**
 * Why a pool's Substrate read failed, in words. Only notFound proves the pool
 * is gone; incomplete means Radar read too little to tell either way.
 */
export function poolReadFailure(code: SubstrateCode | undefined, message: string | undefined): string {
  if (code === 'notFound') return 'There is no WorkerPool by this name, and Substrate has no workers for one.'
  if (code === 'incomplete') return `Radar can’t tell whether this pool exists: it read too little of the WorkerPools or of Substrate’s workers${message ? ` (${message})` : ''}.`
  return `Radar couldn’t read the pool from Substrate’s API${message ? `: ${message}` : ''}.`
}

/** A worker's memory-limit kill of the actor's sandbox, in words: "the worker killed its sandbox over its memory limit of 2Gi (it used 2Gi)". */
export function memoryKillText(kill: { limitBytes: number; usageBytes?: number }): string {
  return `the worker killed its sandbox over its memory limit of ${bytesText(kill.limitBytes)}${kill.usageBytes ? ` (it used ${bytesText(kill.usageBytes)})` : ''}`
}


/** Local date and time to the second, for a tooltip beside a relative time. */
export function localTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** "12m ago", or "in 12m" for a time ahead; a timestamp Radar can't parse is returned as is. */
export function relativeTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return iso
  const s = Math.round((t - now) / 1000)
  if (s > 60) return `in ${formatDuration(s * 1000)}`
  if (s > -5) return 'just now'
  return `${formatDuration(-s * 1000)} ago`
}

/** A container line newer than this says the actor is still running. */
export const RECENT_OUTPUT_MS = 5 * 60_000

/**
 * Whether the actor's own output says it is still running, for an actor on a
 * worker Substrate no longer trusts: the newest container line read. Undefined
 * when the log was not read.
 */
export function servingEvidence(lines: { kind: string; time?: string }[] | undefined, now = Date.now()): { recent: boolean; at?: string } | undefined {
  if (!lines) return undefined
  let newest: number | undefined
  let at: string | undefined
  for (const l of lines) {
    if (l.kind !== 'container' || !l.time) continue
    const t = Date.parse(l.time)
    if (Number.isFinite(t) && (newest === undefined || t > newest)) {
      newest = t
      at = l.time
    }
  }
  return { recent: newest !== undefined && now - newest < RECENT_OUTPUT_MS, at }
}

/**
 * A reading's first sentence, shown in full, and the rest, which waits for
 * Details: a row never shows a sentence cut off.
 */
export function splitReading(reading: string): [string, string | undefined] {
  const m = /^(.+?[.!?])\s+(?=[A-Z0-9])/.exec(reading)
  if (!m) return [reading, undefined]
  const rest = reading.slice(m[0].length).trim()
  return [m[1], rest || undefined]
}

/**
 * Codes that end a detail read for good: the object is gone, access was taken
 * away, or the connection no longer stands. A page shows the failure, not the
 * last answer it kept; any other refresh failure keeps that answer, marked stale.
 */
const DEFINITIVE_CODES = new Set<SubstrateCode>(['notFound', 'denied', 'unauthenticated', 'notConfigured', 'sharedRadar', 'targetChanged'])

export function definitiveFailure(code: SubstrateCode | undefined): boolean {
  return !!code && DEFINITIVE_CODES.has(code)
}

function sentences(text: string): string[] {
  return text.split(/(?<=[.!?])\s+(?=[A-Z0-9])/).map((x) => x.trim()).filter(Boolean)
}

const norm = (x: string) => x.toLowerCase().replace(/[.!?:;,]+$/g, '').replace(/\s+/g, ' ').trim()

const STOP = new Set(['a', 'an', 'the', 'on', 'of', 'to', 'it', 'its', 'is', 'and', 'or', 'in', 'for', 'only', 'also', 'so'])
const words = (x: string) => norm(x).split(/[^a-z0-9’']+/).filter((w) => w && !STOP.has(w))

/**
 * A reading without the sentences its next step already says, so a banner
 * states each fact once: where the action is. A sentence counts as said when
 * nearly all of its words are in the step, however the step words it.
 */
export function withoutRepeats(reading: string | undefined, nextStep: string | undefined): string | undefined {
  if (!reading || !nextStep) return reading
  const step = new Set(words(nextStep))
  const said = (x: string) => {
    const w = words(x)
    return w.length >= 3 && w.filter((t) => step.has(t)).length / w.length >= 0.85
  }
  const kept = sentences(reading).filter((x) => !said(x))
  return kept.length ? kept.join(' ') : undefined
}

/** The tone of an actor's cell in the placement grid: worker impact and transitions apart, by the badge vocabulary. */
export function placementTone(state: SubstrateActorState, impact: SubstrateWorkerImpact['kind'] | undefined): StatusTone {
  if (impact === 'restarted') return 'unhealthy'
  if (impact) return 'degraded'
  if (state === 'crashed') return 'unhealthy'
  if (state === 'running') return 'healthy'
  if (state === 'suspended') return 'unknown'
  return 'neutral'
}

const WORKER_STATE_LABEL: Record<SubstrateWorkerRow['state'], { label: string; severity: BadgeSeverity }> = {
  active: { label: 'Active', severity: 'success' },
  draining: { label: 'Draining', severity: 'warning' },
  unavailable: { label: 'Unavailable', severity: 'error' },
  unknown: { label: 'Unknown', severity: 'neutral' },
}

/**
 * A worker's chip. A registration ahead of what Substrate reconciled is part
 * of it, with the actors that lost their sandboxes: a green "Active" over lost
 * actors would say the opposite of the actors' own rows.
 */
export function workerStateReading(w: Pick<SubstrateWorkerRow, 'state' | 'workerState'>): { label: string; severity: BadgeSeverity; title: string } {
  const base = WORKER_STATE_LABEL[w.state]
  const ws = w.workerState
  const title = ws?.reading ?? 'Substrate’s state for the worker. Only active workers take new actors.'
  if (ws?.kind !== 'reconciling') return { ...base, title }
  const lost = ws.lost ? ` (${ws.lost} ${ws.lost === 1 ? 'actor' : 'actors'} lost)` : ''
  return { label: `${base.label} · reconciling${lost}`, severity: w.state === 'unavailable' ? 'error' : 'warning', title }
}

/** What Kubernetes says about a worker's Pod, in one phrase; terminating comes first, since a Ready Pod being deleted is not counted ready. */
export function workerPodText(w: Pick<SubstrateWorkerRow, 'podReady' | 'podTerminating'>): string | undefined {
  if (w.podTerminating) return 'Pod terminating'
  if (w.podReady === undefined) return undefined
  return w.podReady ? 'Pod ready' : 'Pod not ready'
}

/** A Substrate reason code Kubernetes-style messages lead with ("GoldenActorCrashed: …"): the words after it say the same, plainly. */
export function withoutReasonCode(message: string): string {
  return message.replace(/^[A-Z][A-Za-z]+:\s+/, '')
}

/**
 * A pool's worker Pods in words: Radar's own count of the Pods it holds, with
 * the WorkerPool's status cited only where it says otherwise. readyReplicas
 * lags the Pods, so it is Kubernetes' claim, not the count.
 */
export function poolPodsText(k: Pick<SubstrateK8sPool, 'desired' | 'ready' | 'pods'>): string {
  const of = k.desired !== undefined ? ` of ${k.desired}` : ''
  if (!k.pods) return `Kubernetes calls ${k.ready}${of} worker Pods ready.`
  const terminating = k.pods.terminating ? `, and ${k.pods.terminating} ${k.pods.terminating === 1 ? 'is' : 'are'} terminating` : ''
  const claim = k.ready !== k.pods.ready ? ` The WorkerPool’s status says ${k.ready} ready.` : ''
  return `Kubernetes has created ${k.pods.created}${of} worker Pods; ${k.pods.ready} ${k.pods.ready === 1 ? 'is' : 'are'} ready${terminating}.${claim}`
}

/** Ready worker Pods: Radar's own count when it holds the Pods, else the WorkerPool's status. */
export function poolReadyPods(k: Pick<SubstrateK8sPool, 'ready' | 'pods'>): number {
  return k.pods?.ready ?? k.ready
}

// Substrate's own stuck rule (pkg/substrate StuckAfter): a transition held longer than this is stuck.
export const STUCK_AFTER_MS = 5 * 60_000

/** A resume, suspend, revert or delete that has held past Substrate's stuck threshold. */
export function stuckInTransition(state: SubstrateActorState, updateTime: string | undefined, now = Date.now()): boolean {
  if (!IN_PROGRESS_STATES.includes(state) || !updateTime) return false
  const t = Date.parse(updateTime)
  return Number.isFinite(t) && now - t > STUCK_AFTER_MS
}
