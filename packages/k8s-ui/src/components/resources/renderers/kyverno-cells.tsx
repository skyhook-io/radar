// Kyverno / Policy Report cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getPolicyReportScope,
  getPolicyReportStatus,
  getPolicyReportSummary,
  getKyvernoPolicyStatus,
  getKyvernoEnforcement,
  getKyvernoPolicyRuleCount,
} from '../resource-utils-kyverno'
import {
  getKyvernoRequestState,
  getKyvernoRequestType,
  getKyvernoRequestPolicy,
  getKyvernoRequestTriggers,
  getKyvernoReportStatus,
  getKyvernoReportSubject,
  getKyvernoReportSource,
  getKyvernoReportSummary,
} from '../resource-utils-kyverno-queue'

export function PolicyReportCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPolicyReportStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    // Kyverno names a per-resource report after the subject's UID, so the Name
    // column reads as a bare UUID. Without the subject the whole table is
    // unidentifiable rows with counts beside them — you cannot tell which of
    // your workloads a failing report belongs to without opening every one.
    case 'scope': {
      const scope = getPolicyReportScope(resource)
      return <span className="truncate text-theme-text-primary" title={scope}>{scope}</span>
    }
    case 'pass': {
      const summary = getPolicyReportSummary(resource)
      return <span className={clsx('text-sm', summary.pass > 0 ? 'text-green-400' : 'text-theme-text-tertiary')}>{summary.pass}</span>
    }
    case 'fail': {
      const summary = getPolicyReportSummary(resource)
      return <span className={clsx('text-sm', summary.fail > 0 ? 'text-red-400 font-medium' : 'text-theme-text-tertiary')}>{summary.fail}</span>
    }
    case 'warn': {
      const summary = getPolicyReportSummary(resource)
      return <span className={clsx('text-sm', summary.warn > 0 ? 'text-yellow-400' : 'text-theme-text-tertiary')}>{summary.warn}</span>
    }
    case 'error': {
      const summary = getPolicyReportSummary(resource)
      return <span className={clsx('text-sm', summary.error > 0 ? 'text-red-400 font-medium' : 'text-theme-text-tertiary')}>{summary.error}</span>
    }
    case 'skip': {
      const summary = getPolicyReportSummary(resource)
      return <span className={clsx('text-sm', summary.skip > 0 ? 'text-blue-400' : 'text-theme-text-tertiary')}>{summary.skip}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterPolicyReportCell({ resource, column }: { resource: any; column: string }) {
  // Same rendering logic as PolicyReport
  return <PolicyReportCell resource={resource} column={column} />
}

export function KyvernoPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKyvernoPolicyStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'action': {
      const { label, blocks, discrepancy } = getKyvernoEnforcement(resource)
      return (
        <span className={clsx(
          'badge',
          blocks
            ? 'bg-red-500/20 text-red-400'
            : discrepancy
              ? 'bg-orange-500/20 text-orange-400'
              : 'bg-yellow-500/20 text-yellow-400',
        )}>
          {label}
        </span>
      )
    }
    case 'rules': {
      const count = getKyvernoPolicyRuleCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterPolicyCell({ resource, column }: { resource: any; column: string }) {
  // Same rendering logic as KyvernoPolicy
  return <KyvernoPolicyCell resource={resource} column={column} />
}

// ============================================================================
// UpdateRequest / EphemeralReport
// ============================================================================

export function KyvernoUpdateRequestCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const state = getKyvernoRequestState(resource)
      return <span className={clsx('badge', state.color)}>{state.text}</span>
    }
    case 'type': {
      const t = getKyvernoRequestType(resource)
      return <span className="text-sm text-theme-text-secondary">{t === 'unknown' ? '-' : t}</span>
    }
    case 'policy':
      return (
        <span className="text-sm text-theme-text-secondary truncate block" title={getKyvernoRequestPolicy(resource)}>
          {getKyvernoRequestPolicy(resource) || '-'}
        </span>
      )
    case 'triggers': {
      // A generate request carries every trigger it was queued for, so the row
      // has to say "and N more" rather than pretend it is about one resource.
      const triggers = getKyvernoRequestTriggers(resource)
      if (triggers.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const first = triggers[0]
      const label = `${first.kind ?? ''}/${first.name ?? ''}`
      return (
        <span className="text-sm text-theme-text-secondary truncate block" title={label}>
          {triggers.length === 1 ? label : `${label} +${triggers.length - 1}`}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function KyvernoEphemeralReportCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKyvernoReportStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'subject': {
      const s = getKyvernoReportSubject(resource)
      if (!s?.kind) return <span className="text-sm text-theme-text-tertiary">-</span>
      const label = s.name ? `${s.kind}/${s.name}` : `${s.kind} (deleted)`
      return (
        <span className="text-sm text-theme-text-secondary truncate block" title={label}>
          {label}
        </span>
      )
    }
    case 'source':
      return (
        <span className="text-sm text-theme-text-secondary truncate block">
          {(getKyvernoReportSource(resource) || '-').replace(/-/g, ' ')}
        </span>
      )
    case 'results':
      return (
        <span className="text-sm text-theme-text-secondary">
          {String(getKyvernoReportSummary(resource).total)}
        </span>
      )
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
