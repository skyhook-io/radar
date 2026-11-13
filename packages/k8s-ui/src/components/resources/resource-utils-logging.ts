import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

export const LOGGING_GROUP = 'logging.banzaicloud.io'

const OUTPUT_COMMON_KEYS = new Set(['loggingRef', 'protected', 'enabledNamespaces'])

// safeAddress keeps where a backend is (scheme, host, path) and drops what can
// carry a credential: userinfo, query and fragment. The server applies the
// same rule (prom.SafeAddress) to everything it returns.
export function safeAddress(raw: string): string {
  if (!raw.includes('://')) return /[@?#]/.test(raw) ? '[redacted]' : raw
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return '[redacted]'
  }
  if (!url.username && !url.password && !url.search && !url.hash) return raw
  return `${url.protocol}//${url.host}${url.pathname}`
}

export function getLoggingOutputType(data: any): string | null {
  const keys = Object.keys(data?.spec ?? {}).filter((k) => !OUTPUT_COMMON_KEYS.has(k)).sort()
  return keys[0] ?? null
}

export function getLoggingOutputAddress(data: any): string | null {
  const type = getLoggingOutputType(data)
  const block = type ? data.spec[type] : null
  if (!block || typeof block !== 'object') return null
  for (const key of ['url', 'endpoint', 'host']) {
    if (typeof block[key] === 'string' && block[key]) return safeAddress(block[key])
  }
  return null
}

const list = (values: string[]) => `[${values.join(', ')}]`

function conditions(c: any): string[] {
  const out = Object.entries(c?.labels ?? {}).map(([k, v]) => `label ${k}=${v}`)
  if (c?.container_names?.length) out.push(`container in ${list(c.container_names)}`)
  if (c?.hosts?.length) out.push(`node in ${list(c.hosts)}`)
  if (c?.namespaces?.length) out.push(`namespace in ${list(c.namespaces)}`)
  out.push(...Object.entries(c?.namespace_labels ?? {}).map(([k, v]) => `namespace label ${k}=${v}`))
  return out
}

// getLoggingFlowRules lists spec.match in order, or the deprecated
// spec.selectors as one select rule when match is empty.
export function getLoggingFlowRules(data: any): { exclude: boolean; conditions: string[] }[] {
  const match: any[] = data?.spec?.match ?? []
  if (match.length === 0) {
    const legacy = data?.spec?.selectors
    return legacy && Object.keys(legacy).length ? [{ exclude: false, conditions: conditions({ labels: legacy }) }] : []
  }
  return match.flatMap((m) => [
    ...(m?.select ? [{ exclude: false, conditions: conditions(m.select) }] : []),
    ...(m?.exclude ? [{ exclude: true, conditions: conditions(m.exclude) }] : []),
  ])
}

export function getLoggingStatus(data: any): StatusBadge {
  const problems = Number(data?.status?.problemsCount ?? 0)
  const active = data?.status?.active
  let text = 'Not reported'
  let level: StatusBadge['level'] = 'unknown'
  if (problems > 0) { text = `${problems} problem${problems === 1 ? '' : 's'}`; level = 'degraded' }
  else if (active === true) { text = 'Active'; level = 'healthy' }
  else if (active === false) { text = 'Inactive'; level = 'unhealthy' }
  return { text, level, color: healthColors[level] }
}
