interface FluxTimestamp {
  value: string
  milliseconds: number
  submillisecond: number
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function fluxStatus(resource: unknown, group: string, kind: string): Record<string, unknown> | undefined {
  if (!object(resource) || resource.kind !== kind || typeof resource.apiVersion !== 'string'
    || !resource.apiVersion.startsWith(`${group}/`) || !object(resource.status)) return undefined
  return resource.status
}

function history(resource: unknown, group: string, kind: string): unknown[] {
  const entries = fluxStatus(resource, group, kind)?.history
  return Array.isArray(entries) ? entries : []
}

function timestamp(value: unknown): FluxTimestamp | undefined {
  if (typeof value !== 'string') return undefined
  const match = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.(\d{1,9}))?(?:Z|[+-]\d{2}:\d{2})$/.exec(value)
  if (!match) return undefined
  const milliseconds = Date.parse(value)
  if (!Number.isFinite(milliseconds) || milliseconds === -62135596800000) return undefined
  const day = new Date(`${value.slice(0, 10)}T00:00:00Z`)
  if (!Number.isFinite(day.getTime()) || day.toISOString().slice(0, 10) !== value.slice(0, 10)
    || Number(value.slice(11, 13)) > 23) return undefined
  return { value, milliseconds, submillisecond: Number((match[1] ?? '').padEnd(9, '0').slice(3)) }
}

// Flux history may omit failures before apply, and firstReconciled can span
// several source revisions that produced the same manifests.
export function getFluxLastApplyAttempt(resource: unknown): string | undefined {
  let latest: FluxTimestamp | undefined
  for (const entry of history(resource, 'kustomize.toolkit.fluxcd.io', 'Kustomization')) {
    const candidate = object(entry) ? timestamp(entry.lastReconciled) : undefined
    if (candidate && (!latest || candidate.milliseconds > latest.milliseconds
      || (candidate.milliseconds === latest.milliseconds && candidate.submillisecond > latest.submillisecond))) latest = candidate
  }
  return latest?.value
}

export interface FluxDeployedRelease {
  version: number
  chartVersion: string
  appVersion: string
  status: 'deployed'
  firstDeployed?: string
  lastDeployed?: string
}

// Select identity first so a missing date never borrows another release's time.
export function getFluxDeployedRelease(resource: unknown): FluxDeployedRelease | undefined {
  let selected: FluxDeployedRelease | undefined
  for (const entry of history(resource, 'helm.toolkit.fluxcd.io', 'HelmRelease')) {
    if (!object(entry) || entry.status !== 'deployed' || typeof entry.version !== 'number'
      || !Number.isSafeInteger(entry.version) || entry.version <= 0
      || (selected && entry.version <= selected.version)) continue
    selected = {
      version: entry.version,
      chartVersion: typeof entry.chartVersion === 'string' ? entry.chartVersion : '',
      appVersion: typeof entry.appVersion === 'string' ? entry.appVersion : '',
      status: 'deployed',
      firstDeployed: timestamp(entry.firstDeployed)?.value,
      lastDeployed: timestamp(entry.lastDeployed)?.value,
    }
  }
  return selected
}

export function getFluxHelmChartVersion(resource: unknown): string | undefined {
  const deployed = getFluxDeployedRelease(resource)
  if (deployed?.chartVersion) return deployed.chartVersion
  const applied = fluxStatus(resource, 'helm.toolkit.fluxcd.io', 'HelmRelease')?.lastAppliedRevision
  return typeof applied === 'string' && applied !== '' ? applied : undefined
}
