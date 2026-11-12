// Flux Operator (fluxcd.controlplane.io): FluxInstance installs Flux,
// FluxReport describes what is installed and reconciling, ResourceSet and
// ResourceSetInputProvider template objects from inputs.
import type { StatusBadge } from './resource-utils'
import { getFluxResourceStatus } from './resource-utils-flux'

export const FLUX_OPERATOR_GROUP = 'fluxcd.controlplane.io'

export const getFluxOperatorStatus = (data: any): StatusBadge => getFluxResourceStatus(data)

const count = (value: unknown): string => String(Array.isArray(value) ? value.length : 0)

// Undefined means the column isn't one of these kinds' curated columns.
export function getFluxOperatorColumnValue(kind: string, data: any, column: string): string | undefined {
  const spec = data?.spec ?? {}
  const status = data?.status ?? {}
  switch (kind) {
    case 'fluxinstances':
      if (column === 'version') return spec.distribution?.version ?? ''
      if (column === 'applied') return status.lastAppliedRevision ?? ''
      if (column === 'components') return count(status.components)
      return undefined
    case 'fluxreports':
      if (column === 'version') return spec.distribution?.version ?? ''
      if (column === 'operator') return spec.operator?.version ?? ''
      if (column === 'failing') {
        const reconcilers: any[] = Array.isArray(spec.reconcilers) ? spec.reconcilers : []
        return String(reconcilers.reduce((n, r) => n + (Number(r?.stats?.failing) || 0), 0))
      }
      return undefined
    case 'resourcesets':
      if (column === 'inputs') return String((Array.isArray(spec.inputs) ? spec.inputs.length : 0) + (Array.isArray(spec.inputsFrom) ? spec.inputsFrom.length : 0))
      if (column === 'resources') return count(status.inventory?.entries)
      return undefined
    case 'resourcesetinputproviders':
      if (column === 'type') return spec.type ?? ''
      if (column === 'url') return redactUrlCredentials(spec.url ?? '')
      return undefined
  }
  return undefined
}

// redactUrlCredentials drops user info ("user:token@") from a URL. Text that
// doesn't parse is shown only when it can't be hiding credentials.
export function redactUrlCredentials(url: string): string {
  if (!url) return ''
  try {
    const parsed = new URL(url)
    if (!parsed.username && !parsed.password) return url
    parsed.username = ''
    parsed.password = ''
    return parsed.toString().replace(/\/$/, url.endsWith('/') ? '/' : '')
  } catch {
    return url.includes('@') ? '' : url
  }
}
