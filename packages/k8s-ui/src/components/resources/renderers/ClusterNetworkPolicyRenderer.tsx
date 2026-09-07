import { Shield, ArrowDownToLine, ArrowUpFromLine } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, LabelSelectorDisplay } from '../../ui/drawer-components'

interface ClusterNetworkPolicyRendererProps {
  data: any
}

export function ClusterNetworkPolicyRenderer({ data }: ClusterNetworkPolicyRendererProps) {
  const spec = data.spec || {}
  const subject = spec.subject || {}
  const priority = spec.priority
  const ingress: any[] | undefined = spec.ingress
  const egress: any[] | undefined = spec.egress

  return (
    <>
      <Section title="Subject" icon={Shield}>
        <PropertyList>
          {priority !== undefined && (
            <Property label="Priority" value={String(priority)} />
          )}
        </PropertyList>
        {subject.namespaces && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Namespace Selector</div>
            <LabelSelectorDisplay selector={subject.namespaces} />
          </div>
        )}
        {subject.pods && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Pod Selector</div>
            {subject.pods.namespaceSelector && (
              <div className="mb-1">
                <span className="text-xs text-theme-text-secondary">namespaces: </span>
                <LabelSelectorDisplay selector={subject.pods.namespaceSelector} inline />
              </div>
            )}
            {subject.pods.podSelector && (
              <div>
                <span className="text-xs text-theme-text-secondary">pods: </span>
                <LabelSelectorDisplay selector={subject.pods.podSelector} inline />
              </div>
            )}
          </div>
        )}
      </Section>

      {ingress && ingress.length > 0 && (
        <Section title={`Ingress Rules (${ingress.length})`} icon={ArrowDownToLine} defaultExpanded>
          <div className="space-y-3">
            {ingress.map((rule: any, i: number) => (
              <AdminRuleCard key={i} rule={rule} direction="from" />
            ))}
          </div>
        </Section>
      )}

      {egress && egress.length > 0 && (
        <Section title={`Egress Rules (${egress.length})`} icon={ArrowUpFromLine} defaultExpanded>
          <div className="space-y-3">
            {egress.map((rule: any, i: number) => (
              <AdminRuleCard key={i} rule={rule} direction="to" />
            ))}
          </div>
        </Section>
      )}
    </>
  )
}

function AdminRuleCard({ rule, direction }: { rule: any; direction: 'from' | 'to' }) {
  const action = rule.action || 'Allow'
  const name = rule.name || ''
  const peers: any[] = rule[direction] || []
  const ports: any[] = rule.ports || []

  const actionColor = action === 'Deny'
    ? 'status-red'
    : action === 'Pass'
      ? 'status-amber'
      : 'status-green'

  return (
    <div className="card-inner-lg">
      <div className="flex items-center gap-2 mb-2">
        <span className={clsx('badge', actionColor)}>{action}</span>
        {name && <span className="text-xs text-theme-text-secondary">{name}</span>}
      </div>

      {peers.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1 capitalize">{direction}</div>
          <div className="space-y-1.5">
            {peers.map((peer: any, j: number) => (
              <AdminPeerEntry key={j} peer={peer} />
            ))}
          </div>
        </div>
      )}

      {ports.length > 0 && (
        <div>
          <div className="text-xs text-theme-text-tertiary mb-1">Ports</div>
          <div className="flex flex-wrap gap-1">
            {ports.map((port: any, j: number) => (
              <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">
                {port.portNumber?.protocol || 'TCP'}/{port.portNumber?.port || port.portRange?.start || '*'}
                {port.portRange && `-${port.portRange.end}`}
              </span>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function AdminPeerEntry({ peer }: { peer: any }) {
  if (peer.namespaces) {
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">namespaces: </span>
        <LabelSelectorDisplay selector={peer.namespaces} inline />
      </div>
    )
  }
  if (peer.pods) {
    return (
      <div className="text-sm space-y-0.5">
        {peer.pods.namespaceSelector && (
          <div>
            <span className="text-theme-text-secondary text-xs">namespaces: </span>
            <LabelSelectorDisplay selector={peer.pods.namespaceSelector} inline />
          </div>
        )}
        {peer.pods.podSelector && (
          <div>
            <span className="text-theme-text-secondary text-xs">pods: </span>
            <LabelSelectorDisplay selector={peer.pods.podSelector} inline />
          </div>
        )}
      </div>
    )
  }
  if (peer.networks) {
    return (
      <div className="text-sm">
        <span className="text-theme-text-secondary text-xs">networks: </span>
        <span className="inline-flex flex-wrap gap-1">
          {peer.networks.map((n: string, i: number) => (
            <span key={i} className="badge bg-theme-elevated text-theme-text-secondary">{n}</span>
          ))}
        </span>
      </div>
    )
  }
  return null
}

