import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'

interface NetworkPolicyDiagramProps {
  spec: any
}

/**
 * Visual flow diagram for a NetworkPolicy.
 * Three-column layout: Sources -> Target -> Destinations
 * Each rule is a horizontal band. Peers within a rule are OR'd (stacked).
 * Pure CSS/SVG — no ReactFlow needed for this static diagram.
 */
export function NetworkPolicyDiagram({ spec }: NetworkPolicyDiagramProps) {
  const podSelector = spec.podSelector || {}
  const matchLabels = podSelector.matchLabels || {}
  const policyTypes: string[] = spec.policyTypes || []
  const ingress: any[] | undefined = spec.ingress
  const egress: any[] | undefined = spec.egress

  const hasIngress = policyTypes.includes('Ingress')
  const hasEgress = policyTypes.includes('Egress')
  const targetLabel = Object.keys(matchLabels).length > 0
    ? Object.entries(matchLabels).map(([k, v]) => `${k}=${v}`).join(', ')
    : 'All pods'

  const ingressDenied = hasIngress && (!ingress || ingress.length === 0)
  const egressDenied = hasEgress && (!egress || egress.length === 0)

  return (
    <div className="card-inner-lg space-y-3">
      {/* Ingress flows */}
      {hasIngress && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full bg-blue-500" />
            <span className="text-[10px] font-semibold uppercase tracking-wider text-blue-400">Ingress</span>
          </div>
          {ingressDenied ? (
            <FlowRow
              sources={[{ label: 'All traffic', type: 'deny' as const }]}
              target={targetLabel}
              ports={[]}
              direction="ingress"
              denied
            />
          ) : (
            ingress?.map((rule, i) => (
              <FlowRow
                key={i}
                sources={extractPeers(rule.from)}
                target={targetLabel}
                ports={extractPorts(rule.ports)}
                direction="ingress"
              />
            ))
          )}
        </div>
      )}

      {/* Egress flows */}
      {hasEgress && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full bg-purple-500" />
            <span className="text-[10px] font-semibold uppercase tracking-wider text-purple-400">Egress</span>
          </div>
          {egressDenied ? (
            <FlowRow
              sources={[{ label: 'All traffic', type: 'deny' as const }]}
              target={targetLabel}
              ports={[]}
              direction="egress"
              denied
            />
          ) : (
            egress?.map((rule, i) => (
              <FlowRow
                key={i}
                sources={extractPeers(rule.to)}
                target={targetLabel}
                ports={extractPorts(rule.ports)}
                direction="egress"
              />
            ))
          )}
        </div>
      )}

      {!hasIngress && !hasEgress && (
        <div className="text-xs text-theme-text-tertiary text-center py-2">No policy types specified</div>
      )}
    </div>
  )
}

interface PeerInfo {
  label: string
  sublabel?: string
  type: 'pod' | 'namespace' | 'cidr' | 'all' | 'deny' | 'combined'
}

function extractPeers(peers: any[] | undefined): PeerInfo[] {
  if (!peers || peers.length === 0) {
    return [{ label: 'Any source', type: 'all' }]
  }
  return peers.map(peer => {
    const hasPodSel = peer.podSelector !== undefined
    const hasNsSel = peer.namespaceSelector !== undefined
    const hasIpBlock = peer.ipBlock !== undefined

    if (hasPodSel && hasNsSel) {
      // AND: both must match
      const podLabels = formatSelector(peer.podSelector)
      const nsLabels = formatSelector(peer.namespaceSelector)
      return {
        label: podLabels,
        sublabel: `in ${nsLabels === 'all' ? 'any namespace' : nsLabels}`,
        type: 'combined' as const,
      }
    }
    if (hasPodSel) {
      return { label: formatSelector(peer.podSelector), type: 'pod' as const }
    }
    if (hasNsSel) {
      const nsLabels = formatSelector(peer.namespaceSelector)
      return { label: nsLabels === 'all' ? 'All namespaces' : nsLabels, type: 'namespace' as const }
    }
    if (hasIpBlock) {
      const cidr = peer.ipBlock.cidr || '?'
      const except = peer.ipBlock.except
      return {
        label: cidr,
        sublabel: except?.length ? `except ${except.join(', ')}` : undefined,
        type: 'cidr' as const,
      }
    }
    return { label: '?', type: 'all' as const }
  })
}

function extractPorts(ports: any[] | undefined): string[] {
  if (!ports || ports.length === 0) return []
  return ports.map((p: any) => {
    const proto = p.protocol || 'TCP'
    const port = p.port || '*'
    const endPort = p.endPort
    return endPort ? `${proto}/${port}-${endPort}` : `${proto}/${port}`
  })
}

