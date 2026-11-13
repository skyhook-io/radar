// k3s / RKE2 Helm controller (helm.cattle.io): a HelmChart installs a Helm
// release through a helm-install-<name> Job; a HelmChartConfig of the same
// name overrides its values. HelmChart shares its kind with Flux's
// source.toolkit.fluxcd.io HelmChart, so every reader is gated on the group.
import { isApiGroup } from './resource-utils-cnpg'
import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

export const K3S_HELM_GROUP = 'helm.cattle.io'

const condition = (data: any, type: string) => (data?.status?.conditions ?? []).find((c: any) => c?.type === type)

// helm-controller reports only that it created the install Job, or that the
// Job failed; whether the release itself is healthy is the Helm view's answer.
export function getK3sHelmChartStatus(data: any): StatusBadge {
  let text = 'Not reported'
  let level: StatusBadge['level'] = 'unknown'
  if (condition(data, 'Failed')?.status === 'True') { text = 'Failed'; level = 'unhealthy' }
  else if (condition(data, 'JobCreated')?.status === 'True') { text = 'Job created'; level = 'neutral' }
  return { text, level, color: healthColors[level] }
}

export function k3sHelmTargetNamespace(data: any): string {
  return data?.spec?.targetNamespace || data?.metadata?.namespace || ''
}

// A chart URL with k3s's %{KUBERNETES_API}% placeholder is served by k3s itself.
export function k3sHelmChartSource(data: any): { chart: string; version?: string; repo?: string; bundled: boolean } {
  const spec = data?.spec ?? {}
  const raw = String(spec.chart ?? '')
  const bundled = raw.includes('%{KUBERNETES_API}%')
  const out: { chart: string; version?: string; repo?: string; bundled: boolean } = { chart: bundled ? raw.split('/').pop() || raw : raw, bundled }
  if (spec.version) out.version = spec.version
  if (spec.repo) out.repo = spec.repo
  return out
}

// Values can hold credentials, so only their size and the set keys are shown.
export function k3sValuesSummary(spec: any): { lines: number; setKeys: string[] } {
  const content = typeof spec?.valuesContent === 'string' ? spec.valuesContent.replace(/\n+$/, '') : ''
  const set = spec?.set && typeof spec.set === 'object' ? Object.keys(spec.set).sort() : []
  return { lines: content ? content.split('\n').length : 0, setKeys: set }
}

export function getK3sHelmChartColumnValue(data: any, column: string): string | undefined {
  if (!isApiGroup(data?.apiVersion, K3S_HELM_GROUP) || data?.kind !== 'HelmChart') return undefined
  const source = k3sHelmChartSource(data)
  switch (column) {
    case 'chart': return source.chart
    case 'version': return source.version ?? (source.bundled ? 'bundled' : '')
    case 'targetNamespace': return k3sHelmTargetNamespace(data)
    case 'status': return getK3sHelmChartStatus(data).text
  }
  return undefined
}
