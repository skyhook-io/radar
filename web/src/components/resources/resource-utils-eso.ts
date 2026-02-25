// External Secrets Operator CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge } from './resource-utils'

// ============================================================================
// PROVIDER DETECTION
// ============================================================================

const PROVIDER_MAP: Record<string, string> = {
  aws: 'AWS Secrets Manager',
  azurekv: 'Azure Key Vault',
  gcpsm: 'GCP Secret Manager',
  vault: 'HashiCorp Vault',
  kubernetes: 'Kubernetes',
  oracle: 'Oracle Vault',
  ibm: 'IBM Secrets Manager',
  doppler: 'Doppler',
  onepassword: '1Password',
  senhasegura: 'senhasegura',
  gitlab: 'GitLab',
  webhook: 'Webhook',
  fake: 'Fake',
  keepersecurity: 'Keeper Security',
  scaleway: 'Scaleway',
  delinea: 'Delinea',
  chef: 'Chef',
  pulumi: 'Pulumi ESC',
  fortanix: 'Fortanix',
  passworddepot: 'Password Depot',
  device42: 'Device42',
  akeyless: 'Akeyless',
  beyondtrust: 'BeyondTrust',
  infisical: 'Infisical',
  passbolt: 'Passbolt',
  bitwarden: 'Bitwarden',
}

/**
 * Detect the provider type from a SecretStore/ClusterSecretStore spec.
 * Checks which provider key exists in spec.provider.
 */
export function getSecretStoreProviderType(resource: any): string {
  const provider = resource.spec?.provider
  if (!provider) return 'Unknown'

  for (const [key, label] of Object.entries(PROVIDER_MAP)) {
    if (provider[key]) return label
  }

  // Fallback: return the first key found in provider
  const keys = Object.keys(provider).filter(k => k !== 'retrySettings' && k !== 'controller')
  if (keys.length > 0) return keys[0]

  return 'Unknown'
}

/**
 * Get the short provider key (e.g., 'aws', 'vault') for color mapping.
 */
export function getSecretStoreProviderKey(resource: any): string {
  const provider = resource.spec?.provider
  if (!provider) return 'unknown'

  for (const key of Object.keys(PROVIDER_MAP)) {
    if (provider[key]) return key
  }

  return 'unknown'
}

/**
 * Get provider-specific details from a SecretStore/ClusterSecretStore.
 * Returns key-value pairs suitable for display, never exposing secret values.
 */
