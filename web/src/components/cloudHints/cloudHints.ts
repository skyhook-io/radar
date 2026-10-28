import { useCapabilities } from '../../api/client'
import { useNavCustomization } from '../../context/NavCustomization'
import { local, safeGet, safeSet, session } from './hintStorage'

// In-context pointers to Radar Cloud. Each names the surface it came from so
// the links the person opens from the Cloud dialog can say so (utm_term).
// Nothing here is sent anywhere on its own: the entry only rides in URLs the
// person clicks.
export const CLOUD_HINT_ENTRIES = [
  'context-switch',
  'timeline-history',
  'alert-issue',
  'alert-findings',
  'team-findings',
  'ai-setup',
  'ai-shared',
  'settings-ai',
  'gitops-remote',
] as const
export type CloudHintEntry = (typeof CLOUD_HINT_ENTRIES)[number]

const ENTRY_SET: ReadonlySet<string> = new Set(CLOUD_HINT_ENTRIES)

// The open event is a window event any script can dispatch, so its payload is
// validated before it reaches a URL.
export function isCloudHintEntry(value: unknown): value is CloudHintEntry {
  return typeof value === 'string' && ENTRY_SET.has(value)
}

// What a hint was looking at, shown back in the Cloud dialog and used to build
// the Radar Cloud link that picks up where the person left off: an alert rule
// for this issue ("alert"), or this resource with the team ("team").
export interface CloudAlertSubject {
  intent?: 'alert' | 'team'
  // The problem's display label ("Crash loop"), when the hint knows it.
  category?: string
  // The raw issue category ("crashloop"), the value alert rules match on.
  categoryRaw?: string
  // Radar's stable issue ID; what an alert rule pins to.
  issueId?: string
  kind: string
  group?: string
  name: string
  namespace?: string
  context?: string
}

function isAlertSubject(value: unknown): value is CloudAlertSubject {
  if (!value || typeof value !== 'object') return false
  const v = value as Record<string, unknown>
  const optionalString = (x: unknown) => x === undefined || typeof x === 'string'
  return (
    (v.intent === undefined || v.intent === 'alert' || v.intent === 'team') &&
    optionalString(v.category) &&
    optionalString(v.categoryRaw) &&
    optionalString(v.issueId) &&
    optionalString(v.group) &&
    typeof v.kind === 'string' &&
    typeof v.name === 'string' &&
    optionalString(v.namespace) &&
    optionalString(v.context)
  )
}

export interface CloudFunnelOpenRequest {
  entry: CloudHintEntry | null
  alert: CloudAlertSubject | null
}

export const OPEN_CLOUD_FUNNEL_EVENT = 'radar:open-cloud-funnel'

// Other OSS surfaces point at Radar Cloud by asking the top-bar button to open
// its dialog, so there is one pitch and one connect flow. The button is
// mounted whenever Radar runs standalone; embedded hosts never mount it, and
// every hint checks useCloudHintsEnabled before rendering.
export function openCloudFunnel(entry?: CloudHintEntry, alert?: CloudAlertSubject) {
  window.dispatchEvent(new CustomEvent(OPEN_CLOUD_FUNNEL_EVENT, { detail: { entry, alert } }))
}

export function readOpenRequest(event: Event): CloudFunnelOpenRequest {
  const detail = event instanceof CustomEvent ? (event.detail as Record<string, unknown> | null) : null
  const entry = isCloudHintEntry(detail?.entry) ? detail.entry : null
  const alert = entry && isAlertSubject(detail?.alert) ? detail.alert : null
  return { entry, alert }
}

// Adds the entry as utm_term to a link into the Hub. utm_content keeps naming
// the link that was clicked; utm_term says which hint opened the dialog.
export function withCloudHintEntry(url: string, entry: CloudHintEntry | null | undefined): string {
  if (!entry || !isCloudHintEntry(entry)) return url
  try {
    const u = new URL(url)
    u.searchParams.set('utm_term', entry)
    return u.toString()
  } catch {
    return url
  }
}

// The same rule the top-bar button follows: the server decides whether there
// is anything to point at (cloudConnect is absent in cloud mode, on a
// tunneled install, out of the rollout cohort, or with RADAR_CLOUD_FUNNEL=off),
// and embedded hosts never show Radar Cloud hints.
export function useCloudHintsEnabled(): boolean {
  const { embedded } = useNavCustomization()
  const capabilities = useCapabilities()
  return !embedded && !!capabilities.data?.cloudConnect
}

export type CloudHintSurface = 'context-switch' | 'alert-issue' | 'alert-findings' | 'ai-shared'

const dismissKey = (surface: CloudHintSurface) => `radar.cloudHint.${surface}.dismissed`

export function isCloudHintDismissed(surface: CloudHintSurface): boolean {
  return safeGet(local, dismissKey(surface)) === 'true'
}

export function dismissCloudHint(surface: CloudHintSurface) {
  safeSet(local, dismissKey(surface), 'true')
}

// The issue prompt's X hides it for one issue in this tab only. Keyed by
// context too: issue ids are per cluster, and a hide on one cluster must not
// hide an unrelated issue after a switch.
const issueHideKey = 'radar.cloudHint.alert-issue.hidden'

function readHiddenIssues(): string[] {
  try {
    const parsed = JSON.parse(safeGet(session, issueHideKey) ?? '[]')
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

export function issueHintKey(context: string | undefined, issueId: string): string {
  return `${context ?? ''}\u0000${issueId}`
}

export function isIssueHintHidden(key: string): boolean {
  return readHiddenIssues().includes(key)
}

export function hideIssueHint(key: string) {
  const hidden = readHiddenIssues()
  if (hidden.includes(key)) return
  // Bounded: a long session with many hides should not grow storage forever.
  safeSet(session, issueHideKey, JSON.stringify([...hidden, key].slice(-200)))
}

export {
  CONTEXT_SWITCH_THRESHOLD,
  CONTEXT_SWITCH_WINDOW_MS,
  CONTEXT_SWITCHED_EVENT,
  comparedContexts,
  readSwitchLog,
  recentSwitches,
  recordContextSwitch,
  type ContextSwitchRecord,
} from './contextSwitchLog'
