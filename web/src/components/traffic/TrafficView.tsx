import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import { useTrafficSources, useTrafficFlows, useTrafficRecords, useTrafficConnect, useSetTrafficSource, type TrafficEndpointPair } from '../../api/traffic'
import { useClusterInfo, useNamespaceScope } from '../../api/client'
import { useSearchParams } from 'react-router-dom'
import type { TrafficWizardState, AggregatedFlow, TrafficFlow } from '../../types'
import { TrafficWizard } from './TrafficWizard'
import { TrafficGraph, type TrafficGraphSelection } from './TrafficGraph'
import { TrafficFilterSidebar } from './TrafficFilterSidebar'
import { TrafficFlowListProvider } from './TrafficFlowListContext'
import { Loader2, Filter, Plug, ChevronDown, List, Activity, AlertTriangle, Clock, Crosshair, X } from 'lucide-react'
import { TrafficFocusSearch } from './TrafficFocusSearch'
import { TrafficGraphTooLarge } from './TrafficGraphTooLarge'
import { clsx } from 'clsx'
import { useQueryClient } from '@tanstack/react-query'
import { useDock } from '../dock'
import { AlertBanner, EmptyState, PaneLoader, FreshnessControl } from '@skyhook-io/k8s-ui'
import { useConnection } from '../../context/ConnectionContext'
import { Tooltip } from '../ui/Tooltip'
import { matchesStatusRanges, bucketsFromCounts, bucketsFromStatus, isRateBasedSource, keepAvailable, effectiveThreshold, volumeUnit, type VolumeUnit, isExternalKind, mergeFlowVolume, coverageLabel, type GraphFlow, endpointPair, graphEndpoint, graphEndpointId, mergeRawPairs, pairKey, selectionRawPairs, selectionMatch, graphSize, GRAPH_DRAW_BUDGET, GRAPH_DRAW_CEILING, parseFocus, focusParam, focusId, focusNeighborhood, touchesFocus, endpointSummaries, namespaceSummaries, type TrafficFocus } from './trafficFilters'

// Consecutive 2s retries of an empty result that came with a transient warning.
const MAX_EMPTY_RETRIES = 5

// Addon types for filtering
export type AddonMode = 'show' | 'group' | 'hide'

// Cluster addons that can be grouped/hidden (infrastructure, not traffic-flow)
const CLUSTER_ADDON_NAMESPACES = new Set([
  // Certificate management
  'cert-manager',
  // Secrets management
  'external-secrets',
  'sealed-secrets',
  'vault',
  // Backup
  'velero',
  // Monitoring & metrics
  'gmp-system',
  'gmp-public',
  'datadog',
  'monitoring',
  'observability',
  'opencost',
  'prometheus',
  'grafana',
  'kube-state-metrics',
  // Logging
  'loki',
  'logging',
  'fluentd',
  'fluentbit',
  // DNS
  'external-dns',
  // Autoscaling
  'cluster-autoscaler',
  'karpenter',
  'keda',
  // GitOps & CI/CD
  'argocd',
  'argo-rollouts',
  'argo-workflows',
  'flux-system',
  // Policy
  'gatekeeper-system',
  // Config management
  'reloader',
  // Database operators
  'cloud-native-pg',
  'cnpg-system',
  'postgres-operator',
  'mysql-operator',
  'redis-operator',
])

// Addon workload names (for detection when namespace isn't enough)
const CLUSTER_ADDON_NAMES = new Set([
  'coredns',
  'metrics-server',
  'cluster-autoscaler',
  'kube-dns',
  'kube-state-metrics',
  'reloader',
])

// Traffic-flow related addons that should NEVER be grouped/hidden
// These are essential for understanding traffic patterns
const TRAFFIC_FLOW_NAMESPACES = new Set([
  'ingress-nginx',
  'nginx-ingress',
  'traefik',
  'contour',
  'kong',
  'ambassador',
  'emissary',
  'haproxy-ingress',
  'istio-system',
  'istio-ingress',
  'linkerd',
  'consul',
  'envoy-gateway-system',
  'gateway-system',
])

const TRAFFIC_FLOW_NAMES = new Set([
  'ingress-nginx-controller',
  'nginx-ingress-controller',
  'traefik',
  'contour',
  'envoy',
  'kong',
  'ambassador',
  'istio-ingressgateway',
  'istio-proxy',
  'linkerd-proxy',
])

// Check if an endpoint is a cluster addon (can be grouped/hidden)
// Exported for use in TrafficGraph
export function isClusterAddon(name: string, namespace: string | undefined): boolean {
  // Never treat traffic-flow addons as regular addons
  if (namespace && TRAFFIC_FLOW_NAMESPACES.has(namespace)) return false
  if (TRAFFIC_FLOW_NAMES.has(name)) return false

  // Check namespace-based addons
  if (namespace && CLUSTER_ADDON_NAMESPACES.has(namespace)) return true

  // Check name-based addons
  if (CLUSTER_ADDON_NAMES.has(name)) return true

  // Check for common addon naming patterns
  if (name.includes('prometheus') || name.includes('grafana') ||
      name.includes('datadog') || name.includes('fluentd') ||
      name.includes('metrics-server') || name.includes('coredns')) {
    return true
  }

  return false
}

// System namespaces to hide by default
const SYSTEM_NAMESPACES = new Set([
  'kube-system',
  'kube-public',
  'kube-node-lease',
  'cert-manager',
  'caretta',
  'cilium',
  'calico-system',
  'tigera-operator',
  'gatekeeper-system',
  'argo-rollouts',
  'argocd',
  'flux-system',
  'monitoring',
  'observability',
  'istio-system',
  'linkerd',
  // Phase 1.1: Additional infrastructure namespaces
  'node',           // Node-level traffic (often 35%+ of flows)
  'gmp-system',     // GKE Managed Prometheus
  'gmp-public',     // GKE Managed Prometheus public
  'datadog',        // Datadog monitoring
  'opencost',       // OpenCost
  'external-dns',   // External DNS controller
  'ingress-nginx',  // NGINX Ingress Controller
  'traefik',        // Traefik
  'velero',         // Velero backup
  'vault',          // HashiCorp Vault
  'external-secrets', // External Secrets Operator
])

// Sent with each query while system traffic is hidden, so the source drops it
// before it counts against the source's per-node limits.
const SYSTEM_NAMESPACE_LIST: readonly string[] = [...SYSTEM_NAMESPACES]

// Detect internal load balancer IPs (appear as "external" but are internal)
function isInternalLoadBalancer(name: string): boolean {
  // GKE internal LB IPs (10.x.x.x range)
  if (/^10\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(name)) return true
  // AWS internal LB pattern (172.16-31.x.x)
  if (/^172\.(1[6-9]|2[0-9]|3[0-1])\.\d{1,3}\.\d{1,3}$/.test(name)) return true
  // Azure internal LB pattern
  if (/^192\.168\.\d{1,3}\.\d{1,3}$/.test(name)) return true
  return false
}

// Patterns for external service aggregation (Phase 4.2)
const EXTERNAL_SERVICE_PATTERNS: { pattern: RegExp; display: string; category: string }[] = [
  { pattern: /.*\.mongodb\.net\.?$/, display: 'MongoDB Atlas', category: 'database' },
  { pattern: /.*\.mongodb\.com\.?$/, display: 'MongoDB Atlas', category: 'database' },
  { pattern: /.*\.redis\.cloud\.?$/, display: 'Redis Cloud', category: 'database' },
  { pattern: /.*\.rds\.amazonaws\.com\.?$/, display: 'AWS RDS', category: 'database' },
  { pattern: /.*\.amazonaws\.com\.?$/, display: 'AWS Services', category: 'cloud' },
  { pattern: /.*\.googleapis\.com\.?$/, display: 'Google APIs', category: 'cloud' },
  // GCE VM patterns - various formats (IP.bc.googleusercontent.com, with/without trailing dot)
  { pattern: /[\d.-]+\.bc\.googleusercontent\.com\.?$/i, display: 'GCE VMs', category: 'cloud' },
  { pattern: /.*\.googleusercontent\.com\.?$/i, display: 'Google Cloud', category: 'cloud' },
  { pattern: /.*\.azure\.com\.?$/, display: 'Azure Services', category: 'cloud' },
  { pattern: /.*\.blob\.core\.windows\.net\.?$/, display: 'Azure Blob', category: 'cloud' },
  { pattern: /.*\.sentry\.io\.?$/, display: 'Sentry', category: 'monitoring' },
  { pattern: /.*\.datadoghq\.com\.?$/, display: 'Datadog', category: 'monitoring' },
  { pattern: /.*\.stripe\.com\.?$/, display: 'Stripe', category: 'payment' },
  { pattern: /.*\.auth0\.com\.?$/, display: 'Auth0', category: 'auth' },
  { pattern: /.*\.okta\.com\.?$/, display: 'Okta', category: 'auth' },
  { pattern: /.*\.sendgrid\.net\.?$/, display: 'SendGrid', category: 'email' },
  { pattern: /.*\.mailgun\.org\.?$/, display: 'Mailgun', category: 'email' },
  { pattern: /.*\.slack\.com\.?$/, display: 'Slack', category: 'messaging' },
  { pattern: /.*\.twilio\.com\.?$/, display: 'Twilio', category: 'messaging' },
]