export function getSecretStoreProviderDetails(resource: any): Array<{ label: string; value: string }> {
  const provider = resource.spec?.provider
  if (!provider) return []

  const details: Array<{ label: string; value: string }> = []

  // AWS
  if (provider.aws) {
    const aws = provider.aws
    if (aws.region) details.push({ label: 'Region', value: aws.region })
    if (aws.service) details.push({ label: 'Service', value: aws.service })
    if (aws.role) details.push({ label: 'Role ARN', value: aws.role })
    if (aws.auth?.jwt?.serviceAccountRef?.name) {
      details.push({ label: 'Service Account', value: aws.auth.jwt.serviceAccountRef.name })
    }
  }

  // Azure Key Vault
  if (provider.azurekv) {
    const az = provider.azurekv
    if (az.vaultUrl) details.push({ label: 'Vault URL', value: az.vaultUrl })
    if (az.tenantId) details.push({ label: 'Tenant ID', value: az.tenantId })
    if (az.authType) details.push({ label: 'Auth Type', value: az.authType })
    if (az.environmentType) details.push({ label: 'Environment', value: az.environmentType })
  }

  // GCP Secret Manager
  if (provider.gcpsm) {
    const gcp = provider.gcpsm
    if (gcp.projectID) details.push({ label: 'Project ID', value: gcp.projectID })
    if (gcp.location) details.push({ label: 'Location', value: gcp.location })
  }

  // HashiCorp Vault
  if (provider.vault) {
    const vault = provider.vault
    if (vault.server) details.push({ label: 'Server', value: vault.server })
    if (vault.path) details.push({ label: 'Path', value: vault.path })
    if (vault.version) details.push({ label: 'Version', value: vault.version })
    if (vault.namespace) details.push({ label: 'Namespace', value: vault.namespace })
    // Auth method detection
    const auth = vault.auth
    if (auth) {
      if (auth.kubernetes) details.push({ label: 'Auth Method', value: 'Kubernetes' })
      else if (auth.appRole) details.push({ label: 'Auth Method', value: 'AppRole' })
      else if (auth.tokenSecretRef) details.push({ label: 'Auth Method', value: 'Token' })
      else if (auth.jwt) details.push({ label: 'Auth Method', value: 'JWT' })
      else if (auth.ldap) details.push({ label: 'Auth Method', value: 'LDAP' })
      else if (auth.userPass) details.push({ label: 'Auth Method', value: 'UserPass' })
      else if (auth.cert) details.push({ label: 'Auth Method', value: 'Certificate' })
      else if (auth.iam) details.push({ label: 'Auth Method', value: 'IAM' })
    }
  }

  // Kubernetes
  if (provider.kubernetes) {
    const k8s = provider.kubernetes
    if (k8s.server?.url) {
      details.push({ label: 'Server', value: k8s.server.url })
    } else {
      details.push({ label: 'Server', value: 'In-cluster' })
    }
    if (k8s.remoteNamespace) details.push({ label: 'Remote Namespace', value: k8s.remoteNamespace })
  }

  // Oracle Vault
  if (provider.oracle) {
    const oracle = provider.oracle
    if (oracle.vault) details.push({ label: 'Vault OCID', value: oracle.vault })
    if (oracle.region) details.push({ label: 'Region', value: oracle.region })
  }

  // IBM
  if (provider.ibm) {
    const ibm = provider.ibm
    if (ibm.serviceUrl) details.push({ label: 'Service URL', value: ibm.serviceUrl })
  }

  // Doppler
  if (provider.doppler) {
    const doppler = provider.doppler
    if (doppler.project) details.push({ label: 'Project', value: doppler.project })
    if (doppler.config) details.push({ label: 'Config', value: doppler.config })
  }

  // 1Password
  if (provider.onepassword) {
    const op = provider.onepassword
    if (op.connectHost) details.push({ label: 'Connect Host', value: op.connectHost })
    if (op.vaults) {
      const vaultNames = Object.keys(op.vaults)
      if (vaultNames.length > 0) details.push({ label: 'Vaults', value: vaultNames.join(', ') })
    }
  }

  // Akeyless
  if (provider.akeyless) {
    const ak = provider.akeyless
    if (ak.akeylessGWApiURL) details.push({ label: 'Gateway URL', value: ak.akeylessGWApiURL })
  }

  return details
}

// ============================================================================
// EXTERNALSECRET UTILITIES
// ============================================================================

export function getExternalSecretStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Synced', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Synced', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getExternalSecretStore(resource: any): { name: string; kind: string } {
  const ref = resource.spec?.secretStoreRef
  return {
    name: ref?.name || '-',
    kind: ref?.kind || 'SecretStore',
  }
}

export function getExternalSecretRefreshInterval(resource: any): string {
  return resource.spec?.refreshInterval || '-'
}

export function getExternalSecretSecretCount(resource: any): number {
  const dataCount = (resource.spec?.data || []).length
  const dataFromCount = (resource.spec?.dataFrom || []).length
  return dataCount + dataFromCount
}

export function getExternalSecretTargetName(resource: any): string {
  return resource.spec?.target?.name || resource.metadata?.name || '-'
}

export function getExternalSecretTargetCreationPolicy(resource: any): string {
  return resource.spec?.target?.creationPolicy || 'Owner'
}

export function getExternalSecretTargetDeletionPolicy(resource: any): string {
  return resource.spec?.target?.deletionPolicy || 'Retain'
}

export function getExternalSecretLastSync(resource: any): string {
  const refreshTime = resource.status?.refreshTime
  if (!refreshTime) return '-'
  return formatAge(refreshTime)
}

