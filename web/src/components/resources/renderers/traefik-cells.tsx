// Traefik cell components for ResourcesView table

import { Shield } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'
import {
  getIngressRouteEntryPoints,
  getIngressRouteHosts,
  getIngressRouteRoutesSummary,
  hasIngressRouteTLS,
  getIngressRouteMiddlewareCount,
  getMiddlewareType,
  getTraefikServiceType,
  getTraefikServiceTargets,
  getServersTransportInsecure,
  getServersTransportServerName,
  getTLSOptionMinVersion,
} from '../resource-utils-traefik'

export function IngressRouteCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'entrypoints': {
      const eps = getIngressRouteEntryPoints(resource)
      return <span className="text-sm text-theme-text-secondary">{eps}</span>
    }
    case 'hosts': {
      const hosts = getIngressRouteHosts(resource)
      return (
        <Tooltip content={hosts}>
          <span className="text-sm text-theme-text-secondary truncate">{hosts}</span>
        </Tooltip>
      )
    }
    case 'routes': {
      const summary = getIngressRouteRoutesSummary(resource)
      return (
        <Tooltip content={summary}>
          <span className="text-sm text-theme-text-secondary truncate">{summary}</span>
        </Tooltip>
      )
    }
    case 'tls': {
      const hasTLS = hasIngressRouteTLS(resource)
      return hasTLS ? (
        <Tooltip content="TLS Enabled">
          <span>
            <Shield className="w-4 h-4 text-green-400" />
          </span>
        </Tooltip>
      ) : (
        <span className="text-sm text-theme-text-tertiary">-</span>
      )
    }
    case 'middlewares': {
      const count = getIngressRouteMiddlewareCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function MiddlewareCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'type': {
      const type = getMiddlewareType(resource)
      return <span className="text-sm text-theme-text-secondary">{type}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function TraefikServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'type': {
      const type = getTraefikServiceType(resource)
      return <span className="text-sm text-theme-text-secondary">{type}</span>
    }
    case 'targets': {
      const targets = getTraefikServiceTargets(resource)
      return (
        <Tooltip content={targets}>
          <span className="text-sm text-theme-text-secondary truncate">{targets}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ServersTransportCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'insecure': {
      const insecure = getServersTransportInsecure(resource)
      return (
        <span className={`text-sm ${insecure ? 'text-yellow-400' : 'text-theme-text-secondary'}`}>
          {insecure ? 'Yes' : 'No'}
        </span>
      )
    }
    case 'serverName': {
      const name = getServersTransportServerName(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{name || '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function TLSOptionCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'minVersion': {
      const v = getTLSOptionMinVersion(resource)
      return <span className="text-sm text-theme-text-secondary">{v || '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