function formatSelector(selector: any): string {
  const labels = selector?.matchLabels || {}
  if (Object.keys(labels).length === 0) return 'all'
  return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
}

// Color coding for peer types
const PEER_STYLES = {
  pod: 'border-emerald-500/30 bg-emerald-500/8',
  namespace: 'border-sky-500/30 bg-sky-500/8',
  cidr: 'border-amber-500/30 bg-amber-500/8',
  combined: 'border-emerald-500/30 bg-emerald-500/8',
  all: 'border-theme-border bg-theme-elevated/50',
  deny: 'border-red-500/30 bg-red-500/8',
} as const

const PEER_DOT = {
  pod: 'bg-emerald-500',
  namespace: 'bg-sky-500',
  cidr: 'bg-amber-500',
  combined: 'bg-emerald-500',
  all: 'bg-theme-text-tertiary',
  deny: 'bg-red-500',
} as const

function FlowRow({
  sources,
  target,
  ports,
  direction,
  denied = false,
}: {
  sources: PeerInfo[]
  target: string
  ports: string[]
  direction: 'ingress' | 'egress'
  denied?: boolean
}) {
  const isIngress = direction === 'ingress'

  const sourceNodes = (
    <div className="min-w-0 max-w-full shrink space-y-1">
      {sources.map((peer, i) => (
        <div key={i}>
          {i > 0 && (
            <div className="text-[9px] text-theme-text-tertiary uppercase tracking-widest text-center my-0.5">or</div>
          )}
          <Tooltip content={peer.sublabel ? `${peer.label}\n${peer.sublabel}` : peer.label} position="top">
            <div
              className={clsx(
                'rounded-md border px-2 py-1.5 min-w-0 overflow-hidden',
                PEER_STYLES[peer.type],
              )}
            >
              <div className="flex items-center gap-1.5 min-w-0">
                <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', PEER_DOT[peer.type])} />
                <span className={clsx('text-[11px] font-medium truncate min-w-0', denied && 'line-through text-red-400')}>
                  {peer.label}
                </span>
              </div>
              {peer.sublabel && (
                <div className="text-[10px] text-theme-text-tertiary ml-3 truncate">{peer.sublabel}</div>
              )}
            </div>
          </Tooltip>
        </div>
      ))}
    </div>
  )

  const targetNode = (
    <div className="min-w-0 max-w-full shrink-[1000]">
      <Tooltip content={target} position="top">
        <div className="rounded-md border border-indigo-500/30 bg-indigo-500/8 px-2 py-1.5 min-w-0 max-w-full overflow-hidden">
          <div className="flex items-center gap-1.5 min-w-0">
            <span className="w-1.5 h-1.5 rounded-full shrink-0 bg-indigo-500" />
            <span className="text-[11px] font-medium truncate min-w-0">{target}</span>
          </div>
        </div>
      </Tooltip>
    </div>
  )

  const arrow = (
    <div className="flex flex-col items-center justify-center shrink-0 w-10">
      {/* Arrow line with optional port label */}
      <svg width="40" height="20" viewBox="0 0 40 20" className="overflow-visible">
        <defs>
          <marker id={`arrow-${direction}-${denied ? 'denied' : 'ok'}`} markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
            <path d="M0,0 L6,3 L0,6" fill="none" stroke={denied ? '#ef4444' : direction === 'ingress' ? '#3b82f6' : '#a855f7'} strokeWidth="1.5" />
          </marker>
        </defs>
        <line
          x1="2" y1="10" x2="32" y2="10"
          stroke={denied ? '#ef4444' : direction === 'ingress' ? '#3b82f6' : '#a855f7'}
          strokeWidth="1.5"
          strokeDasharray={denied ? '3 2' : undefined}
          markerEnd={`url(#arrow-${direction}-${denied ? 'denied' : 'ok'})`}
        />
      </svg>
      {ports.length > 0 && (
        <div className="flex flex-wrap justify-center gap-0.5 mt-0.5">
          {ports.map((p, i) => (
            <span key={i} className="text-[8px] text-theme-text-tertiary font-mono leading-none">{p}</span>
          ))}
        </div>
      )}
    </div>
  )

  return (
    <div className="flex items-center gap-1">
      {isIngress ? (
        <>
          {sourceNodes}
          {arrow}
          {targetNode}
        </>
      ) : (
        <>
          {targetNode}
          {arrow}
          {sourceNodes}
        </>
      )}
    </div>
  )
}
