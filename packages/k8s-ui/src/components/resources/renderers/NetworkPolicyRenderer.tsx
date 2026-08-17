import { Shield, ArrowDownToLine, ArrowUpFromLine, GitFork } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, LabelSelectorDisplay } from '../../ui/drawer-components'
import { NetworkPolicyDiagram } from './NetworkPolicyDiagram'

interface NetworkPolicyRendererProps {
  data: any
  staged?: boolean
}

export function NetworkPolicyRenderer({ data, staged = false }: NetworkPolicyRendererProps) {
  const spec = data.spec || {}
  const podSelector = spec.podSelector || {}
  const policyTypes: string[] = spec.policyTypes || []
  const ingress: any[] | undefined = spec.ingress
  const egress: any[] | undefined = spec.egress

  const hasIngress = policyTypes.includes('Ingress')
  const hasEgress = policyTypes.includes('Egress')

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
        {policyTypes.length > 0 && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Policy Types</div>
            <div className="flex flex-wrap gap-1">
              {policyTypes.map((type) => (
                <span
                  key={type}
                  className={clsx(
                    'badge',
                    type === 'Ingress'
                      ? 'bg-blue-500/20 text-blue-400 border-blue-500/30'
                      : 'bg-purple-500/20 text-purple-400 border-purple-500/30'
                  )}
                >
                  {type}
                </span>
              ))}
            </div>
          </div>
        )}
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
                {port.protocol || 'TCP'}/{port.port}
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
  if (peer.podSelector) {
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">podSelector: </span>
        <LabelSelectorDisplay selector={peer.podSelector} emptyText="all pods" inline />
      </div>
    )
  }

  if (peer.namespaceSelector) {
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">namespaceSelector: </span>
        <LabelSelectorDisplay selector={peer.namespaceSelector} emptyText="all namespaces" inline />
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
