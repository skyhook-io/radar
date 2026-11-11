// certificates.k8s.io CertificateSigningRequest utilities. The state reading
// mirrors pkg/csr; pkg/csr/testdata/states.json holds the cases both must agree on.

import type { StatusBadge, HealthLevel } from './resource-utils'
import { healthColors, minutesSince, PENDING_STUCK_MINUTES } from './resource-utils'
import { isApiGroup } from './resource-utils-cnpg'

export const CSR_GROUP = 'certificates.k8s.io'

/**
 * Where a request stands, read the way kubectl and client-go's
 * WaitForCertificate (what the kubelet waits with) read it: Denied and Failed
 * are final, and a returned certificate counts only once the request is Approved.
 */
export type CSRState = 'pending' | 'approved' | 'issued' | 'denied' | 'failed'

export interface CSRCondition {
  type: string
  reason?: string
  message?: string
  lastUpdateTime?: string
  lastTransitionTime?: string
}

export interface CSRStatus {
  state: CSRState
  /** The condition that decided the state: Denied, Failed or Approved. Absent while pending. */
  condition?: CSRCondition
  /** The approver's decision: the Denied condition, otherwise Approved. */
  decision?: CSRCondition
  /** The signer's verdict when it could not sign. */
  failure?: CSRCondition
  /** status.certificate is non-empty, whatever the state. */
  certificatePresent: boolean
}

const STATE_LABELS: Record<CSRState, string> = {
  pending: 'Pending approval',
  approved: 'Approved, not issued',
  issued: 'Issued',
  denied: 'Denied',
  failed: 'Failed',
}

const FINAL_LEVELS: Record<'issued' | 'denied' | 'failed', HealthLevel> = {
  issued: 'healthy',
  denied: 'unhealthy',
  failed: 'unhealthy',
}

/**
 * Waiting for an approver or a signer reads like a pending Pod: neutral at
 * first, degraded once it has waited PENDING_STUCK_MINUTES, counted from
 * creation while pending and from approval once approved.
 */
export function getCertificateSigningRequestLevel(status: CSRStatus, csr: any): HealthLevel {
  if (status.state !== 'pending' && status.state !== 'approved') return FINAL_LEVELS[status.state]
  const since = status.state === 'pending'
    ? csr?.metadata?.creationTimestamp
    : status.condition?.lastUpdateTime ?? status.condition?.lastTransitionTime
  return minutesSince(since) >= PENDING_STUCK_MINUTES ? 'degraded' : 'neutral'
}

export function isCertificateSigningRequest(data: any): boolean {
  return isApiGroup(data?.apiVersion, CSR_GROUP) && data?.kind === 'CertificateSigningRequest'
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function firstCondition(conditions: any[], type: string): CSRCondition | undefined {
  const c = conditions.find((cond: any) => cond?.type === type)
  if (!c) return undefined
  return {
    type,
    reason: text(c.reason),
    message: text(c.message),
    lastUpdateTime: text(c.lastUpdateTime),
    lastTransitionTime: text(c.lastTransitionTime),
  }
}

export function getCertificateSigningRequestState(csr: any): CSRStatus {
  const status = csr?.status && typeof csr.status === 'object' ? csr.status : {}
  const conditions: any[] = Array.isArray(status.conditions) ? status.conditions : []
  // The API admits only status True for these types, and kubectl and client-go
  // read the type alone.
  const approved = firstCondition(conditions, 'Approved')
  const denied = firstCondition(conditions, 'Denied')
  const failed = firstCondition(conditions, 'Failed')
  const certificatePresent = status.certificate !== undefined && status.certificate !== null && status.certificate !== ''
  const facts = { decision: denied ?? approved, failure: failed, certificatePresent }

  if (denied) return { ...facts, state: 'denied', condition: denied }
  if (failed) return { ...facts, state: 'failed', condition: failed }
  if (approved && certificatePresent) return { ...facts, state: 'issued', condition: approved }
  if (approved) return { ...facts, state: 'approved', condition: approved }
  return { ...facts, state: 'pending' }
}

export function getCertificateSigningRequestStatus(csr: any): StatusBadge {
  const status = getCertificateSigningRequestState(csr)
  const level = getCertificateSigningRequestLevel(status, csr)
  return { text: STATE_LABELS[status.state], color: healthColors[level], level }
}

/** The deciding condition's reason and message, for a tooltip or banner. */
export function getCertificateSigningRequestReason(csr: any): string | undefined {
  const { condition } = getCertificateSigningRequestState(csr)
  if (!condition) return undefined
  const parts = [condition.reason, condition.message].filter(Boolean)
  return parts.length > 0 ? parts.join(': ') : undefined
}

export function getCertificateSigningRequestSigner(csr: any): string {
  return text(csr?.spec?.signerName) ?? ''
}

export function getCertificateSigningRequestRequester(csr: any): string {
  return text(csr?.spec?.username) ?? ''
}
