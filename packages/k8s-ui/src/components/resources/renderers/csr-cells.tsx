// CertificateSigningRequest cell components for the ResourcesView table.

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getCertificateSigningRequestReason,
  getCertificateSigningRequestRequester,
  getCertificateSigningRequestSigner,
  getCertificateSigningRequestStatus,
} from '../resource-utils-csr'

export function CertificateSigningRequestCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCertificateSigningRequestStatus(resource)
      const reason = getCertificateSigningRequestReason(resource)
      const badge = <span className={clsx('badge', status.color)}>{status.text}</span>
      return reason ? <Tooltip content={reason}>{badge}</Tooltip> : badge
    }
    case 'signerName':
    case 'username': {
      const value = column === 'signerName' ? getCertificateSigningRequestSigner(resource) : getCertificateSigningRequestRequester(resource)
      if (!value) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={value}>
          <span className="text-sm text-theme-text-secondary truncate block">{value}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
