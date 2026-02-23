// Prometheus Operator cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getServiceMonitorStatus,
  getServiceMonitorEndpointCount,
  getServiceMonitorJobLabel,
  getServiceMonitorSelector,
  getPrometheusRuleStatus,
  getPrometheusRuleGroupCount,
  getPrometheusRuleTotalRules,
  getPodMonitorStatus,
  getPodMonitorEndpointCount,
  getPodMonitorSelector,
} from '../resource-utils-prometheus'

export function ServiceMonitorCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getServiceMonitorStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'endpoints':
      return <span className="text-sm text-theme-text-secondary">{getServiceMonitorEndpointCount(resource)}</span>
    case 'jobLabel':
      return <span className="text-sm text-theme-text-secondary truncate block">{getServiceMonitorJobLabel(resource)}</span>
    case 'selector':
      return <span className="text-sm text-theme-text-secondary truncate block">{getServiceMonitorSelector(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function PrometheusRuleCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPrometheusRuleStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'groups':
      return <span className="text-sm text-theme-text-secondary">{getPrometheusRuleGroupCount(resource)}</span>
    case 'rules':
      return <span className="text-sm text-theme-text-secondary">{getPrometheusRuleTotalRules(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function PodMonitorCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPodMonitorStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'endpoints':
      return <span className="text-sm text-theme-text-secondary">{getPodMonitorEndpointCount(resource)}</span>
    case 'selector':
      return <span className="text-sm text-theme-text-secondary truncate block">{getPodMonitorSelector(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
