// Contour cell components for ResourcesView table

import { Shield } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'
import {
  getHTTPProxyFQDN,
  getHTTPProxyRouteCount,
  getHTTPProxyIncludeCount,
  hasHTTPProxyTLS,
  getHTTPProxyStatus,
} from '../resource-utils-contour'

export function HTTPProxyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'fqdn': {
      const fqdn = getHTTPProxyFQDN(resource)
      return <span className="text-sm truncate" title={fqdn}>{fqdn}</span>
    }
    case 'routes':
      return <span className="text-sm">{getHTTPProxyRouteCount(resource) || '-'}</span>
    case 'includes': {
      const count = getHTTPProxyIncludeCount(resource)
      return <span className="text-sm">{count > 0 ? count : '-'}</span>
    }
    case 'tls': {
      const hasTLS = hasHTTPProxyTLS(resource)
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
    case 'status': {
      const { label } = getHTTPProxyStatus(resource)
      const color = label === 'Valid' ? 'text-green-500'
        : label === 'Invalid' ? 'text-red-500'
        : label === 'Orphaned' ? 'text-yellow-500'
        : 'text-theme-text-secondary'
      return <span className={`text-sm ${color}`}>{label}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
