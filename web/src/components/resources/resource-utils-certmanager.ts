// cert-manager CRD utility functions — extracted from resource-utils.ts

import type { StatusBadge, HealthLevel } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// CERTIFICATE UTILITIES (cert-manager CRD)
// ============================================================================

export function getCertificateStatus(cert: any): StatusBadge {
  const conditions = cert.status?.conditions || []
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getCertificateDomains(cert: any): string {
  const dnsNames = cert.spec?.dnsNames || []
  if (dnsNames.length === 0) return '-'
  if (dnsNames.length === 1) return dnsNames[0]
  if (dnsNames.length <= 2) return dnsNames.join(', ')
  return `${dnsNames[0]} +${dnsNames.length - 1}`
}

export function getCertificateIssuer(cert: any): string {
  const ref = cert.spec?.issuerRef
  if (!ref) return '-'
  return ref.name || '-'
}

export function getCertificateExpiry(cert: any): { text: string; level: HealthLevel } {
  const notAfter = cert.status?.notAfter
  if (!notAfter) return { text: '-', level: 'unknown' }

  const expiryDate = new Date(notAfter)
  const now = new Date()
  const daysUntilExpiry = Math.floor((expiryDate.getTime() - now.getTime()) / (1000 * 60 * 60 * 24))

  if (daysUntilExpiry < 0) {
    return { text: `Expired ${-daysUntilExpiry}d ago`, level: 'unhealthy' }
  }
  if (daysUntilExpiry < 7) {
    return { text: `${daysUntilExpiry}d`, level: 'unhealthy' }
  }
  if (daysUntilExpiry < 30) {
    return { text: `${daysUntilExpiry}d`, level: 'degraded' }
  }
  return { text: `${daysUntilExpiry}d`, level: 'healthy' }
}

// ============================================================================
// CERTIFICATE REQUEST UTILITIES (cert-manager)
// ============================================================================

export function getCertificateRequestStatus(cr: any): StatusBadge {
  const conditions = cr.status?.conditions || []
  const denied = conditions.find((c: any) => c.type === 'Denied' && c.status === 'True')
  if (denied) return { text: 'Denied', color: healthColors.unhealthy, level: 'unhealthy' }

  const ready = conditions.find((c: any) => c.type === 'Ready')
  if (ready?.status === 'True') return { text: 'Issued', color: healthColors.healthy, level: 'healthy' }
  if (ready?.status === 'False') return { text: ready.reason || 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }

  const approved = conditions.find((c: any) => c.type === 'Approved' && c.status === 'True')
  if (approved) return { text: 'Approved', color: healthColors.degraded, level: 'degraded' }

  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getCertificateRequestIssuer(cr: any): string {
  const ref = cr.spec?.issuerRef
  if (!ref) return '-'
  return ref.name || '-'
}

export function getCertificateRequestApproved(cr: any): string {
  const conditions = cr.status?.conditions || []
  const approved = conditions.find((c: any) => c.type === 'Approved')
  if (!approved) return 'Pending'
  return approved.status === 'True' ? 'Yes' : 'No'
}

// ============================================================================
// CLUSTER ISSUER UTILITIES (cert-manager)
// ============================================================================

export function getClusterIssuerStatus(issuer: any): StatusBadge {
  const conditions = issuer.status?.conditions || []
  const ready = conditions.find((c: any) => c.type === 'Ready')
  if (ready?.status === 'True') return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  if (ready?.status === 'False') return { text: ready.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getClusterIssuerType(issuer: any): string {
  const spec = issuer.spec || {}
  if (spec.acme) return 'ACME'
  if (spec.ca) return 'CA'
  if (spec.selfSigned !== undefined) return 'SelfSigned'
  if (spec.vault) return 'Vault'
  if (spec.venafi) return 'Venafi'
  return 'Unknown'
}

// ============================================================================
// ISSUER UTILITIES (cert-manager)
// Issuers and ClusterIssuers share the same spec/status schema in cert-manager,
// differing only in scope (namespaced vs cluster-wide).
// ============================================================================

export const getIssuerStatus = getClusterIssuerStatus
export const getIssuerType = getClusterIssuerType

// ============================================================================
// ORDER UTILITIES (cert-manager ACME)
// ============================================================================

export function getOrderState(order: any): StatusBadge {
  const state = order.status?.state || ''
  switch (state.toLowerCase()) {
    case 'valid':
      return { text: 'Valid', color: healthColors.healthy, level: 'healthy' }
    case 'ready':
      return { text: 'Ready', color: 'bg-blue-500/20 text-blue-400', level: 'healthy' }
    case 'pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    case 'invalid':
      return { text: 'Invalid', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'expired':
      return { text: 'Expired', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'errored':
      return { text: 'Errored', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: state || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getOrderDomains(order: any): string {
  const dnsNames = order.spec?.dnsNames || []
  if (dnsNames.length === 0) return '-'
  if (dnsNames.length === 1) return dnsNames[0]
  if (dnsNames.length <= 2) return dnsNames.join(', ')
  return `${dnsNames[0]} +${dnsNames.length - 1}`
}

export function getOrderIssuer(order: any): string {
  return order.spec?.issuerRef?.name || '-'
}

// ============================================================================
// CHALLENGE UTILITIES (cert-manager ACME)
// ============================================================================

export function getChallengeState(challenge: any): StatusBadge {
  const state = challenge.status?.state || ''
  switch (state.toLowerCase()) {
    case 'valid':
      return { text: 'Valid', color: healthColors.healthy, level: 'healthy' }
    case 'ready':
      return { text: 'Ready', color: 'bg-blue-500/20 text-blue-400', level: 'healthy' }
    case 'pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    case 'processing':
      return { text: 'Processing', color: healthColors.degraded, level: 'degraded' }
    case 'invalid':
      return { text: 'Invalid', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'expired':
      return { text: 'Expired', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'errored':
      return { text: 'Errored', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: state || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getChallengeType(challenge: any): string {
  const type = challenge.spec?.type
  if (type) return type
  if (challenge.spec?.solver?.http01) return 'HTTP-01'
  if (challenge.spec?.solver?.dns01) return 'DNS-01'
  return 'Unknown'
}

export function getChallengeDomain(challenge: any): string {
  return challenge.spec?.dnsName || '-'
}

export function getChallengePresented(challenge: any): string {
  const presented = challenge.status?.presented
  if (presented === true) return 'Yes'
  if (presented === false) return 'No'
  return '-'
}
