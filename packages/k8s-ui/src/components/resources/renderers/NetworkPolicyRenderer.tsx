import { Shield, ArrowDownToLine, ArrowUpFromLine, GitFork } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, LabelSelectorDisplay } from '../../ui/drawer-components'
import { NetworkPolicyDiagram } from './NetworkPolicyDiagram'
import { effectivePolicyTypeNames, effectivePolicyTypes, formatNetworkPolicyPort } from '../../../utils/network-policy'

interface NetworkPolicyRendererProps {
  data: any
  staged?: boolean
}

export function NetworkPolicyRenderer({ data, staged = false }: NetworkPolicyRendererProps) {
  const spec = data.spec || {}
  const podSelector = spec.podSelector || {}
  const ingress: any[] | undefined = spec.ingress
  const egress: any[] | undefined = spec.egress

  const types = effectivePolicyTypes(spec)
  const policyTypes = effectivePolicyTypeNames(spec)
  const hasIngress = types.ingress
  const hasEgress = types.egress

  const hasDiagramContent = hasIngress || hasEgress

  return (
    <>
      {hasDiagramContent && (
        <Section title="Policy Flow" icon={GitFork} defaultExpanded>
          <NetworkPolicyDiagram spec={spec} staged={staged} />
        </Section>
      )}

      <Section title="Target" icon={Shield}>
        <div className="mt-2">
          <div className="text-xs text-theme-text-tertiary mb-1">Pod Selector</div>
          <LabelSelectorDisplay selector={podSelector} emptyText="All pods in namespace" />
        </div>
        <div className="mt-2">
          <div className="text-xs text-theme-text-tertiary mb-1">Policy Types</div>
          <div className="flex flex-wrap items-center gap-1">
            {policyTypes.map((type) => (
              <span
                key={type}
                className={clsx(
                  'badge',
                  type === 'Ingress'
                    ? 'status-blue'
                    : 'status-purple'
                )}
              >
                {type}
              </span>
            ))}
            {!types.explicit && (
              <span className="text-xs text-theme-text-tertiary">
                by default — policyTypes not set
              </span>
            )}
          </div>
        </div>
      </Section>

      {hasIngress && (
        <Section title="Ingress Rules" icon={ArrowDownToLine} defaultExpanded>
          {ingress && ingress.length > 0 ? (
            <div className="space-y-3">
              {ingress.map((rule: any, i: number) => (
                <IngressEgressRuleCard key={i} rule={rule} direction="from" />
              ))}
            </div>
          ) : (
            <div className="text-sm text-red-400">Deny all ingress</div>
          )}
        </Section>
      )}

      {hasEgress && (
        <Section title="Egress Rules" icon={ArrowUpFromLine} defaultExpanded>
          {egress && egress.length > 0 ? (
            <div className="space-y-3">
              {egress.map((rule: any, i: number) => (
                <IngressEgressRuleCard key={i} rule={rule} direction="to" />
              ))}
            </div>
          ) : (
            <div className="text-sm text-red-400">Deny all egress</div>
          )}
        </Section>
      )}
    </>
  )
}

function IngressEgressRuleCard({
  rule,
  direction,
}: {
  rule: any
  direction: 'from' | 'to'
}) {
  const peers: any[] = rule[direction] || []
  const ports: any[] = rule.ports || []

  return (
    <div className="card-inner-lg">
      {peers.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1 capitalize">{direction}</div>
          <div className="space-y-1.5">
            {peers.map((peer: any, j: number) => (
              <PeerEntry key={j} peer={peer} />
            ))}
          </div>
        </div>
      )}

      {ports.length > 0 && (
        <div>
          <div className="text-xs text-theme-text-tertiary mb-1">Ports</div>
          <div className="flex flex-wrap gap-1">
            {ports.map((port: any, j: number) => (
              <span
                key={j}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {formatNetworkPolicyPort(port)}
              </span>
            ))}
          </div>
        </div>
      )}

      {peers.length === 0 && ports.length === 0 && (
        <div className="text-xs text-theme-text-tertiary">
          {direction === 'from' ? 'All sources' : 'All destinations'}
        </div>
      )}
    </div>
  )
}

function PeerEntry({ peer }: { peer: any }) {
  // podSelector and namespaceSelector on one peer are ANDed: the peer is the
  // pods matching the first inside the namespaces matching the second. Both
  // have to be shown, or a narrow cross-namespace grant reads as a broad
  // same-namespace one.
  const hasPod = peer.podSelector !== undefined
  const hasNs = peer.namespaceSelector !== undefined
  if (hasPod || hasNs) {
    return (
      <div className="text-sm space-y-0.5">
        {hasPod && (
          <div>
            <span className="text-theme-text-secondary text-xs">podSelector: </span>
            <LabelSelectorDisplay selector={peer.podSelector} emptyText="all pods" inline />
          </div>
        )}
        {hasNs && (
          <div>
            <span className="text-theme-text-secondary text-xs">
              {hasPod ? 'in namespaceSelector: ' : 'namespaceSelector: '}
            </span>
            <LabelSelectorDisplay selector={peer.namespaceSelector} emptyText="all namespaces" inline />
          </div>
        )}
      </div>
    )
  }

  if (peer.ipBlock) {
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">ipBlock: </span>
        <span className="text-xs text-theme-text-primary">{peer.ipBlock.cidr}</span>
        {peer.ipBlock.except && peer.ipBlock.except.length > 0 && (
          <span className="text-xs text-theme-text-tertiary">
            {' '}except {peer.ipBlock.except.join(', ')}
          </span>
        )}
      </div>
    )
  }

  return null
}
