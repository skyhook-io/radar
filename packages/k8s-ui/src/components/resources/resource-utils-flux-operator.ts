// Flux Operator (fluxcd.controlplane.io): FluxInstance installs Flux,
// FluxReport describes what is installed and reconciling, ResourceSet and
// ResourceSetInputProvider template objects from inputs.
import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'
import { getFluxResourceStatus } from './resource-utils-flux'

export const FLUX_OPERATOR_GROUP = 'fluxcd.controlplane.io'
export const FLUX_OPERATOR_PLURALS = ['fluxinstances', 'fluxreports', 'resourcesets', 'resourcesetinputproviders']

// These kinds have no spec.suspend; Flux Operator pauses them by annotation.
export function getFluxOperatorStatus(data: any): StatusBadge {
  if (data?.metadata?.annotations?.['fluxcd.controlplane.io/reconcile'] === 'disabled') {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  return getFluxResourceStatus(data)
}

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
        if (!Array.isArray(spec.reconcilers)) return ''
        return String(spec.reconcilers.reduce((n: number, r: any) => n + (Number(r?.stats?.failing) || 0), 0))
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

const SECRET_PARAM = /token|secret|password|passwd|key|sig|auth/i

// redactUrlCredentials drops user info ("user:token@") and secret-looking
// query values from a URL. scp-style git URLs keep host and path. Other text
// that doesn't parse is shown only when it can't be hiding credentials.
export function redactUrlCredentials(url: string): string {
  if (!url) return ''
  const scp = /^[^@/:\s]+@([^:/\s]+):(?!\/)(.*)$/.exec(url)
  if (scp) return `${scp[1]}:${scp[2]}`
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return url.includes('@') ? '' : url
  }
  const secretParams = [...parsed.searchParams.keys()].filter((k) => SECRET_PARAM.test(k))
  if (!parsed.username && !parsed.password && secretParams.length === 0) return url
  parsed.username = ''
  parsed.password = ''
  for (const k of secretParams) parsed.searchParams.set(k, 'redacted')
  return parsed.toString().replace(/\/$/, url.endsWith('/') ? '/' : '')
}
