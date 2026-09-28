import { useState } from 'react'
import { TrafficFilterSidebar } from '@skyhook-io/k8s-ui'

const frame = { height: 900, display: 'flex' } as const

const NAMESPACES = [
  { name: 'shop', nodeCount: 9 },
  { name: 'payments', nodeCount: 3 },
  { name: 'ingress-nginx', nodeCount: 1 },
  { name: 'observability', nodeCount: 6 },
  { name: 'kube-system', nodeCount: 11 },
]

function toggler(set: Set<string>, value: string) {
  const next = new Set(set)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  return next
}

function Stateful({
  l7,
  rate,
  hidden = [],
  initial = {},
}: {
  l7?: boolean
  rate?: boolean
  hidden?: string[]
  initial?: Partial<{ hideSystem: boolean; showNamespaceGroups: boolean; minConnections: number; l7Protocol: string; methods: string[]; status: string[] }>
}) {
  const [hideSystem, setHideSystem] = useState(initial.hideSystem ?? true)
  const [hideExternal, setHideExternal] = useState(false)
  const [minConnections, setMinConnections] = useState(initial.minConnections ?? 0)
  const [showNamespaceGroups, setShowNamespaceGroups] = useState(initial.showNamespaceGroups ?? false)
  const [collapseInternet, setCollapseInternet] = useState(true)
  const [addonMode, setAddonMode] = useState<'group' | 'show' | 'hide'>('group')
  const [aggregateExternal, setAggregateExternal] = useState(true)
  const [detectServices, setDetectServices] = useState(true)
  const [timeRange, setTimeRange] = useState('5m')
  const [l7Protocol, setL7Protocol] = useState(initial.l7Protocol ?? 'all')
  const [l7Methods, setL7Methods] = useState(new Set(initial.methods ?? []))
  const [l7StatusRanges, setL7StatusRanges] = useState(new Set(initial.status ?? []))
  const [l7Verdicts, setL7Verdicts] = useState(new Set<string>())
  const [dnsPattern, setDnsPattern] = useState('')
  const [hiddenNamespaces, setHidden] = useState(new Set(hidden))

  return (
    <div style={frame}>
      <TrafficFilterSidebar
        hideSystem={hideSystem}
        setHideSystem={setHideSystem}
        hideExternal={hideExternal}
        setHideExternal={setHideExternal}
        minConnections={minConnections}
        setMinConnections={setMinConnections}
        showNamespaceGroups={showNamespaceGroups}
        setShowNamespaceGroups={setShowNamespaceGroups}
        collapseInternet={collapseInternet}
        setCollapseInternet={setCollapseInternet}
        addonMode={addonMode}
        setAddonMode={setAddonMode}
        aggregateExternal={aggregateExternal}
        setAggregateExternal={setAggregateExternal}
        detectServices={detectServices}
        setDetectServices={setDetectServices}
        timeRange={timeRange}
        setTimeRange={setTimeRange}
        showL7Filters={l7}
        availableStatusRanges={l7 ? ['2xx', '3xx', '4xx', '5xx'] : undefined}
        availableVerdicts={l7 ? ['forwarded', 'dropped'] : undefined}
        hasDNSQueries={l7}
        availableHTTPMethods={l7 ? ['GET', 'POST', 'PUT', 'DELETE'] : undefined}
        isRateBased={rate}
        l7Protocol={l7Protocol}
        setL7Protocol={setL7Protocol}
        l7Methods={l7Methods}
        onToggleL7Method={(m) => setL7Methods((s) => toggler(s, m))}
        l7StatusRanges={l7StatusRanges}
        onToggleL7StatusRange={(r) => setL7StatusRanges((s) => toggler(s, r))}
        l7Verdicts={l7Verdicts}
        onToggleL7Verdict={(v) => setL7Verdicts((s) => toggler(s, v))}
        dnsPattern={dnsPattern}
        setDnsPattern={setDnsPattern}
        namespaces={NAMESPACES}
        hiddenNamespaces={hiddenNamespaces}
        onToggleNamespace={(ns) => setHidden((s) => toggler(s, ns))}
      />
    </div>
  )
}

export function HubbleWithL7() {
  return <Stateful l7 initial={{ l7Protocol: 'HTTP', methods: ['POST'], status: ['5xx'] }} />
}

export function ConnectionsOnly() {
  return <Stateful hidden={['kube-system']} initial={{ minConnections: 100, showNamespaceGroups: true }} />
}

export function IstioRateBased() {
  return <Stateful l7 rate initial={{ hideSystem: false }} />
}
