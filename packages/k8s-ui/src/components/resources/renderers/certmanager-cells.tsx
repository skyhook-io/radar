// cert-manager cell components for ResourcesView table — extracted from ResourcesView.tsx

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getCertificateStatus,
  getCertificateDomains,
  getCertificateIssuer,
  getCertificateExpiry,
  getCertificateRequestStatus,
  getCertificateRequestIssuer,
  getCertificateRequestApproved,
  getClusterIssuerStatus,
  getClusterIssuerType,
  getIssuerStatus,
  getIssuerType,
  getOrderState,
  getOrderDomains,
  getOrderIssuer,
  getChallengeState,
  getChallengeType,
  getChallengeDomain,
  getChallengePresented,
} from '../resource-utils'

export function CertificateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCertificateStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'domains': {
      const domains = getCertificateDomains(resource)
      return (
        <Tooltip content={domains}>
          <span className="text-sm text-theme-text-secondary truncate block">{domains || '-'}</span>
        </Tooltip>
      )
    }
    case 'issuer': {
      const issuer = getCertificateIssuer(resource)
      return <span className="text-sm text-theme-text-secondary">{issuer}</span>
    }
    case 'expires': {
      const expiry = getCertificateExpiry(resource)
      return (
        <span className={clsx(
          'text-sm font-medium',
          expiry.level === 'unhealthy' ? 'text-red-400' :
          expiry.level === 'degraded' ? 'text-yellow-400' :
          expiry.level === 'healthy' ? 'text-green-400' :
          'text-theme-text-tertiary'
        )}>
          {expiry.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CertificateRequestCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCertificateRequestStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'issuer':
      return <span className="text-sm text-theme-text-secondary">{getCertificateRequestIssuer(resource)}</span>
    case 'approved':
      return <span className="text-sm text-theme-text-secondary">{getCertificateRequestApproved(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterIssuerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getClusterIssuerStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'issuerType':
      return <span className="text-sm text-theme-text-secondary">{getClusterIssuerType(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function IssuerCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getIssuerStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'issuerType':
      return <span className="text-sm text-theme-text-secondary">{getIssuerType(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function OrderCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'state': {
      const state = getOrderState(resource)
      return (
        <span className={clsx('badge', state.color)}>
          {state.text}
        </span>
      )
    }
    case 'domains': {
      const domains = getOrderDomains(resource)
      return (
        <Tooltip content={domains}>
          <span className="text-sm text-theme-text-secondary truncate block">{domains}</span>
        </Tooltip>
      )
    }
    case 'issuer':
      return <span className="text-sm text-theme-text-secondary">{getOrderIssuer(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ChallengeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'state': {
      const state = getChallengeState(resource)
      return (
        <span className={clsx('badge', state.color)}>
          {state.text}
        </span>
      )
    }
    case 'challengeType':
      return <span className="text-sm text-theme-text-secondary">{getChallengeType(resource)}</span>
    case 'domain':
      return <span className="text-sm text-theme-text-secondary">{getChallengeDomain(resource)}</span>
    case 'presented':
      return <span className="text-sm text-theme-text-secondary">{getChallengePresented(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
