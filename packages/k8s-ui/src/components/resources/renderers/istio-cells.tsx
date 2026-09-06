// Istio cell components for ResourcesView table

import { clsx } from 'clsx'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import {
  getVirtualServiceStatus,
  getVirtualServiceHosts,
  getVirtualServiceGateways,
  getVirtualServiceRouteCount,
  getDestinationRuleStatus,
  getDestinationRuleHost,
  getDestinationRuleSubsetCount,
  getDestinationRuleLoadBalancer,
  getDestinationRuleTlsMode,
  getIstioGatewayStatus,
  getIstioGatewayServerCount,
  getIstioGatewaySelectorString,
  getServiceEntryStatus,
  getServiceEntryHosts,
  getServiceEntryLocation,
  getServiceEntryPortsString,
  getPeerAuthenticationStatus,
  getPeerAuthenticationMode,
  getPeerAuthenticationSelectorString,
  getAuthorizationPolicyStatus,
  getAuthorizationPolicyAction,
  getAuthorizationPolicyRuleCount,
  getAuthorizationPolicySelectorString,
} from '../resource-utils-istio'

const modeSeverity: Record<string, BadgeSeverity> = {
  STRICT: 'success',
  PERMISSIVE: 'warning',
  DISABLE: 'error',
}

export function VirtualServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getVirtualServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'hosts': {
      const hosts = getVirtualServiceHosts(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{hosts}</span>
    }
    case 'gateways': {
      const gateways = getVirtualServiceGateways(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{gateways}</span>
    }
    case 'routes': {
      const count = getVirtualServiceRouteCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function DestinationRuleCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getDestinationRuleStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'host': {
      const host = getDestinationRuleHost(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{host}</span>
    }
    case 'subsets': {
      const count = getDestinationRuleSubsetCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    case 'loadBalancer': {
      const lb = getDestinationRuleLoadBalancer(resource)
      return <span className="text-sm text-theme-text-secondary">{lb}</span>
    }
    case 'tlsMode': {
      // Every mode reads neutral: this is the rule's declaration, not the
      // posture in force. What a DISABLE produces depends on the server's
      // PeerAuthentication — unencrypted traffic, or failed requests.
      const mode = getDestinationRuleTlsMode(resource)
      return <span className="text-sm text-theme-text-secondary">{mode}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function IstioGatewayCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getIstioGatewayStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'servers': {
      const count = getIstioGatewayServerCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'selector': {
      const selector = getIstioGatewaySelectorString(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{selector}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ServiceEntryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getServiceEntryStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'hosts': {
      const hosts = getServiceEntryHosts(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{hosts}</span>
    }
    case 'location': {
      const location = getServiceEntryLocation(resource)
      return (
        <Badge tone={location === 'MESH_EXTERNAL' ? 'accent2' : 'accent1'}>
          {location === 'MESH_EXTERNAL' ? 'External' : 'Internal'}
        </Badge>
      )
    }
    case 'ports': {
      const ports = getServiceEntryPortsString(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{ports}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function PeerAuthenticationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPeerAuthenticationStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'mode': {
      const mode = getPeerAuthenticationMode(resource)
      return (
        <Badge severity={modeSeverity[mode] ?? 'neutral'}>
          {mode}
        </Badge>
      )
    }
    case 'selector': {
      const selector = getPeerAuthenticationSelectorString(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{selector}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function AuthorizationPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getAuthorizationPolicyStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'action': {
      const action = getAuthorizationPolicyAction(resource)
      // Uniformly neutral: an action is a declaration, not a verdict. A green
      // ALLOW would make a deny-all policy look healthiest, and a red DENY
      // would make a control doing its job look failed.
      return (
        <Badge severity="neutral">
          {action}
        </Badge>
      )
    }
    case 'rules': {
      const count = getAuthorizationPolicyRuleCount(resource)
      // Rules are alternatives, so an ALLOW with none of them matches nothing —
      // Istio's deny-all idiom. This is the default-visible cell that carries
      // the warning; the Status column is off by default.
      const allowsNothing = count === 0 && (resource.spec?.action || 'ALLOW') === 'ALLOW'
      // Amber on the count itself: it fits the column and reads at a glance
      // against grey neighbours. The explanation rides on the badge so it does
      // not depend on finding the column header's tooltip.
      if (allowsNothing) {
        return (
          <Tooltip content="Rules are alternatives, so this ALLOW policy matches nothing and permits no traffic on its own. Other ALLOW policies selecting the same workload may still permit requests.">
            <Badge severity="warning">0</Badge>
          </Tooltip>
        )
      }
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'selector': {
      const scope = getAuthorizationPolicySelectorString(resource)
      return (
        <Tooltip content={scope}>
          <span className="text-sm text-theme-text-secondary truncate block">{scope}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
