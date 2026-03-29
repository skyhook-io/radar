// Trivy Operator cell components for ResourcesView table — extracted from ResourcesView.tsx

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getVulnerabilityReportSummary,
  getVulnerabilityReportContainer,
  getVulnerabilityReportImage,
  getConfigAuditReportSummary,
  getConfigAuditReportStatus,
  getExposedSecretReportSummary,
  getExposedSecretReportContainer,
  getExposedSecretReportImage,
  getRbacAssessmentReportSummary,
  getRbacAssessmentReportStatus,
  getClusterComplianceReportStatus,
  getSbomReportStatus,
  getSbomReportContainer,
} from '../resource-utils'

// Shared severity count cell
export function TrivySeverityCell({ summary, column }: { summary: { critical: number; high: number; medium: number; low: number }; column: string }) {
  switch (column) {
    case 'critical':
      return <span className={clsx('text-sm font-medium', summary.critical > 0 ? 'text-red-400' : 'text-theme-text-tertiary')}>{summary.critical}</span>
    case 'high':
      return <span className={clsx('text-sm font-medium', summary.high > 0 ? 'text-orange-400' : 'text-theme-text-tertiary')}>{summary.high}</span>
    case 'medium':
      return <span className={clsx('text-sm font-medium', summary.medium > 0 ? 'text-yellow-400' : 'text-theme-text-tertiary')}>{summary.medium}</span>
    case 'low':
      return <span className={clsx('text-sm font-medium', summary.low > 0 ? 'text-blue-400' : 'text-theme-text-tertiary')}>{summary.low}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function VulnerabilityReportCell({ resource, column }: { resource: any; column: string }) {
  const summary = getVulnerabilityReportSummary(resource)
  if (['critical', 'high', 'medium', 'low'].includes(column)) return <TrivySeverityCell summary={summary} column={column} />
  switch (column) {
    case 'container':
      return <span className="text-sm text-theme-text-secondary">{getVulnerabilityReportContainer(resource)}</span>
    case 'image': {
      const image = getVulnerabilityReportImage(resource)
      return (
        <Tooltip content={image}>
          <span className="text-sm text-theme-text-secondary truncate block">{image}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ConfigAuditReportCell({ resource, column }: { resource: any; column: string }) {
  const summary = getConfigAuditReportSummary(resource)
  if (['critical', 'high', 'medium', 'low'].includes(column)) return <TrivySeverityCell summary={summary} column={column} />
  switch (column) {
    case 'status': {
      const status = getConfigAuditReportStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ExposedSecretReportCell({ resource, column }: { resource: any; column: string }) {
  const summary = getExposedSecretReportSummary(resource)
  if (['critical', 'high', 'medium', 'low'].includes(column)) return <TrivySeverityCell summary={summary} column={column} />
  switch (column) {
    case 'container':
      return <span className="text-sm text-theme-text-secondary">{getExposedSecretReportContainer(resource)}</span>
    case 'image': {
      const image = getExposedSecretReportImage(resource)
      return (
        <Tooltip content={image}>
          <span className="text-sm text-theme-text-secondary truncate block">{image}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function RbacAssessmentReportCell({ resource, column }: { resource: any; column: string }) {
  const summary = getRbacAssessmentReportSummary(resource)
  if (['critical', 'high', 'medium', 'low'].includes(column)) return <TrivySeverityCell summary={summary} column={column} />
  switch (column) {
    case 'status': {
      const status = getRbacAssessmentReportStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterComplianceReportCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'title':
      return <span className="text-sm text-theme-text-secondary truncate block">{resource.spec?.compliance?.title || '-'}</span>
    case 'status': {
      const status = getClusterComplianceReportStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'pass': {
      const count = resource.status?.summary?.passCount ?? null
      return <span className={clsx('text-sm font-medium', count != null && count > 0 ? 'text-green-400' : 'text-theme-text-tertiary')}>{count != null ? count : '-'}</span>
    }
    case 'fail': {
      const count = resource.status?.summary?.failCount ?? null
      return <span className={clsx('text-sm font-medium', count != null && count > 0 ? 'text-red-400' : 'text-theme-text-tertiary')}>{count != null ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function SbomReportCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'container':
      return <span className="text-sm text-theme-text-secondary">{getSbomReportContainer(resource)}</span>
    case 'components': {
      const count = resource.report?.summary?.componentsCount ?? null
      return <span className="text-sm text-theme-text-secondary">{count != null ? count : '-'}</span>
    }
    case 'status': {
      const status = getSbomReportStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
