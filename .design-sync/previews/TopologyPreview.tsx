import { TopologyPreview } from '@skyhook-io/k8s-ui'

const frame = { width: 360, display: 'flex', flexDirection: 'column' } as const
const noop = () => {}

type Health = 'healthy' | 'degraded' | 'unhealthy' | 'unknown'

function buildTopology(spec: Array<[string, number, Health[]?]>, edgeCount: number) {
  const nodes: { id: string; kind: any; name: string; status: Health; data: Record<string, unknown> }[] = []
  for (const [kind, count, statuses = []] of spec) {
    for (let i = 0; i < count; i++) {
      nodes.push({
        id: `${kind.toLowerCase()}/shop/${kind.toLowerCase()}-${i}`,
        kind,
        name: `${kind.toLowerCase()}-${i}`,
        status: statuses[i] ?? 'healthy',
        data: { namespace: 'shop' },
      })
    }
  }
  const edges = Array.from({ length: edgeCount }, (_, i) => ({
    id: `e${i}`,
    source: nodes[i % nodes.length].id,
    target: nodes[(i + 1) % nodes.length].id,
    type: 'manages' as any,
  }))
  return { nodes, edges }
}

export function HealthyCluster() {
  return (
    <div style={frame}>
      <TopologyPreview
        namespaceSelected={false}
        onNavigate={noop}
        topology={buildTopology(
          [['Deployment', 14], ['StatefulSet', 3], ['Service', 17], ['Ingress', 4], ['Pod', 41], ['ConfigMap', 22], ['Secret', 9]],
          96,
        )}
      />
    </div>
  )
}

export function WithUnhealthyWorkloads() {
  return (
    <div style={frame}>
      <TopologyPreview
        namespaceSelected
        onNavigate={noop}
        topology={buildTopology(
          [
            ['Deployment', 6, ['healthy', 'degraded', 'healthy', 'unhealthy']],
            ['Service', 7],
            ['Gateway', 1],
            ['HTTPRoute', 5],
            ['Pod', 18, ['healthy', 'unhealthy', 'unhealthy', 'degraded']],
            ['Job', 2],
          ],
          38,
        )}
      />
    </div>
  )
}

export function LargeClusterNeedsNamespace() {
  return (
    <div style={frame}>
      <TopologyPreview
        namespaceSelected={false}
        onNavigate={noop}
        topology={{ nodes: [], edges: [], requiresNamespaceFilter: true, estimatedNodes: 18420 }}
      />
    </div>
  )
}

export function WaitingForFirstFrame() {
  return (
    <div style={frame}>
      <TopologyPreview namespaceSelected={false} onNavigate={noop} topology={null as any} />
    </div>
  )
}
