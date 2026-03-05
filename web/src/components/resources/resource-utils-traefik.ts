// Traefik CRD utility functions for resource list cells and detail renderers

// ============================================================================
// INGRESSROUTE UTILITIES
// ============================================================================

export function getIngressRouteEntryPoints(resource: any): string {
  const eps = resource.spec?.entryPoints || []
  if (eps.length === 0) return '-'
  if (eps.length <= 3) return eps.join(', ')
  return `${eps.slice(0, 2).join(', ')} +${eps.length - 2} more`
}

export function getIngressRouteHosts(resource: any): string {
  const routes = resource.spec?.routes || []
  const hosts = new Set<string>()
  for (const route of routes) {
    const match = route.match || ''
    // Extract Host(`...`) patterns from Traefik match expressions
    const hostMatches = match.matchAll(/Host\(`([^`]+)`\)/gi)
    for (const m of hostMatches) {
      m[1].split(',').forEach((h: string) => {
        const trimmed = h.trim()
        if (trimmed) hosts.add(trimmed)
      })
    }
    // Also extract HostSNI(`...`) for TCP/TLS
    const sniMatches = match.matchAll(/HostSNI\(`([^`]+)`\)/gi)
    for (const m of sniMatches) {
      m[1].split(',').forEach((h: string) => {
        const trimmed = h.trim()
        if (trimmed && trimmed !== '*') hosts.add(trimmed)
      })
    }
  }
  const arr = Array.from(hosts)
  if (arr.length === 0) return '*'
  if (arr.length <= 2) return arr.join(', ')
  return `${arr[0]} +${arr.length - 1} more`
}

export function getIngressRouteRoutesSummary(resource: any): string {
  const routes = resource.spec?.routes || []
  if (routes.length === 0) return 'No routes'

  const summaries: string[] = []
  for (const route of routes) {
    const services = route.services || []
    const svcNames = services.map((s: any) => {
      const kind = s.kind && s.kind !== 'Service' ? `${s.kind}/` : ''
      const port = s.port ? `:${s.port}` : ''
      return `${kind}${s.name}${port}`
    })
    if (svcNames.length > 0) {
      summaries.push(svcNames.join(', '))
    }
  }

  if (summaries.length === 0) return `${routes.length} route(s)`
  if (summaries.length === 1) return summaries[0]
  return `${summaries[0]}; +${summaries.length - 1} more`
}

export function hasIngressRouteTLS(resource: any): boolean {
  return !!resource.spec?.tls
}

export function getIngressRouteMiddlewareCount(resource: any): number {
  const routes = resource.spec?.routes || []
  const seen = new Set<string>()
  for (const route of routes) {
    for (const mw of route.middlewares || []) {
      const ns = mw.namespace || ''
      seen.add(`${ns}/${mw.name}`)
    }
  }
  return seen.size
}

export function getIngressRouteServiceCount(resource: any): number {
  const routes = resource.spec?.routes || []
  const seen = new Set<string>()
  for (const route of routes) {
    for (const svc of route.services || []) {
      const kind = svc.kind || 'Service'
      const ns = svc.namespace || ''
      seen.add(`${kind}/${ns}/${svc.name}`)
    }
  }
  return seen.size
}

// ============================================================================
// MIDDLEWARE UTILITIES
// ============================================================================

export function getMiddlewareType(resource: any): string {
  const spec = resource.spec || {}
  // Each middleware has exactly one spec key indicating its type
  const types = [
    'addPrefix', 'basicAuth', 'buffering', 'chain', 'circuitBreaker',
    'compress', 'contentType', 'digestAuth', 'errors', 'forwardAuth',
    'grpcWeb', 'headers', 'inFlightReq', 'ipAllowList', 'ipWhiteList',
    'passTLSClientCert', 'plugin', 'rateLimit', 'redirectRegex',
    'redirectScheme', 'replacePath', 'replacePathRegex', 'retry',
    'stripPrefix', 'stripPrefixRegex',
  ]
  for (const t of types) {
    if (spec[t]) return t
  }
  return 'unknown'
}

export function getMiddlewareChainMembers(resource: any): string[] {
  return (resource.spec?.chain?.middlewares || []).map((m: any) => m.name)
}

// ============================================================================
// TRAEFIKSERVICE UTILITIES
// ============================================================================

export function getTraefikServiceType(resource: any): string {
  const spec = resource.spec || {}
  if (spec.weighted) return 'Weighted Round Robin'
  if (spec.mirroring) return 'Mirroring'
  if (spec.highestRandomWeight) return 'Highest Random Weight'
  return 'Unknown'
}

export function getTraefikServiceTargets(resource: any): string {
  const spec = resource.spec || {}
  if (spec.weighted) {
    const svcs = spec.weighted.services || []
    if (svcs.length === 0) return '-'
    return svcs.map((s: any) => {
      const w = s.weight !== undefined ? ` (${s.weight}%)` : ''
      return `${s.name}${w}`
    }).join(', ')
  }
  if (spec.mirroring) {
    const primary = spec.mirroring.name || '-'
    const mirrors = spec.mirroring.mirrors || []
    if (mirrors.length === 0) return primary
    return `${primary} + ${mirrors.length} mirror(s)`
  }
  if (spec.highestRandomWeight) {
    const svcs = spec.highestRandomWeight.services || []
    if (svcs.length === 0) return '-'
    return svcs.map((s: any) => s.name).join(', ')
  }
  return '-'
}

// ============================================================================
// SERVERSTRANSPORT UTILITIES
// ============================================================================

export function getServersTransportInsecure(resource: any): boolean {
  return !!resource.spec?.insecureSkipVerify
}

export function getServersTransportServerName(resource: any): string | null {
  return resource.spec?.serverName || null
}

// ============================================================================
// TLSOPTION UTILITIES
// ============================================================================

export function getTLSOptionMinVersion(resource: any): string | null {
  return resource.spec?.minVersion || null
}

export function getTLSOptionMaxVersion(resource: any): string | null {
  return resource.spec?.maxVersion || null
}

export function getTLSOptionCipherSuites(resource: any): string[] {
  return resource.spec?.cipherSuites || []
}