// Port-based service detection (when hostname doesn't give enough info)
const PORT_SERVICE_MAP: Record<number, { name: string; category: string }> = {
  27017: { name: 'MongoDB', category: 'database' },
  27018: { name: 'MongoDB', category: 'database' },
  5432: { name: 'PostgreSQL', category: 'database' },
  3306: { name: 'MySQL', category: 'database' },
  6379: { name: 'Redis', category: 'database' },
  9042: { name: 'Cassandra', category: 'database' },
  9200: { name: 'Elasticsearch', category: 'database' },
  9300: { name: 'Elasticsearch', category: 'database' },
  443: { name: 'HTTPS', category: 'web' },
  80: { name: 'HTTP', category: 'web' },
  8080: { name: 'HTTP', category: 'web' },
  8443: { name: 'HTTPS', category: 'web' },
  5672: { name: 'RabbitMQ', category: 'messaging' },
  9092: { name: 'Kafka', category: 'messaging' },
  4222: { name: 'NATS', category: 'messaging' },
  11211: { name: 'Memcached', category: 'cache' },
  25: { name: 'SMTP', category: 'email' },
  587: { name: 'SMTP', category: 'email' },
  53: { name: 'DNS', category: 'infra' },
  22: { name: 'SSH', category: 'infra' },
}

// Get aggregated display name for external services (considers both hostname and port)
function getExternalServiceName(name: string, port?: number): { name: string; aggregated: boolean; category?: string } {
  // Check for port-based service first (more reliable than hostname guessing)
  const portService = port ? PORT_SERVICE_MAP[port] : undefined

  // Try hostname patterns
  for (const { pattern, display, category } of EXTERNAL_SERVICE_PATTERNS) {
    if (pattern.test(name)) {
      // If we also have port info, combine them for clarity (e.g., "MongoDB (GCE VMs)")
      if (portService && display !== portService.name) {
        return { name: `${portService.name} (${display})`, aggregated: true, category: portService.category }
      }
      return { name: display, aggregated: true, category }
    }
  }

  // If hostname doesn't match but we have a known port, aggregate by service type
  if (portService) {
    return { name: portService.name, aggregated: true, category: portService.category }
  }

  return { name, aggregated: false }
}


// Cilium reserved identities (internal infrastructure traffic)
const CILIUM_RESERVED_IDENTITIES = new Set([
  'host',       // Node-level traffic
  'health',     // Cilium health probes
  'init',       // Initialization identity
  'unmanaged',  // Unmanaged endpoints
])

// Check if an address is IPv6 link-local or multicast (infrastructure noise)
function isIPv6Infrastructure(name: string): boolean {
  // Link-local (fe80::/10)
  if (name.toLowerCase().startsWith('fe80:')) return true
  // Multicast (ff00::/8) - includes ff02::2 (all routers), ff02::1 (all nodes), etc.
  if (name.toLowerCase().startsWith('ff0')) return true
  return false
}

// Check if an endpoint is a system/infrastructure component
function isSystemEndpoint(name: string, namespace: string | undefined, kind: string): boolean {
  // System namespaces
  if (namespace && SYSTEM_NAMESPACES.has(namespace)) {
    return true
  }

  // Cilium reserved identities: nodes and the host network arrive as Host;
  // health, init and unmanaged endpoints carry no usable identity.
  if (kind === 'Host') {
    return true
  }
  if ((kind === 'External' || kind === 'Unknown') && CILIUM_RESERVED_IDENTITIES.has(name)) {
    return true
  }

  // IPv6 link-local and multicast addresses (infrastructure noise)
  if (isIPv6Infrastructure(name)) {
    return true
  }

  // Node-level traffic
  if (kind === 'node' || kind === 'Node') {
    return true
  }

  // Cloud metadata services (AWS, GCE, Azure)
  if (name.startsWith('169.254.') || name === 'instance-data.ec2.internal') {
    return true
  }
  if (name === 'metadata.google.internal' || name === 'metadata.google.internal.') {
    return true
  }
  if (name === 'metadata.azure.com' || name.endsWith('.metadata.azure.com')) {
    return true
  }

  // Localhost / loopback traffic (within-pod communication, health checks)
  if (name === '127.0.0.1' || name === 'localhost' || name.startsWith('127.')) {
    return true
  }

  // 0.0.0.0 - binding address, not a real destination
  if (name === '0.0.0.0') {
    return true
  }

  // Kubernetes API server in default namespace
  if (namespace === 'default' && name === 'kubernetes') {
    return true
  }

  // IP-based names (internal cluster IPs)
  if (/^\d{1,3}-\d{1,3}-\d{1,3}-\d{1,3}\./.test(name)) {
    return true
  }

  // EC2 instance hostnames
  if (/^ec2-\d+-\d+-\d+-\d+\./.test(name) || /^ip-\d+-\d+-\d+-\d+\./.test(name)) {
    return true
  }

  // Internal load balancer IPs that appear as "external"
  if (kind === 'External' && isInternalLoadBalancer(name)) {
    return true
  }

  return false
}

const isExternal = isExternalKind


// Why the map covers less than the chosen window. Two limits can cut it: each
// node returns at most nodeFlowLimit flows, and on a large cluster Radar keeps
// at most flowLimit in total. Narrowing what is fetched is the way to see more.
function coverageExplanation(data: { nodeFlowLimit?: number; flowLimit?: number } | undefined, timeRange: string, hideSystem: boolean): string {
  const narrow = hideSystem
    ? 'Selecting fewer namespaces fetches less traffic, so the map covers more of the window.'
    : 'Hiding system traffic or selecting fewer namespaces fetches less traffic, so the map covers more of the window.'
  if (data?.flowLimit) {
    return `This cluster produced more flows in the last ${timeRange} than Radar keeps per refresh (${data.flowLimit.toLocaleString()}), so the map shows the newest ones. ${narrow}`
  }
  return `Radar reads at most ${(data?.nodeFlowLimit ?? 0).toLocaleString()} of the newest flows from each node. At least one node reached that limit before the start of the ${timeRange} window, so its earlier traffic is not included. Other nodes may still show older flows. ${narrow}`
}

interface TrafficViewProps {
  namespaces: string[]
  /** Sets the app-wide namespace selection. */
  onSetNamespaces?: (namespaces: string[]) => void
}

