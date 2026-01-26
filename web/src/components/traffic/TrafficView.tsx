import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import { useTrafficSources, useTrafficFlows, useTrafficConnect } from '../../api/traffic'
import { useClusterInfo } from '../../api/client'
import type { TrafficWizardState, AggregatedFlow } from '../../types'
import { TrafficWizard } from './TrafficWizard'
import { TrafficGraph } from './TrafficGraph'
import { TrafficFilterSidebar } from './TrafficFilterSidebar'
import { Loader2, RefreshCw, Filter, Plug } from 'lucide-react'
import { clsx } from 'clsx'
import { useQueryClient } from '@tanstack/react-query'

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


// Check if an endpoint is a system/infrastructure component
function isSystemEndpoint(name: string, namespace: string | undefined, kind: string): boolean {
  // System namespaces
  if (namespace && SYSTEM_NAMESPACES.has(namespace)) {
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

// Helper to check if endpoint is external (case-insensitive)
function isExternal(kind: string): boolean {
  return kind.toLowerCase() === 'external'
}

interface TrafficViewProps {
  namespace: string
}

export function TrafficView({ namespace }: TrafficViewProps) {
  const [wizardState, setWizardState] = useState<TrafficWizardState>('detecting')
  const [timeRange, setTimeRange] = useState<string>('5m')
  const [hideSystem, setHideSystem] = useState(true)
  const [hideExternal, setHideExternal] = useState(false)
  const [minConnections, setMinConnections] = useState(0)
  const [showNamespaceGroups, setShowNamespaceGroups] = useState(true)
  const [aggregateExternal, setAggregateExternal] = useState(true)
  const [detectServices, setDetectServices] = useState(true)
  const [collapseInternet, setCollapseInternet] = useState(true)
  const [addonMode, setAddonMode] = useState<AddonMode>('show')
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [hiddenNamespaces, setHiddenNamespaces] = useState<Set<string>>(new Set())
  const [isConnecting, setIsConnecting] = useState(false)
  const [connectionError, setConnectionError] = useState<string | null>(null)
  const queryClient = useQueryClient()
  const connectMutation = useTrafficConnect()
  const hasAutoConnectedRef = useRef(false)

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

  const {
    data: sourcesData,
    isLoading: sourcesLoading,
    refetch: refetchSources,
  } = useTrafficSources()

  const {
    data: flowsData,
    isLoading: flowsLoading,
    refetch: refetchFlows,
  } = useTrafficFlows({
    namespace,
    since: timeRange,
    enabled: wizardState === 'ready',
  })

  // Filter flows based on user preferences
  // Note: namespace filtering is done server-side via the global namespace selector
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
      if (flow.connections < minConnections) {
        return false
      }

      // Filter by hidden namespaces - hide flow if EITHER endpoint is in a hidden namespace
      if (hiddenNamespaces.size > 0) {
        const sourceNs = flow.source.namespace
        const destNs = flow.destination.namespace
        if (sourceNs && hiddenNamespaces.has(sourceNs)) return false
        if (destNs && hiddenNamespaces.has(destNs)) return false
      }

      return true
    })
  }, [flowsData?.aggregated, hideSystem, hideExternal, minConnections, hiddenNamespaces, addonMode])

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
  const { processedFlows, serviceCategories } = useMemo<{
    processedFlows: AggregatedFlow[]
    serviceCategories: Map<string, string>
  }>(() => {
    const categories = new Map<string, string>()

    // Helper to get service info (optionally using port-based detection)
    const getServiceInfo = (name: string, port: number) => {
      return getExternalServiceName(name, detectServices ? port : undefined)
    }

    if (!aggregateExternal) {
      // Even without aggregation, detect service categories for coloring (destinations only)
      filteredFlows.forEach(flow => {
        if (isExternal(flow.destination.kind)) {
          const info = getServiceInfo(flow.destination.name, flow.port)
          if (info.category) {
            categories.set(flow.destination.name, info.category)
          }
        }
        // Don't apply port-based detection to sources - port tells us the destination service
      })
      return { processedFlows: filteredFlows, serviceCategories: categories }
    }

    // Aggregate flows to the same external service
    const aggregatedMap = new Map<string, AggregatedFlow>()

    filteredFlows.forEach(flow => {
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
      // Group by service name, not by port (all MongoDB connections become one edge)
      const key = `${sourceKey}->${destKey}`

      const existing = aggregatedMap.get(key)
      if (existing) {
        // Merge connections and bytes
        existing.connections += flow.connections
        existing.bytesSent += flow.bytesSent
        existing.bytesRecv += flow.bytesRecv
        existing.flowCount += flow.flowCount
        if (flow.requestCount) {
          existing.requestCount = (existing.requestCount || 0) + flow.requestCount
        }
        if (flow.errorCount) {
          existing.errorCount = (existing.errorCount || 0) + flow.errorCount
        }
      } else {
        // Create new aggregated flow with modified names
        aggregatedMap.set(key, {
          ...flow,
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
  }, [filteredFlows, aggregateExternal, detectServices])

  // Collapse inbound internet traffic (external sources → internal destinations)
  const internetCollapsedFlows = useMemo<AggregatedFlow[]>(() => {
    if (!collapseInternet) return processedFlows

    // Group flows where external sources connect to internal destinations
    const internetFlowsMap = new Map<string, AggregatedFlow>() // destKey -> aggregated flow
    const nonInternetFlows: AggregatedFlow[] = []

    processedFlows.forEach(flow => {
      const sourceIsExternal = isExternal(flow.source.kind)
      const destIsInternal = !isExternal(flow.destination.kind)

      // Only collapse external → internal flows (inbound internet traffic)
      if (sourceIsExternal && destIsInternal) {
        // Create a key based on destination + port
        const destKey = flow.destination.namespace
          ? `${flow.destination.namespace}/${flow.destination.name}:${flow.port}`
          : `${flow.destination.name}:${flow.port}`

        const existing = internetFlowsMap.get(destKey)
        if (existing) {
          // Merge into existing "Internet" flow
          existing.connections += flow.connections
          existing.bytesSent += flow.bytesSent
          existing.bytesRecv += flow.bytesRecv
          existing.flowCount += flow.flowCount
        } else {
          // Create new "Internet" → destination flow
          internetFlowsMap.set(destKey, {
            ...flow,
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
  const finalFlows = useMemo<AggregatedFlow[]>(() => {
    if (addonMode !== 'group') return internetCollapsedFlows

    // Track totals for aggregated edges
    let addonInternetTotal = 0
    let addonToK8sTotal = 0
    const processedFlows: AggregatedFlow[] = []

    // Check if destination is the kubernetes API server
    const isKubernetesAPI = (name: string, namespace: string | undefined) => {
      return name === 'kubernetes' && (!namespace || namespace === 'default')
    }

    internetCollapsedFlows.forEach(flow => {
      const sourceIsAddon = isClusterAddon(flow.source.name, flow.source.namespace)
      const destIsAddon = isClusterAddon(flow.destination.name, flow.destination.namespace)
      const sourceIsInternet = flow.source.kind === 'Internet'
      const destIsK8sAPI = isKubernetesAPI(flow.destination.name, flow.destination.namespace)

      // Internet → Addon: aggregate into single edge to group
      if (sourceIsInternet && destIsAddon) {
        addonInternetTotal += flow.connections
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
        bytesSent: 0,
        bytesRecv: 0,
        flowCount: 1,
        lastSeen: new Date().toISOString(),
      })
    }

    return processedFlows
  }, [internetCollapsedFlows, addonMode])

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

      if (flow.connections < minConnections) {
        return false
      }

      return true
    })

    flows.forEach(flow => {
      // Count source nodes
      if (flow.source.namespace && flow.source.kind.toLowerCase() !== 'external') {
        if (!nsCounts.has(flow.source.namespace)) {
          nsCounts.set(flow.source.namespace, new Set())
        }
        nsCounts.get(flow.source.namespace)!.add(flow.source.name)
      }
      // Count destination nodes
      if (flow.destination.namespace && flow.destination.kind.toLowerCase() !== 'external') {
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
  }, [flowsData?.aggregated, hideSystem, hideExternal, minConnections])

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

  // Auto-connect when source is detected
  useEffect(() => {
    if (wizardState === 'ready' && !hasAutoConnectedRef.current && !isConnecting) {
      hasAutoConnectedRef.current = true
      setIsConnecting(true)
      setConnectionError(null)

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
    }
  }, [wizardState, isConnecting, connectMutation])

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
    <div className="flex h-full w-full">
      {/* Sidebar */}
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
        namespaces={namespacesWithCounts}
        hiddenNamespaces={hiddenNamespaces}
        onToggleNamespace={toggleNamespace}
        collapsed={sidebarCollapsed}
        onToggleCollapse={() => setSidebarCollapsed(!sidebarCollapsed)}
      />

      {/* Main content area */}
      <div className="flex-1 flex flex-col min-w-0">
        {/* Compact header */}
        <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border bg-theme-surface/50">
          <div className="flex items-center gap-4">
            <h2 className="text-sm font-medium text-theme-text-primary">Traffic Flow</h2>
            {(() => {
              const activeSource = sourcesData?.detected.find(s => s.status === 'available')
              if (!activeSource) return null
              return (
                <div className="flex items-center gap-2 text-xs text-theme-text-secondary border-l border-theme-border pl-4">
                  {isConnecting ? (
                    <>
                      <Loader2 className="h-3 w-3 animate-spin text-blue-400" />
                      <span className="text-blue-400">Connecting...</span>
                    </>
                  ) : connectionError ? (
                    <>
                      <span className="inline-block w-2 h-2 rounded-full bg-yellow-500" />
                      <span className="text-yellow-400">{activeSource.name}</span>
                      <button
                        onClick={() => {
                          hasAutoConnectedRef.current = false
                          setConnectionError(null)
                        }}
                        className="flex items-center gap-1 px-1.5 py-0.5 text-[10px] bg-yellow-500/10 text-yellow-400 rounded hover:bg-yellow-500/20"
                        title="Retry connection"
                      >
                        <Plug className="h-3 w-3" />
                        Retry
                      </button>
                    </>
                  ) : (
                    <>
                      <span className="inline-block w-2 h-2 rounded-full bg-green-500" />
                      <span>via {activeSource.name}</span>
                      {activeSource.native && (
                        <span className="px-1.5 py-0.5 text-[10px] bg-blue-500/10 text-blue-400 rounded">
                          Native
                        </span>
                      )}
                      <span className="text-theme-text-tertiary">· network connections observed via eBPF</span>
                    </>
                  )}
                </div>
              )
            })()}
          </div>

          <div className="flex items-center gap-3">
            {/* Flow stats */}
            <div className="text-xs text-theme-text-tertiary">
              {flowStats.shown} of {flowStats.total} flows
              {flowStats.aggregated > 0 && (
                <span className="text-theme-text-secondary"> ({flowStats.aggregated} aggregated)</span>
              )}
            </div>

            <button
              onClick={() => refetchFlows()}
              disabled={flowsLoading}
              className={clsx(
                'p-1.5 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover transition-colors',
                flowsLoading && 'opacity-50 cursor-not-allowed'
              )}
              title="Refresh traffic data"
            >
              {flowsLoading ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="h-4 w-4" />
              )}
            </button>
          </div>
        </div>

        {/* Graph area */}
        <div className="flex-1 relative">
          {flowsLoading && !flowsData ? (
            <div className="absolute inset-0 flex items-center justify-center">
              <div className="flex items-center gap-2 text-theme-text-secondary">
                <Loader2 className="h-5 w-5 animate-spin" />
                <span>Loading traffic data...</span>
              </div>
            </div>
          ) : finalFlows.length > 0 ? (
            <TrafficGraph
              flows={finalFlows}
              hotPathThreshold={hotPathThreshold}
              showNamespaceGroups={showNamespaceGroups}
              serviceCategories={serviceCategories}
              addonMode={addonMode}
            />
          ) : (
            <div className="absolute inset-0 flex items-center justify-center">
              <div className="text-center space-y-2">
                <Filter className="h-12 w-12 text-theme-text-tertiary mx-auto" />
                {flowStats.total > 0 && flowStats.shown === 0 ? (
                  <>
                    <p className="text-theme-text-secondary">All traffic is filtered out</p>
                    <p className="text-xs text-theme-text-tertiary">
                      {flowStats.total} flows hidden by current filters.
                      <button
                        onClick={() => {
                          setHideSystem(false)
                          setHideExternal(false)
                          setMinConnections(0)
                        }}
                        className="ml-1 text-blue-400 hover:underline"
                      >
                        Show all
                      </button>
                    </p>
                  </>
                ) : flowsData?.warning ? (
                  <>
                    <p className="text-theme-text-secondary">Unable to fetch traffic data</p>
                    <p className="text-xs text-yellow-500 max-w-md">
                      {flowsData.warning}
                    </p>
                  </>
                ) : (
                  <>
                    <p className="text-theme-text-secondary">No traffic observed</p>
                    <p className="text-xs text-theme-text-tertiary">
                      Traffic will appear here once connections are made between services
                    </p>
                  </>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