export function getExternalSecretDataMappings(resource: any): Array<{
  secretKey: string
  remoteKey: string
  remoteProperty?: string
  remoteVersion?: string
}> {
  const data = resource.spec?.data || []
  return data.map((d: any) => ({
    secretKey: d.secretKey || '',
    remoteKey: d.remoteRef?.key || '',
    remoteProperty: d.remoteRef?.property,
    remoteVersion: d.remoteRef?.version,
  }))
}

export function getExternalSecretDataFromSources(resource: any): Array<{
  type: string
  details: string
}> {
  const dataFrom = resource.spec?.dataFrom || []
  return dataFrom.map((df: any) => {
    if (df.extract) {
      return { type: 'extract', details: df.extract.key || '' }
    }
    if (df.find) {
      return { type: 'find', details: df.find.name?.regexp || df.find.tags ? 'by tags' : 'by name' }
    }
    if (df.sourceRef) {
      return { type: 'sourceRef', details: `${df.sourceRef.kind || ''}/${df.sourceRef.name || ''}` }
    }
    return { type: 'unknown', details: '' }
  })
}

/**
 * Get the provider type from an ExternalSecret by reading the secretStoreRef
 * and resolving via the store's spec. Since we don't have the store data here,
 * this is a best-effort based on available info.
 */
export function getExternalSecretProvider(resource: any): string {
  // ExternalSecrets don't directly contain provider info.
  // The provider is on the SecretStore. Return the store name as a hint.
  const store = getExternalSecretStore(resource)
  return store.name !== '-' ? store.name : '-'
}

// ============================================================================
// CLUSTEREXTERNALSECRET UTILITIES
// ============================================================================

export function getClusterExternalSecretStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  const failedNamespaces = resource.status?.failedNamespaces || []

  if (failedNamespaces.length > 0) {
    return { text: 'Partial Failure', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Check for PartiallyReady or similar
  const partialCond = conditions.find((c: any) => c.type === 'PartiallyReady')
  if (partialCond?.status === 'True') {
    return { text: 'Partial', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getClusterExternalSecretNamespaceCount(resource: any): number {
  const provisioned = resource.status?.provisionedNamespaces || []
  return provisioned.length
}

export function getClusterExternalSecretFailedCount(resource: any): number {
  const failed = resource.status?.failedNamespaces || []
  // failedNamespaces can be an array of strings or objects with namespace+reason
  return failed.length
}

export function getClusterExternalSecretNamespaces(resource: any): string[] {
  // From spec: either namespaceSelector or explicit namespaces
  return resource.spec?.namespaces || []
}

export function getClusterExternalSecretNamespaceSelector(resource: any): Record<string, string> | null {
  return resource.spec?.namespaceSelector?.matchLabels || null
}

export function getClusterExternalSecretProvisionedNamespaces(resource: any): string[] {
  return resource.status?.provisionedNamespaces || []
}

export function getClusterExternalSecretFailedNamespaces(resource: any): Array<{
  namespace: string
  reason?: string
}> {
  const failed = resource.status?.failedNamespaces || []
  return failed.map((f: any) => {
    if (typeof f === 'string') return { namespace: f }
    return { namespace: f.namespace || f.name || '', reason: f.reason || f.message }
  })
}

// ============================================================================
// SECRETSTORE UTILITIES
// ============================================================================

export function getSecretStoreStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getSecretStoreRetrySettings(resource: any): {
  maxRetries?: number
  retryInterval?: string
} | null {
  const retry = resource.spec?.retrySettings
  if (!retry) return null
  return {
    maxRetries: retry.maxRetries,
    retryInterval: retry.retryInterval,
  }
}

export function getSecretStoreController(resource: any): string | null {
  return resource.spec?.controller || null
}

// ============================================================================
// CLUSTERSECRETSTORE UTILITIES
// ============================================================================

// ClusterSecretStore uses the same structure as SecretStore
export function getClusterSecretStoreStatus(resource: any): StatusBadge {
  return getSecretStoreStatus(resource)
}
