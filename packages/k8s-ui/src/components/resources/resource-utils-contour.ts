// Contour HTTPProxy CRD utility functions for resource list cells and detail renderers

export function getHTTPProxyFQDN(resource: any): string {
  return resource.spec?.virtualhost?.fqdn || '-'
}

export function getHTTPProxyStatus(resource: any): { status: string; label: string } {
  const currentStatus = resource.status?.currentStatus?.toLowerCase()
  if (currentStatus === 'valid') return { status: 'healthy', label: 'Valid' }
  if (currentStatus === 'invalid') return { status: 'unhealthy', label: 'Invalid' }
  if (currentStatus === 'orphaned') return { status: 'degraded', label: 'Orphaned' }
  return { status: 'unknown', label: '-' }
}

export function getHTTPProxyRouteCount(resource: any): number {
  return resource.spec?.routes?.length || 0
}

export function getHTTPProxyServiceCount(resource: any): number {
  let count = 0
  for (const route of resource.spec?.routes || []) {
    count += (route.services || []).length
  }
  count += (resource.spec?.tcpproxy?.services || []).length
  return count
}

export function getHTTPProxyIncludeCount(resource: any): number {
  return resource.spec?.includes?.length || 0
}

export function hasHTTPProxyTLS(resource: any): boolean {
  return !!resource.spec?.virtualhost?.tls
}

// Contour reads tls.secretName as "namespace/name" when another namespace
// delegates the Secret with a TLSCertificateDelegation; a bare name, or an
// empty namespace, is in the proxy's own namespace.
export function getHTTPProxyTLSSecret(resource: any): { namespace: string; name: string } | null {
  const ref: string | undefined = resource.spec?.virtualhost?.tls?.secretName
  if (!ref) return null
  const ownNamespace = resource.metadata?.namespace ?? ''
  const slash = ref.indexOf('/')
  if (slash < 0) return { namespace: ownNamespace, name: ref }
  return { namespace: ref.slice(0, slash) || ownNamespace, name: ref.slice(slash + 1) }
}
