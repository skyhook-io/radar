import type { HealthLevel, StatusBadge } from '@skyhook-io/k8s-ui'
import type { AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'

// Plain words for the controller's and Kubernetes' reasons a reading names.
// The readings keep the raw reason as their text, which stays the filter and
// sort value; the lists show these labels with the reason on hover. They
// mirror pkg/agentsandbox's failureTitles and claimTitles, so a row's badge
// and its Needs attention title agree.
const REASON_LABELS: Record<string, string> = {
  ImagePullBackOff: 'Image can’t be pulled',
  ErrImagePull: 'Image can’t be pulled',
  InvalidImageName: 'Image name is invalid',
  CrashLoopBackOff: 'Container keeps crashing',
  CreateContainerConfigError: 'Container can’t be configured',
  PodFailed: 'Pod failed',
  InvalidConfiguration: 'Invalid configuration',
  MultiplePods: 'More than one Pod matches',
  WarmPoolNotFound: 'Warm pool not found',
  TemplateNotFound: 'Template not found',
  InvalidMetadata: 'Invalid Pod metadata',
  EnvVarsInjectionRejected: 'Environment variables rejected',
  VolumeClaimTemplatesError: 'Volume claim templates rejected',
  SandboxMissing: 'Its Sandbox is gone',
  ReconcilerError: 'Controller can’t reconcile it',
  Unschedulable: 'Can’t schedule',
}

/** A reading's label for a list: plain words, and the raw reason when they differ from it. */
export function readingLabel(status: StatusBadge): { label: string; reason?: string } {
  const label = REASON_LABELS[status.text]
  return label ? { label, reason: status.text } : { label: status.text }
}

/**
 * The warm pools whose reserve is short because the scheduler can't place
 * their sandboxes, as the server's Needs attention says it: a
 * reserveBelowTarget row whose source is the scheduler. Keys are
 * "namespace/name". A pool refilling normally has no such row.
 */
export function unschedulablePools(workspace: AgentSandboxWorkspaceResponse | undefined): Set<string> {
  const out = new Set<string>()
  for (const a of workspace?.attention ?? []) {
    if (a.rule === 'reserveBelowTarget' && a.rawDetailSource === 'scheduler') out.add(`${a.subject.namespace ?? ''}/${a.subject.name}`)
  }
  return out
}

export const CANT_SCHEDULE = 'Can’t schedule'

export type RowTone = Extract<HealthLevel, 'unhealthy' | 'alert' | 'degraded'>

/** What the server's summary says about the objects the lists show. */
export interface ServerReadings {
  /** Warm pools short because the scheduler can't place their sandboxes. */
  unschedulable: Set<string>
  /**
   * The severity of the Needs attention row about an object, as a health tone,
   * keyed by objectKey. A list colors that object's badge with it, so one
   * state reads the same color on the Overview and in the lists.
   */
  tones: Map<string, RowTone>
}

export const objectKey = (kind: string, namespace: string, name: string) => `${kind}/${namespace}/${name}`

const TONE: Record<'critical' | 'alert' | 'warning', RowTone> = { critical: 'unhealthy', alert: 'alert', warning: 'degraded' }

export function serverReadings(workspace: AgentSandboxWorkspaceResponse | undefined): ServerReadings {
  const tones = new Map<string, RowTone>()
  for (const a of workspace?.attention ?? []) tones.set(objectKey(a.subject.kind, a.subject.namespace ?? '', a.subject.name), TONE[a.severity])
  return { unschedulable: unschedulablePools(workspace), tones }
}