export function TrafficView({ namespaces, onSetNamespaces }: TrafficViewProps) {
  const { connection } = useConnection()
  const [wizardState, setWizardState] = useState<TrafficWizardState>('detecting')
  const [timeRange, setTimeRange] = useState<string>('5m')
  const [hideSystem, setHideSystem] = useState(true)
  const [hideExternal, setHideExternal] = useState(false)
  const [minConnections, setMinConnections] = useState(0)
  // The unit the threshold above was picked under. Stored because the number alone
  // is ambiguous: 100 is a valid step in both scales and means something different
  // in each.
  const [minConnectionsUnit, setMinConnectionsUnit] = useState<VolumeUnit>('connections')
  const [showNamespaceGroups, setShowNamespaceGroups] = useState(true)
  const [aggregateExternal, setAggregateExternal] = useState(true)
  const [detectServices, setDetectServices] = useState(true)
  const [collapseInternet, setCollapseInternet] = useState(true)
  const [groupByWorkload, setGroupByWorkload] = useState(true)
  // A record's endpoint as the graph draws it in the chosen grouping.
  const drawnEndpoint = useCallback(
    (e: TrafficFlow['source']) => (groupByWorkload ? graphEndpoint(e) : e),
    [groupByWorkload])
  const [addonMode, setAddonMode] = useState<AddonMode>('show')
  const [graphSelection, setGraphSelection] = useState<TrafficGraphSelection | null>(null)
  const clearGraphSelection = useCallback(() => setGraphSelection(null), [])
  // The view the graph was last drawn for, and so keeps drawing (up to the
  // ceiling) as refreshes move its size: set by drawing it or by Draw anyway.
  const [drawnViewKey, setDrawnViewKey] = useState<string | null>(null)
  const dock = useDock()
  const graphPaneRef = useRef<HTMLDivElement>(null)

  // Focus lives in the URL so Back leaves it and a link can carry it.
  const [searchParams, setSearchParams] = useSearchParams()
  const focusValue = searchParams.get('focus')
  const focus = useMemo(() => parseFocus(focusValue), [focusValue])
  const focusKey = focus ? focusId(focus) : undefined
  const setFocus = useCallback((next: TrafficFocus | null) => {
    setSearchParams(prev => {
      const params = new URLSearchParams(prev)
      if (next) params.set('focus', focusParam(next))
      else params.delete('focus')
      return params
    })
  }, [setSearchParams])

  // Dock: offset past sidebar, close flows tab on unmount
  const flowsTabIdRef = useRef<string | null>(null)
  useEffect(() => {
    dock.setLeftOffset(288)
    return () => {
      dock.setLeftOffset(0)
      // Close the flows tab when leaving traffic view
      if (flowsTabIdRef.current) {
        dock.removeTab(flowsTabIdRef.current)
        flowsTabIdRef.current = null
      }
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const [chosenHiddenNamespaces, setHiddenNamespaces] = useState<Set<string>>(new Set())
  // Paused while focused: a neighbor in a hidden namespace is still a neighbor.
  const hiddenNamespaces = useMemo(() => (focus ? new Set<string>() : chosenHiddenNamespaces), [focus, chosenHiddenNamespaces])
  // L7 filters (Hubble-only)
  const [l7Protocol, setL7Protocol] = useState<string>('all')
  const [l7Methods, setL7Methods] = useState<Set<string>>(new Set())
  const [l7StatusRanges, setL7StatusRanges] = useState<Set<string>>(new Set())
  const [l7Verdicts, setL7Verdicts] = useState<Set<string>>(new Set())
  const [dnsPattern, setDnsPattern] = useState('')
  const [isConnecting, setIsConnecting] = useState(false)
  const [connectionError, setConnectionError] = useState<string | null>(null)
  const queryClient = useQueryClient()
  const connectMutation = useTrafficConnect()
  const setSourceMutation = useSetTrafficSource()
  const hasAutoConnectedRef = useRef(false)
  const [sourcePickerOpen, setSourcePickerOpen] = useState(false)
  const sourcePickerRef = useRef<HTMLDivElement>(null)

  // Track cluster context to reset state on cluster change
  const { data: clusterInfo } = useClusterInfo()
  const lastClusterRef = useRef<string | null>(null)

  // Reset state when cluster context changes
  useEffect(() => {
    const currentCluster = clusterInfo?.context || null
    if (lastClusterRef.current !== null && lastClusterRef.current !== currentCluster) {
      // Cluster changed - reset wizard state and invalidate traffic queries
      setWizardState('detecting')
      setConnectionError(null)
      hasAutoConnectedRef.current = false
      queryClient.invalidateQueries({ queryKey: ['traffic-sources'] })
      queryClient.invalidateQueries({ queryKey: ['traffic-flows'] })
      queryClient.invalidateQueries({ queryKey: ['traffic-connection'] })
    }
    lastClusterRef.current = currentCluster
  }, [clusterInfo?.context, queryClient])

  // Close source picker on outside click (capture phase to beat ReactFlow)
  useEffect(() => {
    if (!sourcePickerOpen) return
    const handler = (e: MouseEvent) => {
      if (sourcePickerRef.current && !sourcePickerRef.current.contains(e.target as Node)) {
        setSourcePickerOpen(false)
      }
    }
    document.addEventListener('mousedown', handler, true)
    return () => document.removeEventListener('mousedown', handler, true)
  }, [sourcePickerOpen])

  const {
    data: sourcesData,
    isLoading: sourcesLoading,
    refetch: refetchSources,
  } = useTrafficSources()

  const {
    data: flowsData,
    isFetching: flowsFetching,
    dataUpdatedAt: flowsUpdatedAt,
    refetch: refetchFlowsRaw,
  } = useTrafficFlows({
    namespaces,
    since: timeRange,
    excludeNamespaces: hideSystem ? SYSTEM_NAMESPACE_LIST : undefined,
    excludeHost: hideSystem,
    byPod: !groupByWorkload,
    // Only fetch flows when connected (not connecting and no connection error)
    enabled: wizardState === 'ready' && !isConnecting && !connectionError,
  })

  // A 'partial' warning is the source telling us this is as complete as it gets —
  // an attribute it does not export, traffic it cannot orient. Retrying returns
  // the same answer, so retrying forever is all cost and no progress.
  const warningIsPermanent = flowsData?.warningKind === 'partial'
  const coverage = coverageLabel(flowsData?.coveredSince, flowsData?.timestamp)
  // An 'incomplete' one is a fetch that worked but could not see everything
  // (events lost, nodes unreachable). Not a failure, so not retried at once.
  const warningIsIncomplete = flowsData?.warningKind === 'incomplete'

  // Auto-retry when flows return with warning but no data (e.g., port-forward not
  // ready yet). Bounded: a warning that keeps coming back — a node the relay
  // cannot reach, a query that keeps failing — is the answer rather than a hiccup,
  // and polling it every 2s forever costs the source a burst of queries each time.
  const emptyRetriesRef = useRef(0)
  // A different question gets its own retries.
  useEffect(() => {
    emptyRetriesRef.current = 0
  }, [namespaces, timeRange])
  useEffect(() => {
    const empty = !flowsData?.aggregated || flowsData.aggregated.length === 0
    if (!flowsData?.warning || !empty) {
      emptyRetriesRef.current = 0
      return
    }
    if (!warningIsPermanent && !warningIsIncomplete && !flowsFetching && emptyRetriesRef.current < MAX_EMPTY_RETRIES) {
      const timer = setTimeout(() => {
        emptyRetriesRef.current += 1
        refetchFlowsRaw()
      }, 2000)
      return () => clearTimeout(timer)
    }
  }, [flowsData, warningIsPermanent, warningIsIncomplete, flowsFetching, refetchFlowsRaw])

  // Filter flows based on user preferences
  // Note: namespace filtering is done server-side via the global namespace selector
  // Show L7 filters only when flows actually contain L7 data
  const hasL7Data = useMemo(() => {
    if (!flowsData?.aggregated) return false
    return flowsData.aggregated.some(f => f.l7Protocol || f.topHTTPPaths || f.topDNSQueries)
  }, [flowsData?.aggregated])

  const isRateBased = isRateBasedSource(sourcesData?.active)

  // Which L7 filters can return something, so the sidebar never offers a control
  // that matches nothing. Sources differ in what they can report: a rate-based one
  // measures a 5xx rate but no status distribution, and has no DNS query names at
  // all, so offering 2xx or a DNS box would be a dead end.
  const l7Capabilities = useMemo(() => {
    const statuses = new Set<string>()
    const verdicts = new Set<string>()
    let hasDNSQueries = false
    const methods = new Set<string>()
    for (const f of flowsData?.aggregated ?? []) {
      for (const [bucket, count] of Object.entries(f.httpStatusCounts ?? {})) {
        if (count > 0) statuses.add(bucket)
      }
      // An error rate is a 5xx signal even where no status distribution exists.
      if ((f.errorCount ?? 0) > 0) statuses.add('5xx')
      for (const [verdict, count] of Object.entries(f.verdictCounts ?? {})) {
        if (count > 0) verdicts.add(verdict)
      }
      if (f.topDNSQueries?.length) hasDNSQueries = true
      for (const path of f.topHTTPPaths ?? []) {
        if (path.method) methods.add(path.method)
      }
    }
    return {
      availableStatusRanges: ['2xx', '3xx', '4xx', '5xx'].filter(b => statuses.has(b)),
      availableVerdicts: ['forwarded', 'dropped', 'error'].filter(v => verdicts.has(v)),
      hasDNSQueries,
      availableHTTPMethods: ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'].filter(m => methods.has(m)),
    }
  }, [flowsData?.aggregated])

  // The controls are built from what the data contains, so a selection can outlive
  // its button. These are what actually filter: a choice with no control left to
  // clear it must not keep hiding traffic.
  const activeStatusRanges = useMemo(
    () => keepAvailable(l7StatusRanges, l7Capabilities.availableStatusRanges),
    [l7StatusRanges, l7Capabilities.availableStatusRanges])
  const activeMethods = useMemo(
    () => keepAvailable(l7Methods, l7Capabilities.availableHTTPMethods),
    [l7Methods, l7Capabilities.availableHTTPMethods])
  const activeVerdicts = useMemo(
    () => keepAvailable(l7Verdicts, l7Capabilities.availableVerdicts),
    [l7Verdicts, l7Capabilities.availableVerdicts])
  const activeDnsPattern = l7Capabilities.hasDNSQueries ? dnsPattern : ''
  const activeMinConnections = effectiveThreshold(minConnections, minConnectionsUnit, volumeUnit(isRateBased))

  const filteredFlows = useMemo<AggregatedFlow[]>(() => {
    if (!flowsData?.aggregated) return []

    return flowsData.aggregated.filter(flow => {
      const sourceIsSystem = isSystemEndpoint(flow.source.name, flow.source.namespace, flow.source.kind)
      const destIsSystem = isSystemEndpoint(flow.destination.name, flow.destination.namespace, flow.destination.kind)

      // If hiding system, skip flows where EITHER endpoint is a system component
      if (hideSystem && (sourceIsSystem || destIsSystem)) {
        return false
      }

      // Always filter out non-useful traffic (regardless of hideSystem setting)
      const isAlwaysFiltered = (name: string) =>
        // Cloud metadata services
        name === 'metadata.google.internal' ||
        name === 'metadata.google.internal.' ||
        name.startsWith('169.254.') ||
        name === 'instance-data.ec2.internal' ||
        // Loopback / bind addresses - not real traffic
        name === 'localhost' ||
        name === '127.0.0.1' ||
        name.startsWith('127.') ||
        name === '0.0.0.0'

      if (isAlwaysFiltered(flow.source.name) || isAlwaysFiltered(flow.destination.name)) {
        return false
      }

      // If hiding external, skip flows with external endpoints
      if (hideExternal) {
        if (isExternal(flow.source.kind) || isExternal(flow.destination.kind)) {
          return false
        }
      }

      // Addon mode: hide
      if (addonMode === 'hide') {
        const sourceIsAddon = isClusterAddon(flow.source.name, flow.source.namespace)
        const destIsAddon = isClusterAddon(flow.destination.name, flow.destination.namespace)
        if (sourceIsAddon || destIsAddon) {
          return false
        }
      }

      // Connection threshold filter
      if (flow.connections < activeMinConnections) {
        return false
      }

      // Filter by hidden namespaces - hide flow if EITHER endpoint is in a hidden namespace
      if (hiddenNamespaces.size > 0) {
        const sourceNs = flow.source.namespace
        const destNs = flow.destination.namespace
        if (sourceNs && hiddenNamespaces.has(sourceNs)) return false
        if (destNs && hiddenNamespaces.has(destNs)) return false
      }

      // Protocol filter
      if (l7Protocol === 'HTTP' && flow.l7Protocol !== 'HTTP') return false
      if (l7Protocol === 'DNS' && flow.l7Protocol !== 'DNS') return false
      if (l7Protocol === 'TCP' && flow.l7Protocol) return false // TCP = no L7

      // L7 sub-filters (only apply when active)
      if (activeMethods.size > 0) {
        if (!flow.topHTTPPaths?.some(p => activeMethods.has(p.method))) return false
      }
      if (!matchesStatusRanges(activeStatusRanges, bucketsFromCounts(flow.httpStatusCounts), (flow.errorCount ?? 0) > 0)) return false
      if (activeVerdicts.size > 0) {
        if (!flow.verdictCounts || !Array.from(activeVerdicts).some(v => (flow.verdictCounts?.[v] ?? 0) > 0)) return false
      }
      if (activeDnsPattern) {
        const pattern = activeDnsPattern.toLowerCase()
        if (!flow.topDNSQueries?.some(q => q.query.toLowerCase().includes(pattern))) return false
      }

      return true
    })
  }, [flowsData?.aggregated, hideSystem, hideExternal, activeMinConnections, hiddenNamespaces, addonMode, l7Protocol, activeMethods, activeStatusRanges, activeVerdicts, activeDnsPattern])

  // The graph's filters, applied to individual records for the list view
  const rawFlowPasses = useCallback((flow: TrafficFlow) => {
    // Name rules (system components, addons) judge an endpoint by the name the
    // graph draws it under, so a hidden workload's pods leave the list too.
    const sourceName = drawnEndpoint(flow.source).name
    const destName = drawnEndpoint(flow.destination).name
    const sourceIsSystem = isSystemEndpoint(sourceName, flow.source.namespace, flow.source.kind)
    const destIsSystem = isSystemEndpoint(destName, flow.destination.namespace, flow.destination.kind)
    if (hideSystem && (sourceIsSystem || destIsSystem)) return false

    const isAlwaysFiltered = (name: string) =>
      name === 'metadata.google.internal' || name === 'metadata.google.internal.' ||
      name.startsWith('169.254.') || name === 'instance-data.ec2.internal' ||
      name === 'localhost' || name === '127.0.0.1' || name.startsWith('127.') || name === '0.0.0.0'
    if (isAlwaysFiltered(flow.source.name) || isAlwaysFiltered(flow.destination.name)) return false

    if (hideExternal && (isExternal(flow.source.kind) || isExternal(flow.destination.kind))) return false

    if (addonMode === 'hide') {
      if (isClusterAddon(sourceName, flow.source.namespace) || isClusterAddon(destName, flow.destination.namespace)) return false
    }

    if (hiddenNamespaces.size > 0) {
      if (flow.source.namespace && hiddenNamespaces.has(flow.source.namespace)) return false
      if (flow.destination.namespace && hiddenNamespaces.has(flow.destination.namespace)) return false
    }

    // Protocol filter
    if (l7Protocol === 'HTTP' && flow.l7Protocol !== 'HTTP') return false
    if (l7Protocol === 'DNS' && flow.l7Protocol !== 'DNS') return false
    if (l7Protocol === 'TCP' && flow.l7Protocol) return false

    // L7 sub-filters on individual flow fields
    if (activeMethods.size > 0) {
      if (!flow.httpMethod || !activeMethods.has(flow.httpMethod)) return false
    }
    if (!matchesStatusRanges(activeStatusRanges, bucketsFromStatus(flow.httpStatus), (flow.errorRate ?? 0) > 0)) return false
    if (activeVerdicts.size > 0) {
      if (!flow.verdict || !activeVerdicts.has(flow.verdict)) return false
    }
    if (activeDnsPattern) {
      if (!flow.dnsQuery || !flow.dnsQuery.toLowerCase().includes(activeDnsPattern.toLowerCase())) return false
    }

    return true
  }, [hideSystem, hideExternal, hiddenNamespaces, addonMode, l7Protocol, activeMethods, activeStatusRanges, activeVerdicts, activeDnsPattern, drawnEndpoint])

  const filteredRawFlows = useMemo(
    () => (flowsData?.flows ?? []).filter(rawFlowPasses),
    [flowsData?.flows, rawFlowPasses])


  // Open flow list in the bottom dock
  const openFlowListDock = useCallback(() => {
    const id = dock.addTab({ type: 'traffic-flows', title: 'Traffic Flows' })
    flowsTabIdRef.current = id
  }, [dock])

  // Auto-open flows dock when Hubble raw flows are available
  const hasAutoOpenedFlowsRef = useRef(false)
  useEffect(() => {
    if (flowsData?.flows && flowsData.flows.length > 0 && !hasAutoOpenedFlowsRef.current) {
      hasAutoOpenedFlowsRef.current = true
      openFlowListDock()
    }
  }, [flowsData?.flows, openFlowListDock])

  // Records which quantity the chosen threshold refers to, so it can be discarded
  // rather than reinterpreted if the active source starts measuring the other one.
  const chooseMinConnections = useCallback((value: number) => {
    setMinConnections(value)
    setMinConnectionsUnit(volumeUnit(isRateBased))
  }, [isRateBased])

  // Toggle L7 filter helpers
  const toggleL7Method = useCallback((method: string) => {
    setL7Methods(prev => { const next = new Set(prev); if (next.has(method)) next.delete(method); else next.add(method); return next })
  }, [])
  const toggleL7StatusRange = useCallback((range: string) => {
    setL7StatusRanges(prev => { const next = new Set(prev); if (next.has(range)) next.delete(range); else next.add(range); return next })
  }, [])
  const toggleL7Verdict = useCallback((verdict: string) => {
    setL7Verdicts(prev => { const next = new Set(prev); if (next.has(verdict)) next.delete(verdict); else next.add(verdict); return next })
  }, [])

  // Toggle namespace visibility
  const toggleNamespace = useCallback((ns: string) => {
    setHiddenNamespaces(prev => {
      const next = new Set(prev)
      if (next.has(ns)) {
        next.delete(ns)
      } else {
        next.add(ns)
      }
      return next
    })
  }, [])

  // Process flows for external service aggregation (Phase 4.2)
  // Also tracks service categories for coloring external nodes
  const graphInputFlows = useMemo<GraphFlow[]>(
    () => filteredFlows.map(flow => ({ ...flow, rawPairs: [endpointPair(flow)] })),
    [filteredFlows])

  const { processedFlows, serviceCategories } = useMemo<{
    processedFlows: GraphFlow[]
    serviceCategories: Map<string, string>
  }>(() => {
    const categories = new Map<string, string>()

    // Helper to get service info (optionally using port-based detection)
    const getServiceInfo = (name: string, port: number) => {
      return getExternalServiceName(name, detectServices ? port : undefined)
    }

    if (!aggregateExternal) {
      // Even without aggregation, detect service categories for coloring (destinations only)
      graphInputFlows.forEach(flow => {
        if (isExternal(flow.destination.kind)) {
          const info = getServiceInfo(flow.destination.name, flow.port)
          if (info.category) {
            categories.set(flow.destination.name, info.category)
          }
        }
        // Don't apply port-based detection to sources - port tells us the destination service
      })
      return { processedFlows: graphInputFlows, serviceCategories: categories }
    }

    // Aggregate flows to the same external service
    const aggregatedMap = new Map<string, GraphFlow>()

    graphInputFlows.forEach(flow => {
      // Only aggregate destinations based on port/hostname - sources keep their original name
      // Port-based detection (MongoDB:27017) only makes sense for destinations
      const sourceAgg = isExternal(flow.source.kind)
        ? getExternalServiceName(flow.source.name) // No port - hostname patterns only
        : { name: flow.source.name, aggregated: false }
      const destAgg = isExternal(flow.destination.kind)
        ? getServiceInfo(flow.destination.name, flow.port) // Full detection with port
        : { name: flow.destination.name, aggregated: false }

      // Track categories for coloring (destinations only - sources don't get port-based categories)
      if (destAgg.category) categories.set(destAgg.name, destAgg.category)

      // Create a unique key for the aggregated flow (without port since we aggregate by service)
      const sourceKey = flow.source.namespace
        ? `${flow.source.namespace}/${sourceAgg.name}`
        : sourceAgg.name
      const destKey = flow.destination.namespace
        ? `${flow.destination.namespace}/${destAgg.name}`
        : destAgg.name
      // Group by service name, not by port (all MongoDB connections become one edge).
      // Traffic whose direction is known is kept apart from traffic whose direction
      // is not, the same way the backend aggregation keys it: merging them would
      // give one edge a single arrowhead answer that is wrong for half its bytes.
      const key = `${sourceKey}->${destKey}|${flow.directionUnknown ? 'u' : 'o'}`

      const existing = aggregatedMap.get(key)
      if (existing) {
        mergeFlowVolume(existing, flow)
        mergeRawPairs(existing, flow)
        // Everything merged here shares the key's direction-known state, so the
        // flag is already correct on the entry that was created first.
      } else {
        // Create new aggregated flow with modified names
        aggregatedMap.set(key, {
          ...flow,
          rawPairs: [...(flow.rawPairs ?? [])],
          source: sourceAgg.aggregated
            ? { ...flow.source, name: sourceAgg.name }
            : flow.source,
          destination: destAgg.aggregated
            ? { ...flow.destination, name: destAgg.name }
            : flow.destination,
        })
      }
    })

    return { processedFlows: Array.from(aggregatedMap.values()), serviceCategories: categories }
  }, [graphInputFlows, aggregateExternal, detectServices])

  // Collapse inbound internet traffic (external sources → internal destinations)
  const internetCollapsedFlows = useMemo<GraphFlow[]>(() => {
    if (!collapseInternet) return processedFlows

    // Group flows where external sources connect to internal destinations
    const internetFlowsMap = new Map<string, GraphFlow>() // destKey -> aggregated flow
    const nonInternetFlows: GraphFlow[] = []

    processedFlows.forEach(flow => {
      const sourceIsExternal = isExternal(flow.source.kind)
      const destIsInternal = !isExternal(flow.destination.kind)

      // Only collapse external → internal flows (inbound internet traffic)
      if (sourceIsExternal && destIsInternal) {
        // Key on destination + port, and on whether the direction is known: this
        // collapse merges genuinely different external clients, so one of them
        // arriving unoriented must not take the arrowhead off another's traffic.
        const orientation = flow.directionUnknown ? '|u' : '|o'
        const destKey = flow.destination.namespace
          ? `${flow.destination.namespace}/${flow.destination.name}:${flow.port}${orientation}`
          : `${flow.destination.name}:${flow.port}${orientation}`

        const existing = internetFlowsMap.get(destKey)
        if (existing) {
          mergeFlowVolume(existing, flow)
          mergeRawPairs(existing, flow)
        } else {
          // Create new "Internet" → destination flow
          internetFlowsMap.set(destKey, {
            ...flow,
            rawPairs: [...(flow.rawPairs ?? [])],
            source: {
              name: 'Internet',
              namespace: '',
              kind: 'Internet',
            },
          })
        }
      } else {
        nonInternetFlows.push(flow)
      }
    })

    return [...nonInternetFlows, ...Array.from(internetFlowsMap.values())]
  }, [processedFlows, collapseInternet])

  // When grouping addons:
  // 1. Aggregate internet → addon into single edge to group
  // 2. Aggregate addon → kubernetes into single edge from group
  // Everything a focus can be put on: the endpoints drawn before addons are
  // grouped, which are real ones.
  const focusableEndpoints = useMemo(() => endpointSummaries(internetCollapsedFlows), [internetCollapsedFlows])
  const focusedFlows = useMemo(
    () => (focus ? focusNeighborhood(internetCollapsedFlows, focus) : internetCollapsedFlows),
    [internetCollapsedFlows, focus])
  // Entering a focus selects it, so the flow list holds its records; leaving
  // one clears the selection it made. A focus with no traffic selects nothing.
  const focusFound = !!focus && focusedFlows.length > 0
  useEffect(() => {
    setGraphSelection(focusKey && focusFound ? { type: 'node', nodeId: focusKey } : null)
  }, [focusKey, focusFound])

  // A focused view draws its endpoints as they are: grouping the addons would
  // fold the focus or its neighbors into the group.
  const groupAddons = addonMode === 'group' && !focus
  const finalFlows = useMemo<GraphFlow[]>(() => {
    if (!groupAddons) return focusedFlows

    // Track totals for aggregated edges
    let addonInternetTotal = 0
    let addonToK8sTotal = 0
    let addonInternetRate = 0
    let addonToK8sRate = 0
    const addonInternetPairs: TrafficEndpointPair[] = []
    const addonToK8sPairs: TrafficEndpointPair[] = []
    const processedFlows: GraphFlow[] = []

    // Check if destination is the kubernetes API server
    const isKubernetesAPI = (name: string, namespace: string | undefined) => {
      return name === 'kubernetes' && (!namespace || namespace === 'default')
    }

    focusedFlows.forEach(flow => {
      const sourceIsAddon = isClusterAddon(flow.source.name, flow.source.namespace)
      const destIsAddon = isClusterAddon(flow.destination.name, flow.destination.namespace)
      const sourceIsInternet = flow.source.kind === 'Internet'
      const destIsK8sAPI = isKubernetesAPI(flow.destination.name, flow.destination.namespace)

      // Internet → Addon: aggregate into single edge to group
      if (sourceIsInternet && destIsAddon) {
        addonInternetTotal += flow.connections
        addonInternetRate += flow.requestRate ?? 0
        for (const pair of flow.rawPairs ?? []) addonInternetPairs.push(pair)
        processedFlows.push({
          ...flow,
          source: {
            name: 'addon-internet',
            namespace: '',
            kind: 'SkipEdge', // Create addon node but skip individual edge
          },
        })
      }
      // Addon → Kubernetes API: aggregate into single edge from group
      else if (sourceIsAddon && destIsK8sAPI) {
        addonToK8sTotal += flow.connections
        addonToK8sRate += flow.requestRate ?? 0
        for (const pair of flow.rawPairs ?? []) addonToK8sPairs.push(pair)
        processedFlows.push({
          ...flow,
          destination: {
            ...flow.destination,
            kind: 'SkipEdge', // Create kubernetes node but skip individual edge
          },
        })
      }
      else {
        processedFlows.push(flow)
      }
    })

    // Add virtual flow for Internet → Addon Group edge
    if (addonInternetTotal > 0) {
      processedFlows.push({
        source: {
          name: 'addon-internet',
          namespace: '',
          kind: 'AddonInternet',
        },
        destination: {
          name: 'addon-group-target',
          namespace: '',
          kind: 'AddonGroupTarget',
        },
        protocol: 'tcp',
        port: 0,
        connections: addonInternetTotal,
        ...(addonInternetRate > 0 && { requestRate: addonInternetRate }),
        rawPairs: addonInternetPairs,
        bytesSent: 0,
        bytesRecv: 0,
        flowCount: 1,
        lastSeen: new Date().toISOString(),
      })
    }

    // Add virtual flow for Addon Group → Kubernetes edge
    if (addonToK8sTotal > 0) {
      processedFlows.push({
        source: {
          name: 'addon-group-source',
          namespace: '',
          kind: 'AddonGroupSource',
        },
        destination: {
          name: 'kubernetes',
          namespace: 'default',
          kind: 'Service',
        },
        protocol: 'tcp',
        port: 443,
        connections: addonToK8sTotal,
        ...(addonToK8sRate > 0 && { requestRate: addonToK8sRate }),
        rawPairs: addonToK8sPairs,
        bytesSent: 0,
        bytesRecv: 0,
        flowCount: 1,
        lastSeen: new Date().toISOString(),
      })
    }

    return processedFlows
  }, [focusedFlows, groupAddons])

  // The map is laid out on the main thread, so a view too large to draw is
  // listed as a table instead of freezing the tab.
  const drawnGraph = useMemo(() => graphSize(finalFlows), [finalFlows])
  // Everything that makes it a different view. A refresh doesn't, so a view
  // that was drawn keeps being drawn as its size drifts past the budget.
  const viewKey = JSON.stringify([
    sourcesData?.active ?? '', namespaces, timeRange, focusKey ?? '', groupByWorkload, hideSystem, hideExternal, activeMinConnections,
    [...hiddenNamespaces].sort(), addonMode, aggregateExternal, detectServices, collapseInternet,
    l7Protocol, [...activeMethods].sort(), [...activeStatusRanges].sort(), [...activeVerdicts].sort(), activeDnsPattern,
  ])
  const fitsBudget = drawnGraph.score <= GRAPH_DRAW_BUDGET
  const tooLargeToDraw = drawnGraph.score > GRAPH_DRAW_CEILING || (!fitsBudget && drawnViewKey !== viewKey)
  useEffect(() => {
    if (finalFlows.length > 0 && fitsBudget) setDrawnViewKey(viewKey)
  }, [finalFlows.length, fitsBudget, viewKey])

  // The table lists the endpoints as they are, so a selection made from it is
  // traced through the same flows.
  const selectableFlows = tooLargeToDraw ? focusedFlows : finalFlows
  // The server edges behind the selection, as drawn: what its records are
  // narrowed to, whichever form the lookup is sent in.
  const selectionPairs = useMemo(
    () => selectionRawPairs(selectableFlows, graphSelection, groupAddons && !tooLargeToDraw ? e => isClusterAddon(e.name, e.namespace) : undefined),
    [graphSelection, selectableFlows, groupAddons, tooLargeToDraw])
  const selection = useMemo(() => selectionMatch(selectionPairs, graphSelection), [selectionPairs, graphSelection])
  // Ungrouped, a workload focus is drawn as its pods, so no node carries its
  // id; its records are still the workload's.
  const focusSelected = !!focus && graphSelection?.type === 'node' && graphSelection.nodeId === focusKey
  const recordsMatch = useMemo(
    () => {
      if (selection) return selection
      if (!focusSelected || !focus?.namespace) return null
      // The owner kind lets Hubble select the workload's pods by name.
      const pod = focusedFlows.flatMap(f => [f.source, f.destination])
        .find(e => e.namespace === focus.namespace && e.workload === focus.name && e.workloadKind)
      return { endpoints: [{ namespace: focus.namespace, name: focus.name, kind: 'Workload', ...(pod && { workloadKind: pod.workloadKind }) }] }
    },
    [selection, focusSelected, focus, focusedFlows])

  // A selection names something in the representation it was made in: the
  // table lists endpoints as they are, the graph may group addons. Switching
  // between them keeps only a focus's selection, which means the same in both.
  const prevTooLargeRef = useRef(tooLargeToDraw)
  useEffect(() => {
    if (prevTooLargeRef.current === tooLargeToDraw) return
    prevTooLargeRef.current = tooLargeToDraw
    setGraphSelection(prev => (prev?.type === 'node' && prev.nodeId === focusKey ? prev : null))
  }, [tooLargeToDraw, focusKey])

  const records = useTrafficRecords({
    namespaces,
    since: timeRange,
    excludeNamespaces: hideSystem ? SYSTEM_NAMESPACE_LIST : undefined,
    excludeHost: hideSystem,
    match: recordsMatch,
    enabled: wizardState === 'ready' && !isConnecting && !connectionError,
  })
  // Records come from their own query when the selection could be sent;
  // otherwise the selection is applied to the sample the flows response carries.
  const recordsEligible = recordsMatch !== null && records.supported && !records.tooLarge
  const useRecords = recordsEligible && !records.isError

  const selectionPairKeys = useMemo(
    () => selectionPairs && new Set(selectionPairs.map(p => pairKey(p.source, p.destination, p.port, p.directionUnknown))),
    [selectionPairs])
  const onSelectedEdge = useCallback(
    (f: TrafficFlow) => !selectionPairKeys ||
      selectionPairKeys.has(pairKey(drawnEndpoint(f.source), drawnEndpoint(f.destination), f.port, f.directionUnknown)),
    [selectionPairKeys, drawnEndpoint])
  // An endpoint lookup answers with all of its edges, including ones the view
  // hides (below the connection threshold), so they are dropped here.
  const filteredRecords = useMemo(
    () => (records.data?.flows ?? []).filter(f => rawFlowPasses(f) && onSelectedEdge(f)),
    [records.data?.flows, rawFlowPasses, onSelectedEdge])

  const sampleSelection = useMemo(() => {
    if (!graphSelection) return filteredRawFlows
    if (focusSelected && focus && !selection) {
      return filteredRawFlows.filter(f => touchesFocus(f.source, focus) || touchesFocus(f.destination, focus))
    }
    if (selectionPairKeys) return filteredRawFlows.filter(onSelectedEdge)
    if (graphSelection.type === 'node' && graphSelection.nodeId) {
      const id = graphSelection.nodeId
      return filteredRawFlows.filter(f => {
        const srcId = graphEndpointId(drawnEndpoint(f.source))
        const dstId = graphEndpointId(drawnEndpoint(f.destination))
        return srcId === id || dstId === id
      })
    }
    if (graphSelection.type === 'edge' && graphSelection.sourceId && graphSelection.destId) {
      return filteredRawFlows.filter(f => {
        const srcId = graphEndpointId(drawnEndpoint(f.source))
        const dstId = graphEndpointId(drawnEndpoint(f.destination))
        // Match either direction (request goes A→B, response goes B→A)
        return (srcId === graphSelection.sourceId && dstId === graphSelection.destId) ||
               (srcId === graphSelection.destId && dstId === graphSelection.sourceId)
      })
    }
    return filteredRawFlows
  }, [filteredRawFlows, graphSelection, selectionPairKeys, onSelectedEdge, drawnEndpoint, focusSelected, focus, selection])

  const listFlows = useRecords ? filteredRecords : sampleSelection

  const sampleTotal = flowsData?.flowsTotal ?? flowsData?.flows?.length ?? 0
  const sampleSize = flowsData?.flows?.length ?? 0
  const listNote = useMemo(() => {
    if (useRecords) {
      // This query's own account of what it could see: it is a separate fetch
      // from the graph's, and an empty list beside a warning is not "no traffic".
      const data = records.data
      if (!data) return undefined
      const parts: string[] = []
      if (data.warning) parts.push(data.warning)
      if (data.matched > data.flows.length) {
        parts.push(`Newest ${data.flows.length.toLocaleString()} of ${data.matched.toLocaleString()} records for this selection`)
      }
      const covered = coverageLabel(data.coveredSince, data.timestamp)
      if (covered) parts.push(`Records cover the ${covered} of ${timeRange}`)
      return parts.length > 0 ? parts.join(' · ') : undefined
    }
    if (sampleTotal <= sampleSize) return undefined
    const sample = `newest ${sampleSize.toLocaleString()} of ${sampleTotal.toLocaleString()} records`
    if (!graphSelection) return `Showing the ${sample} — select a node or edge to load its records`
    if (records.tooLarge) return `This selection is too large to look up on its own; showing its flows among the ${sample}`
    if (records.isError) return `Couldn't load this selection's records (${records.error?.message}); showing its flows among the ${sample}`
    return `Showing this selection's flows among the ${sample}`
  }, [useRecords, records.data, records.tooLarge, records.isError, records.error, sampleTotal, sampleSize, graphSelection, timeRange])

  // Stats for display
  const flowStats = useMemo(() => {
    const total = flowsData?.aggregated?.length || 0
    const filtered = filteredFlows.length
    const shown = finalFlows.length
    const hidden = total - filtered
    const aggregated = filtered - shown
    return { total, filtered, shown, hidden, aggregated }
  }, [flowsData?.aggregated?.length, filteredFlows.length, finalFlows.length])

  // Compute hot path threshold (top 10% of connections)
  const hotPathThreshold = useMemo(() => {
    if (finalFlows.length === 0) return 0
    const connectionCounts = finalFlows.map(f => f.connections).sort((a, b) => b - a)
    const topTenPercentIndex = Math.max(0, Math.floor(connectionCounts.length * 0.1) - 1)
    return connectionCounts[topTenPercentIndex] || connectionCounts[0] || 0
  }, [finalFlows])

  // Extract unique namespaces with node counts (from filtered flows, excluding namespace filter itself)
  // This shows only namespaces that pass other filters (hideSystem, hideExternal, minConnections)
  const namespacesWithCounts = useMemo(() => {
    const nsCounts = new Map<string, Set<string>>() // namespace -> set of node names

    // Use flows filtered by everything EXCEPT namespace filter
    const flows = (flowsData?.aggregated || []).filter(flow => {
      const sourceIsSystem = isSystemEndpoint(flow.source.name, flow.source.namespace, flow.source.kind)
      const destIsSystem = isSystemEndpoint(flow.destination.name, flow.destination.namespace, flow.destination.kind)

      if (hideSystem && (sourceIsSystem || destIsSystem)) {
        return false
      }

      if (hideExternal) {
        if (isExternal(flow.source.kind) || isExternal(flow.destination.kind)) {
          return false
        }
      }

      if (flow.connections < activeMinConnections) {
        return false
      }

      return true
    })

    flows.forEach(flow => {
      // Count source nodes
      if (flow.source.namespace && !isExternalKind(flow.source.kind)) {
        if (!nsCounts.has(flow.source.namespace)) {
          nsCounts.set(flow.source.namespace, new Set())
        }
        nsCounts.get(flow.source.namespace)!.add(flow.source.name)
      }
      // Count destination nodes
      if (flow.destination.namespace && !isExternalKind(flow.destination.kind)) {
        if (!nsCounts.has(flow.destination.namespace)) {
          nsCounts.set(flow.destination.namespace, new Set())
        }
        nsCounts.get(flow.destination.namespace)!.add(flow.destination.name)
      }
    })

    return Array.from(nsCounts.entries()).map(([name, nodes]) => ({
      name,
      nodeCount: nodes.size,
    }))
  }, [flowsData?.aggregated, hideSystem, hideExternal, activeMinConnections])

  const namespaceScopeQuery = useNamespaceScope()
  const namespaceLocked = !!namespaceScopeQuery.data?.cacheScoped && !namespaceScopeQuery.data?.namespaceRescope
  // Narrowing to one namespace keeps the edges with either end in it, as the
  // server does, so these are what each choice would show.
  const namespaceChoices = useMemo(() => {
    if (!onSetNamespaces || namespaceLocked || focus) return null
    const choices = namespaceSummaries(finalFlows)
      .filter(ns => !(namespaces.length === 1 && namespaces[0] === ns.name))
    return choices.length > 0 ? choices : null
  }, [onSetNamespaces, namespaceLocked, focus, finalFlows, namespaces])

  const focusPartial = !!focus?.namespace && namespaces.length > 0 && !namespaces.includes(focus.namespace)

  const resetFilters = useCallback(() => {
    setHideSystem(false)
    setHideExternal(false)
    chooseMinConnections(0)
    setHiddenNamespaces(new Set())
    setAddonMode('show')
    setL7Protocol('all')
    setL7Methods(new Set())
    setL7StatusRanges(new Set())
    setL7Verdicts(new Set())
    setDnsPattern('')
  }, [chooseMinConnections])

  // Determine wizard state based on sources detection
  useEffect(() => {
    if (sourcesLoading) {
      setWizardState('detecting')
      return
    }

    if (!sourcesData) {
      setWizardState('not_found')
      return
    }

    // Only consider sources with status 'available' as ready
    const availableSources = sourcesData.detected.filter(s => s.status === 'available')
    if (availableSources.length > 0) {
      setWizardState('ready')
    } else {
      setWizardState('not_found')
    }
  }, [sourcesData, sourcesLoading])

  // Shared connection handler — used by auto-connect and retry buttons.
  // Deliberately does NOT reset hasAutoConnectedRef on failure: that ref gates
  // the auto-connect effect, and re-arming it would re-fire auto-connect on
  // every failed attempt (unbounded loop). Retries come from the explicit
  // Retry button, which calls handleConnect directly.
  const handleConnect = useCallback(() => {
    setIsConnecting(true)
    setConnectionError(null)
    queryClient.removeQueries({ queryKey: ['traffic-flows'] })

    connectMutation.mutate(undefined, {
      onSuccess: (data) => {
        setIsConnecting(false)
        if (!data.connected && data.error) {
          setConnectionError(data.error)
        }
      },
      onError: (error) => {
        setIsConnecting(false)
        setConnectionError(error.message)
      },
    })
  }, [connectMutation, queryClient])

  // Auto-connect once when a source is first detected. Strictly one-shot per
  // mount / cluster (the ref resets on cluster change); failures surface a
  // manual Retry rather than re-arming this effect.
  useEffect(() => {
    if (wizardState === 'ready' && !hasAutoConnectedRef.current && !isConnecting) {
      hasAutoConnectedRef.current = true
      handleConnect()
    }
  }, [wizardState, isConnecting, handleConnect])

  // Show wizard if no traffic source detected
  if (wizardState !== 'ready') {
    return (
      <TrafficWizard
        state={wizardState}
        setState={setWizardState}
        sourcesData={sourcesData}
        sourcesLoading={sourcesLoading}
        onRefetch={refetchSources}
      />
    )
  }

  return (
    <TrafficFlowListProvider
      flows={listFlows}
      responsesCallerOriented={(useRecords ? records.data?.l7ResponsesCallerOriented : flowsData?.l7ResponsesCallerOriented) === true}
      graphSelection={graphSelection}
      clearSelection={clearGraphSelection}
      loading={useRecords && records.isLoading}
      note={listNote}
    >
    <div className="flex h-full w-full">
      {/* Sidebar */}
      <TrafficFilterSidebar
        hideSystem={hideSystem}
        setHideSystem={setHideSystem}
        hideExternal={hideExternal}
        setHideExternal={setHideExternal}
        minConnections={activeMinConnections}
        setMinConnections={chooseMinConnections}
        showNamespaceGroups={showNamespaceGroups}
        setShowNamespaceGroups={setShowNamespaceGroups}
        collapseInternet={collapseInternet}
        setCollapseInternet={setCollapseInternet}
        groupByWorkload={groupByWorkload}
        setGroupByWorkload={setGroupByWorkload}
        addonMode={addonMode}
        setAddonMode={setAddonMode}
        aggregateExternal={aggregateExternal}
        setAggregateExternal={setAggregateExternal}
        detectServices={detectServices}
        setDetectServices={setDetectServices}
        timeRange={timeRange}
        setTimeRange={setTimeRange}
        showL7Filters={hasL7Data}
        availableStatusRanges={l7Capabilities.availableStatusRanges}
        availableVerdicts={l7Capabilities.availableVerdicts}
        hasDNSQueries={l7Capabilities.hasDNSQueries}
        availableHTTPMethods={l7Capabilities.availableHTTPMethods}
        isRateBased={isRateBased}
        l7Protocol={l7Protocol}
        setL7Protocol={setL7Protocol}
        l7Methods={l7Methods}
        onToggleL7Method={toggleL7Method}
        l7StatusRanges={l7StatusRanges}
        onToggleL7StatusRange={toggleL7StatusRange}
        l7Verdicts={l7Verdicts}
        onToggleL7Verdict={toggleL7Verdict}
        dnsPattern={dnsPattern}
        setDnsPattern={setDnsPattern}
        namespaces={namespacesWithCounts}
        hiddenNamespaces={chosenHiddenNamespaces}
        onToggleNamespace={toggleNamespace}
        namespacesPausedFor={focus?.name}
      />

      {/* Main content area */}
      <div ref={graphPaneRef} className="flex-1 relative min-w-0">
          {/* Floating controls — overlaid on graph like topology view */}
          {(() => {
            const availableSources = sourcesData?.detected.filter(s => s.status === 'available') || []
            const activeName = sourcesData?.active
            const activeSource = availableSources.find(s => s.name === activeName) || availableSources[0]

            const handleSwitchSource = (name: string) => {
              if (name === activeSource?.name) { setSourcePickerOpen(false); return }
              setSourcePickerOpen(false)
              setIsConnecting(true)
              setConnectionError(null)
              hasAutoConnectedRef.current = true
              setSourceMutation.mutate(name, {
                onSuccess: () => {
                  queryClient.invalidateQueries({ queryKey: ['traffic-sources'] })
                  connectMutation.mutate(undefined, {
                    onSuccess: (data) => {
                      setIsConnecting(false)
                      if (!data.connected && data.error) setConnectionError(data.error)
                      queryClient.invalidateQueries({ queryKey: ['traffic-flows'] })
                    },
                    onError: (error) => { setIsConnecting(false); setConnectionError(error.message) },
                  })
                },
                onError: (error) => { setIsConnecting(false); setConnectionError(error.message) },
              })
            }

            return (
              <>
                {/* Top-left: source status pill, focus */}
                <div className="absolute top-3 left-3 z-10 flex items-center gap-2">
                  {focus ? (
                    <div className="flex items-center gap-1.5 pl-2 pr-1 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-skyhook-500/40 text-[11px]">
                      <Crosshair className="h-3 w-3 text-skyhook-500" />
                      <span className="text-theme-text-secondary">Focused on</span>
                      <span className="font-medium text-theme-text-primary">{focus.name}</span>
                      {focus.namespace && <span className="text-theme-text-tertiary">({focus.namespace})</span>}
                      {focusPartial && (
                        // Narrowing fetches the edges with an end in the chosen
                        // namespaces, so a focus outside them shows only those.
                        <>
                          <span className="text-amber-500">· only its traffic with {namespaces.join(', ')}</span>
                          {onSetNamespaces && !namespaceLocked && (
                            <button type="button" onClick={() => onSetNamespaces([focus.namespace!])} className="text-blue-400 hover:text-blue-300 font-medium">
                              Show all
                            </button>
                          )}
                        </>
                      )}
                      <Tooltip content="Show everything again">
                        <button type="button" onClick={() => setFocus(null)} aria-label="Leave focus" className="p-0.5 rounded hover:bg-theme-hover text-theme-text-secondary">
                          <X className="h-3 w-3" />
                        </button>
                      </Tooltip>
                    </div>
                  ) : null}
                  <TrafficFocusSearch
                    endpoints={focusableEndpoints}
                    onFocus={setFocus}
                    isRateBased={isRateBased}
                    overlayContainer={graphPaneRef.current}
                  />
                  {activeSource && (
                    <div className="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[11px]">
                      {isConnecting ? (
                        <>
                          <Loader2 className="h-3 w-3 animate-spin text-blue-400" />
                          <span className="text-blue-400">Connecting...</span>
                        </>
                      ) : connectionError ? (
                        <>
                          <span className="w-2 h-2 rounded-full bg-yellow-500" />
                          <span className="text-theme-text-secondary">{activeSource.name}</span>
                          <button onClick={handleConnect} className="text-yellow-500 hover:text-yellow-400 font-medium">retry</button>
                        </>
                      ) : (
                        <>
                          <span className="w-2 h-2 rounded-full bg-green-500" />
                          {availableSources.length > 1 ? (
                            <div className="relative" ref={sourcePickerRef}>
                              <button onClick={() => setSourcePickerOpen(!sourcePickerOpen)} className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary">
                                {activeSource.name} <ChevronDown className="h-3 w-3" />
                              </button>
                              {sourcePickerOpen && (
                                <div className="absolute top-full left-0 mt-1 z-50 bg-theme-surface border border-theme-border rounded-md shadow-lg py-1 min-w-[120px]">
                                  {availableSources.map(source => (
                                    <button key={source.name} onClick={() => handleSwitchSource(source.name)}
                                      className={clsx('w-full text-left px-3 py-1 text-xs hover:bg-theme-hover capitalize', source.name === activeSource.name && 'text-blue-400')}>
                                      {source.name}
                                    </button>
                                  ))}
                                </div>
                              )}
                            </div>
                          ) : (
                            <span className="text-theme-text-secondary">{activeSource.name}</span>
                          )}
                        </>
                      )}
                    </div>
                  )}
                </div>

                {/* Top-right: stats + actions */}
                <div className="absolute top-3 right-3 z-10 flex items-center gap-2">
                  {flowsData?.flows && flowsData.flows.length > 0 && (
                    <Tooltip content="Open flow list in dock">
                    <button onClick={openFlowListDock}
                      className="flex items-center gap-1 px-2 py-1 text-[10px] rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-theme-text-secondary hover:text-theme-text-primary transition-colors">
                      <List className="w-3 h-3" /> Flows
                    </button>
                    </Tooltip>
                  )}
                  {coverage && (
                    // Not a warning: a busy cluster reaches the limit on every
                    // fetch. It says which part of the window the map shows.
                    <Tooltip content={coverageExplanation(flowsData, timeRange, hideSystem)}>
                      <div className="flex items-center gap-1 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[10px] text-theme-text-secondary tabular-nums">
                        <Clock className="w-3 h-3" /> {coverage} of {timeRange}
                      </div>
                    </Tooltip>
                  )}
                  <div className="flex items-center px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[10px] text-theme-text-tertiary tabular-nums">
                    {flowStats.shown}/{flowStats.total}
                  </div>
                  {/* Flows are a REST snapshot (no poll, no stream), so this is
                      an honest "Updated N ago" + manual refresh — not "live". */}
                  <div className="flex items-center rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border px-1.5 py-0.5">
                    <FreshnessControl
                      mode="snapshot"
                      dataUpdatedAt={flowsUpdatedAt}
                      isFetching={flowsFetching}
                      onRefresh={() => {
                        refetchFlowsRaw()
                        if (recordsEligible) records.refetch()
                      }}
                      connectionState={connection.state}
                    />
                  </div>
                </div>
              </>
            )
          })()}

          {isConnecting || (flowsFetching && finalFlows.length === 0) ? (
            <PaneLoader
              label={isConnecting ? 'Connecting to traffic source…' : 'Loading traffic data…'}
              className="absolute inset-0"
            />
          ) : finalFlows.length > 0 ? (
            <>
              {flowsData?.warning && (
                // Sits below the two chip rows (both top-3) rather than beside
                // them: centred at that height it would cover the flow count and
                // the refresh control at common widths. role/aria-live because it
                // appears after the graph has already rendered.
                <div
                  role="status"
                  aria-live="polite"
                  className="absolute top-14 left-1/2 z-10 w-[min(40rem,calc(100%-1.5rem))] -translate-x-1/2"
                >
                  <AlertBanner
                    variant="warning"
                    // The title follows the kind. A partial warning is about values
                    // on edges that are shown — a port reported as 0, UDP as TCP, a
                    // 5xx rate that failed to load — so it says "unreliable", not
                    // "incomplete", which would send the reader looking for missing
                    // workloads. An incomplete or transient one beside flows is
                    // exactly that: a stream cut short, events lost, a node the
                    // relay could not reach, a TCP query that failed — edges may
                    // be missing from what is drawn.
                    title={warningIsPermanent ? 'Some values on this map are unreliable' : 'Some traffic may be missing from this map'}
                    message={flowsData.warning}
                  />
                </div>
              )}
              {tooLargeToDraw ? (
                <TrafficGraphTooLarge
                  graph={drawnGraph}
                  focusName={focus?.name}
                  onDrawAnyway={drawnGraph.score <= GRAPH_DRAW_CEILING ? () => setDrawnViewKey(viewKey) : undefined}
                  namespaces={namespaceChoices}
                  onPickNamespace={ns => onSetNamespaces?.([ns])}
                  endpoints={focusableEndpoints}
                  onFocus={setFocus}
                  isRateBased={isRateBased}
                  overlayContainer={graphPaneRef.current}
                  flows={focusedFlows}
                  selection={graphSelection}
                  onSelect={setGraphSelection}
                  focusedId={focusKey}
                />
              ) : (
                <TrafficGraph
                  flows={finalFlows}
                  hotPathThreshold={hotPathThreshold}
                  showNamespaceGroups={showNamespaceGroups}
                  serviceCategories={serviceCategories}
                  addonMode={groupAddons ? 'group' : addonMode === 'group' ? 'show' : addonMode}
                  trafficSource={sourcesData?.active || ''}
                  selection={graphSelection}
                  onSelectionChange={setGraphSelection}
                  onFocus={setFocus}
                  focusedId={focusKey}
                />
              )}
            </>
          ) : focus && flowsData && !connectionError ? (
            <div className="absolute inset-0 flex items-center justify-center px-4">
              <FocusNotFound
                focus={focus}
                hiddenByFilters={(flowsData?.aggregated ?? []).some(f => touchesFocus(f.source, focus) || touchesFocus(f.destination, focus))}
                systemHidden={hideSystem && !!focus.namespace && SYSTEM_NAMESPACES.has(focus.namespace)}
                outsideNamespaces={namespaces.length > 0 && !!focus.namespace && !namespaces.includes(focus.namespace)}
                window={coverage ? `${coverage} of ${timeRange}` : `last ${timeRange}`}
                onShowAll={resetFilters}
                onShowSystem={() => setHideSystem(false)}
                onSwitchNamespace={onSetNamespaces && !namespaceLocked && focus.namespace ? () => onSetNamespaces([focus.namespace!]) : undefined}
                warning={flowsData.warning}
                onLeave={() => setFocus(null)}
              />
            </div>
          ) : connectionError ? (
            <div className="absolute inset-0 flex items-center justify-center">
              <div className="text-center space-y-3">
                <Plug className="h-12 w-12 text-yellow-500 mx-auto" />
                <p className="text-theme-text-secondary">Connection failed</p>
                <p className="text-xs text-theme-text-tertiary max-w-md">
                  {connectionError}
                </p>
                <button
                  onClick={handleConnect}
                  className="px-3 py-1.5 text-sm btn-brand rounded"
                >
                  Retry Connection
                </button>
              </div>
            </div>
          ) : (
            <div className="absolute inset-0 flex items-center justify-center px-4">
              {flowStats.total > 0 && flowStats.shown === 0 ? (
                <EmptyState
                  tone="filtered"
                  variant="card"
                  icon={Filter}
                  headline="All traffic is filtered out"
                  body={`${flowStats.total} ${flowStats.total === 1 ? 'flow' : 'flows'} hidden by current filters.`}
                  action={
                    <button
                      type="button"
                      // Nine filters can empty this view; clearing three of them left
                      // the user pressing a button that promised everything back and
                      // changed nothing whenever the cause was a namespace, an addon
                      // mode or an L7 choice.
                      onClick={resetFilters}
                      className="badge badge-sm border border-theme-border bg-theme-elevated text-theme-text-primary hover:bg-theme-hover transition-colors"
                    >
                      Show all
                    </button>
                  }
                  className="max-w-md"
                />
              ) : flowsData?.warning ? (
                <EmptyState
                  tone="neutral"
                  variant="card"
                  icon={AlertTriangle}
                  headline={
                    warningIsPermanent
                      ? 'No traffic Radar can place on the map'
                      : warningIsIncomplete
                        ? 'No traffic seen, but some may be missing'
                        : 'Unable to fetch traffic data'
                  }
                  body={flowsData.warning}
                  className="max-w-md"
                />
              ) : (
                <EmptyState
                  tone="neutral"
                  variant="card"
                  icon={Activity}
                  headline={
                    sourcesData?.active
                      ? `Observing via ${sourcesData.active} — no traffic yet`
                      : 'No traffic observed yet'
                  }
                  body="Traffic will appear here once services start communicating."
                  className="max-w-md"
                />
              )}
            </div>
          )}
      </div>
    </div>
    </TrafficFlowListProvider>
  )
}

/**
 * A focus with nothing to show. Says which of the reasons it is, since only
 * one of them means the workload is quiet: Radar may have hidden its traffic,
 * or never fetched it.
 */
function FocusNotFound({ focus, hiddenByFilters, systemHidden, outsideNamespaces, window, onShowAll, onShowSystem, onSwitchNamespace, warning, onLeave }: {
  focus: TrafficFocus
  /** The fetch's own warning: with it, absence is not proof of quiet. */
  warning?: string
  hiddenByFilters: boolean
  systemHidden: boolean
  outsideNamespaces: boolean
  window: string
  onShowAll: () => void
  onShowSystem: () => void
  onSwitchNamespace?: () => void
  onLeave: () => void
}) {
  const name = focus.namespace ? `${focus.name} (${focus.namespace})` : focus.name
  let body: string
  let action: { label: string; onClick: () => void } | undefined
  if (hiddenByFilters) {
    body = 'Its traffic is hidden by the current filters.'
    action = { label: 'Show all traffic', onClick: onShowAll }
  } else if (systemHidden) {
    body = `${focus.namespace} is a system namespace, and Hide System keeps its traffic from being fetched.`
    action = { label: 'Show system traffic', onClick: onShowSystem }
  } else if (outsideNamespaces) {
    body = `${focus.namespace} is outside the namespaces in view, so only its traffic with them was fetched, and there was none.`
    if (onSwitchNamespace) action = { label: `Switch to ${focus.namespace}`, onClick: onSwitchNamespace }
  } else if (warning) {
    body = `Radar found none in the ${window}, but some traffic may be missing.`
  } else {
    body = `Radar saw no traffic to or from it in the ${window}.`
  }
  // Whatever else applies, a fetch that reported trouble is not proof of quiet.
  if (warning) body += ` ${warning}`
  return (
    <EmptyState
      tone="neutral"
      variant="card"
      icon={Crosshair}
      headline={`No traffic for ${name}`}
      body={body}
      action={
        <div className="flex flex-wrap items-center justify-center gap-2">
          {action && (
            <button type="button" onClick={action.onClick} className="btn-brand-muted text-xs px-2.5 py-1 rounded-md">
              {action.label}
            </button>
          )}
          <button
            type="button"
            onClick={onLeave}
            className="text-xs px-2.5 py-1 rounded-md border border-theme-border text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover"
          >
            Leave focus
          </button>
        </div>
      }
      className="max-w-md"
    />
  )
}
