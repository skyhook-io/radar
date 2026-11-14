// system-upgrade-controller (upgrade.cattle.io) Plans: a Plan selects Nodes
// and runs one upgrade Job per Node. Other projects also name a kind Plan, so
// every reader is gated on the group.
import { isApiGroup } from './resource-utils-cnpg'
import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'
import { matchesLabelSelector, unsupportedSelectorOperator } from '../../utils/label-selector'

export const UPGRADE_PLAN_GROUP = 'upgrade.cattle.io'

export interface PlanNode { name: string; labels: Record<string, string> }
export type PlanNodeState = 'upgraded' | 'upgrading' | 'pending'
export type UpgradePlanProgress =
  | { state: 'ok'; total: number; upgraded: number; upgrading: number; pending: number; rows: { name: string; state: PlanNodeState }[] }
  | { state: 'unsupported'; operator: string }

const condition = (data: any, type: string) => (data?.status?.conditions ?? []).find((c: any) => c?.type === type)

// Complete=False reasons the controller sets when the upgrade is stuck rather
// than running (system-upgrade-controller handle_upgrade.go / handle_batch.go).
const FAILED_REASONS = ['JobFailed', 'SelectNodesFailed', 'ValidationFailed']

export function getUpgradePlanStatus(data: any): StatusBadge {
  let text = 'Not reported'
  let level: StatusBadge['level'] = 'unknown'
  const applying = data?.status?.applying
  const complete = condition(data, 'Complete')
  if (condition(data, 'Validated')?.status === 'False') { text = 'Not valid'; level = 'unhealthy' }
  else if (complete?.status === 'False' && FAILED_REASONS.includes(complete.reason)) { text = 'Failed'; level = 'unhealthy' }
  else if (condition(data, 'LatestResolved')?.status === 'False') { text = 'Version not resolved'; level = 'degraded' }
  else if (complete?.status === 'False' && complete.reason === 'Waiting') { text = 'Waiting for window'; level = 'neutral' }
  else if ((Array.isArray(applying) && applying.length > 0) || complete?.status === 'False') { text = 'In progress'; level = 'neutral' }
  else if (condition(data, 'Complete')?.status === 'True') { text = 'Complete'; level = 'healthy' }
  return { text, level, color: healthColors[level] }
}

export function upgradePlanTarget(data: any): string {
  const spec = data?.spec ?? {}
  if (spec.version) return String(spec.version)
  if (spec.channel) return `channel ${spec.channel}`
  return data?.status?.latestVersion ? String(data.status.latestVersion) : '-'
}

// The image's repository, without tag or digest; a registry port is not a tag.
function imageRepository(image: string): string {
  const name = image.split('@')[0]
  const colon = name.lastIndexOf(':')
  return colon > name.lastIndexOf('/') ? name.slice(0, colon) : name
}

// A Plan that upgrades the Kubernetes distribution itself (k3s or RKE2), not
// an agent or an OS package.
export function isDistributionUpgradePlan(data: any): boolean {
  const image = data?.spec?.upgrade?.image
  if (typeof image !== 'string' || !image) return false
  const repo = imageRepository(image)
  return repo.endsWith('k3s-upgrade') || repo.endsWith('rke2-upgrade')
}

const HOSTNAME = 'kubernetes.io/hostname'

const STATE_ORDER: Record<PlanNodeState, number> = { upgrading: 0, pending: 1, upgraded: 2 }

// Upgraded: the controller labels a finished Node with the Plan's
// latestHash. A null selector selects no Node, an empty one every Node
// (metav1.LabelSelectorAsSelector, which the controller uses). Like the
// controller (plan.go NodeSelector) it skips Nodes without a hostname label
// and Nodes whose plan label is "disabled", and it names Nodes in
// status.applying by their kubernetes.io/hostname label, not their name.
export function upgradePlanProgress(plan: any, nodes: PlanNode[]): UpgradePlanProgress {
  const selector = plan?.spec?.nodeSelector
  const unsupported = selector ? unsupportedSelectorOperator(selector) : undefined
  if (unsupported !== undefined) return { state: 'unsupported', operator: unsupported }
  const label = `plan.upgrade.cattle.io/${plan?.metadata?.name ?? ''}`
  const hash = plan?.status?.latestHash || ''
  const applying = new Set<string>(Array.isArray(plan?.status?.applying) ? plan.status.applying : [])
  const selected = selector == null ? [] : nodes.filter(n =>
    HOSTNAME in n.labels && n.labels[label] !== 'disabled' && matchesLabelSelector(selector, n.labels))
  const rows = selected.map(n => ({
    name: n.name,
    state: (applying.has(n.labels[HOSTNAME] ?? n.name) ? 'upgrading' : hash && n.labels[label] === hash ? 'upgraded' : 'pending') as PlanNodeState,
  }))
  rows.sort((a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || a.name.localeCompare(b.name))
  const count = (s: PlanNodeState) => rows.filter(r => r.state === s).length
  return { state: 'ok', total: rows.length, upgraded: count('upgraded'), upgrading: count('upgrading'), pending: count('pending'), rows }
}

export function getUpgradePlanColumnValue(data: any, column: string): string | undefined {
  if (!isApiGroup(data?.apiVersion, UPGRADE_PLAN_GROUP) || data?.kind !== 'Plan') return undefined
  switch (column) {
    case 'version': return upgradePlanTarget(data)
    case 'concurrency': return data?.spec?.concurrency != null ? String(data.spec.concurrency) : '-'
    case 'status': return getUpgradePlanStatus(data).text
  }
  return undefined
}
