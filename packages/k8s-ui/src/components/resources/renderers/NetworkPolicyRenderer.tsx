import { Shield, ArrowDownToLine, ArrowUpFromLine } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface NetworkPolicyRendererProps {
  data: any
}

export function NetworkPolicyRenderer({ data }: NetworkPolicyRendererProps) {
  const spec = data.spec || {}
  const podSelector = spec.podSelector || {}
  const policyTypes: string[] = spec.policyTypes || []
  const ingress: any[] | undefined = spec.ingress
  const egress: any[] | undefined = spec.egress

  const matchLabels = podSelector.matchLabels || {}
  const hasMatchLabels = Object.keys(matchLabels).length > 0
  const hasIngress = policyTypes.includes('Ingress')
  const hasEgress = policyTypes.includes('Egress')

  return (
    <>
      <Section title="Target" icon={Shield}>
        <PropertyList>
          <Property
            label="Pod Selector"
            value={hasMatchLabels ? undefined : 'All pods in namespace'}
          />
        </PropertyList>
        {hasMatchLabels && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Pod Selector</div>
            <div className="flex flex-wrap gap-1">
              {Object.entries(matchLabels).map(([k, v]) => (
                <span
                  key={k}
                  className="badge bg-theme-elevated text-theme-text-secondary"
                >
                  {k}={String(v)}
                </span>
              ))}
            </div>
          </div>
        )}
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
        <div className="text-xs text-theme-text-tertiary">Allow all</div>
      )}
    </div>
  )
}

function PeerEntry({ peer }: { peer: any }) {
  if (peer.podSelector) {
    const labels = peer.podSelector.matchLabels || {}
    const hasLabels = Object.keys(labels).length > 0
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">podSelector: </span>
        {hasLabels ? (
          <span className="inline-flex flex-wrap gap-1 align-middle">
            {Object.entries(labels).map(([k, v]) => (
              <span
                key={k}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {k}={String(v)}
              </span>
            ))}
          </span>
        ) : (
          <span className="text-xs text-theme-text-tertiary">all pods</span>
        )}
      </div>
    )
  }

  if (peer.namespaceSelector) {
    const labels = peer.namespaceSelector.matchLabels || {}
    const hasLabels = Object.keys(labels).length > 0
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">namespaceSelector: </span>
        {hasLabels ? (
          <span className="inline-flex flex-wrap gap-1 align-middle">
            {Object.entries(labels).map(([k, v]) => (
              <span
                key={k}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {k}={String(v)}
              </span>
            ))}
          </span>
        ) : (
          <span className="text-xs text-theme-text-tertiary">all namespaces</span>
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
