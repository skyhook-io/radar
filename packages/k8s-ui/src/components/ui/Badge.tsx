import { clsx } from 'clsx'
import type { ReactNode } from 'react'

// =============================================================================
// BADGE COMPONENT — single source of truth for all badge rendering
// All class names are STATIC STRINGS (required for Tailwind content scanning)
// =============================================================================

export type BadgeSeverity = 'success' | 'warning' | 'alert' | 'error' | 'info' | 'neutral'
export type BadgeSize = 'sm' | 'default'

// Intent tokens beyond severity/kind. Named by PURPOSE, not hue: two tokens may
// share a color value today yet stay separate names so a future retune moves
// exactly the right ones. See DESIGN.md "Badge decision tree".
//
// - protocol: transport/scheme tags (HTTP/TCP/UDP/…). Its OWN family because it
//   is a recurring, meaningful axis and must NOT collide with severity hues
//   (e.g. a green "HTTPS" must not read as "success").
// - note:     neutral attention/FYI markers (cross-namespace, wildcard, default,
//   immutable). Distinct from severity-info — it flags "noteworthy", not a status.
// - accent1/2/3: LOCAL categorical distinction (rw/ro, spot/on-demand,
//   control-plane/worker) where the hue carries no cross-screen meaning — just
//   "tell these sibling options apart". The only place sharing is intended.
// - structural: ports/paths/hosts/weights/names — neutral data fragments, not a
//   status. Its own name so it can diverge from severity-neutral later.
export type BadgeTone = 'note' | 'accent1' | 'accent2' | 'accent3' | 'structural'

interface BadgeProps {
  /** Severity-based coloring (status badges) */
  severity?: BadgeSeverity
  /** K8s resource kind coloring (kind badges) */
  kind?: string
  /** Transport protocol/scheme tag (http/https/tls/tcp/udp/grpc/h2) */
  protocol?: string
  /** Categorical/accent intent tone (see BadgeTone) */
  tone?: BadgeTone
  /** Explicit color class override (bypasses severity/kind lookup) */
  colorClass?: string
  /** Size variant */
  size?: BadgeSize
  /** Additional classes */
  className?: string
  /** Click handler (renders as button) */
  onClick?: () => void
  /** Title/tooltip */
  title?: string
  children: ReactNode
}

// ---------------------------------------------------------------------------
// SEVERITY COLORS — static strings for Tailwind scanning
// ---------------------------------------------------------------------------
const SEVERITY: Record<BadgeSeverity, string> = {
  success: 'bg-emerald-100 text-emerald-700 border-emerald-300 dark:bg-emerald-950/50 dark:text-emerald-400 dark:border-emerald-700/40',
  warning: 'bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-950/50 dark:text-amber-400 dark:border-amber-700/40',
  alert:   'bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-950/50 dark:text-orange-400 dark:border-orange-700/40',
  error:   'bg-red-100 text-red-700 border-red-300 dark:bg-red-950/50 dark:text-red-400 dark:border-red-700/40',
  info:    'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40',
  neutral: 'bg-theme-hover/50 text-theme-text-secondary border-theme-border',
}

// ---------------------------------------------------------------------------
// KIND COLORS — static strings, every K8s resource type
// ---------------------------------------------------------------------------
const KIND: Record<string, string> = {
  // Workloads
  Deployment:    'bg-emerald-100 text-emerald-700 border-emerald-300 dark:bg-emerald-950/50 dark:text-emerald-400 dark:border-emerald-700/40',
  StatefulSet:   'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  DaemonSet:     'bg-teal-100 text-teal-800 border-teal-300 dark:bg-teal-950/50 dark:text-teal-400 dark:border-teal-700/40',
  ReplicaSet:    'bg-green-100 text-green-800 border-green-300 dark:bg-green-950/50 dark:text-green-400 dark:border-green-700/40',
  Pod:           'bg-lime-100 text-lime-800 border-lime-300 dark:bg-lime-950/50 dark:text-lime-300 dark:border-lime-700/40',
  PodGroup:      'bg-lime-100 text-lime-800 border-lime-300 dark:bg-lime-950/50 dark:text-lime-300 dark:border-lime-700/40',
  Rollout:       'bg-emerald-100 text-emerald-700 border-emerald-300 dark:bg-emerald-950/50 dark:text-emerald-400 dark:border-emerald-700/40',

  // Networking
  Service:       'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  Endpoints:     'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40',
  EndpointSlice: 'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40',
  Internet:      'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  Ingress:       'bg-violet-100 text-violet-700 border-violet-300 dark:bg-violet-950/50 dark:text-violet-400 dark:border-violet-700/40',
  Gateway:       'bg-violet-100 text-violet-700 border-violet-300 dark:bg-violet-950/50 dark:text-violet-400 dark:border-violet-700/40',
  HTTPRoute:     'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  GRPCRoute:     'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  TCPRoute:      'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  TLSRoute:      'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',

  // Config
  ConfigMap:     'bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-950/50 dark:text-amber-300 dark:border-amber-700/40',
  Secret:        'bg-red-100 text-red-700 border-red-300 dark:bg-red-950/50 dark:text-red-400 dark:border-red-700/40',

  // Jobs
  Job:           'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  CronJob:       'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  Workflow:      'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  CronWorkflow:  'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  WorkflowTemplate: 'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  ClusterWorkflowTemplate: 'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',
  ScaledJob: 'bg-pink-100 text-pink-700 border-pink-300 dark:bg-pink-950/50 dark:text-pink-400 dark:border-pink-700/40',

  // Scaling & Storage
  HorizontalPodAutoscaler: 'bg-pink-100 text-pink-800 border-pink-300 dark:bg-pink-950/50 dark:text-pink-300 dark:border-pink-700/40',
  PersistentVolumeClaim:   'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  PodDisruptionBudget:     'bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-700/40',
  NetworkPolicy:                      'bg-indigo-100 text-indigo-800 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-300 dark:border-indigo-700/40',
  CiliumNetworkPolicy:                'bg-indigo-100 text-indigo-800 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-300 dark:border-indigo-700/40',
  CiliumClusterwideNetworkPolicy:     'bg-indigo-100 text-indigo-800 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-300 dark:border-indigo-700/40',
  ClusterNetworkPolicy:               'bg-violet-100 text-violet-800 border-violet-300 dark:bg-violet-950/50 dark:text-violet-300 dark:border-violet-700/40',

  // Cluster-scoped
  Node:          'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40',
  Namespace:     'bg-gray-100 text-gray-700 border-gray-300 dark:bg-gray-950/50 dark:text-gray-400 dark:border-gray-700/40',

  // GitOps
  Application:   'bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-700/40',
  Kustomization: 'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  GitRepository: 'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  HelmRelease:   'bg-purple-100 text-purple-700 border-purple-300 dark:bg-purple-950/50 dark:text-purple-400 dark:border-purple-700/40',

  // Knative
  KnativeService:       'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  KnativeConfiguration: 'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  KnativeRevision:      'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  KnativeRoute:         'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  Broker:               'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  Trigger:              'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  PingSource:           'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  ApiServerSource:      'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  ContainerSource:      'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  SinkBinding:          'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  Channel:              'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',

  // Traefik
  IngressRoute:         'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  IngressRouteTCP:      'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  IngressRouteUDP:      'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  Middleware:           'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  MiddlewareTCP:        'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  TraefikService:       'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  ServersTransport:     'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  ServersTransportTCP:  'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  TLSOption:            'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  TLSStore:             'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',

  // Contour
  HTTPProxy:            'bg-violet-100 text-violet-700 border-violet-300 dark:bg-violet-950/50 dark:text-violet-400 dark:border-violet-700/40',

  // Cluster API
  CAPICluster:          'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  MachineDeployment:    'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  MachineSet:           'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  Machine:              'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  MachinePool:          'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  KubeadmControlPlane:  'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  ClusterClass:         'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  MachineHealthCheck:   'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',

  // Events
  Event:                'bg-slate-100 text-slate-700 border-slate-300 dark:bg-slate-950/50 dark:text-slate-400 dark:border-slate-700/40',
}

const DEFAULT_KIND_COLOR = 'bg-fuchsia-50 text-fuchsia-700 border-fuchsia-200 dark:bg-fuchsia-950/40 dark:text-fuchsia-400 dark:border-fuchsia-800/40'

// ---------------------------------------------------------------------------
// PROTOCOL COLORS — transport/scheme tags. Deliberately avoid severity hues
// (emerald/amber/orange/red/sky) so a protocol never reads as a status.
// ---------------------------------------------------------------------------
const PROTOCOL: Record<string, string> = {
  http:  'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  http2: 'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  h2:    'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  h2c:   'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  https: 'bg-teal-100 text-teal-800 border-teal-300 dark:bg-teal-950/50 dark:text-teal-400 dark:border-teal-700/40',
  tls:   'bg-teal-100 text-teal-800 border-teal-300 dark:bg-teal-950/50 dark:text-teal-400 dark:border-teal-700/40',
  tcp:   'bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40',
  udp:   'bg-violet-100 text-violet-700 border-violet-300 dark:bg-violet-950/50 dark:text-violet-400 dark:border-violet-700/40',
  grpc:  'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  'grpc-web': 'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
}
const DEFAULT_PROTOCOL_COLOR = 'bg-slate-100 text-slate-700 border-slate-300 dark:bg-slate-950/50 dark:text-slate-400 dark:border-slate-700/40'

// ---------------------------------------------------------------------------
// TONE COLORS — note / local-categorical accents / structural.
// ---------------------------------------------------------------------------
const TONE: Record<BadgeTone, string> = {
  // attention/FYI — distinct from severity so it doesn't read as warning/info
  note:       'bg-violet-100 text-violet-700 border-violet-300 dark:bg-violet-950/50 dark:text-violet-400 dark:border-violet-700/40',
  // three visually-distinct accents for local "tell siblings apart" use
  accent1:    'bg-blue-100 text-blue-700 border-blue-300 dark:bg-blue-950/50 dark:text-blue-400 dark:border-blue-700/40',
  accent2:    'bg-fuchsia-100 text-fuchsia-700 border-fuchsia-300 dark:bg-fuchsia-950/50 dark:text-fuchsia-400 dark:border-fuchsia-700/40',
  accent3:    'bg-teal-100 text-teal-800 border-teal-300 dark:bg-teal-950/50 dark:text-teal-400 dark:border-teal-700/40',
  // neutral data fragment (ports/paths/hosts/names)
  structural: 'bg-theme-elevated text-theme-text-secondary border-theme-border',
}

// Structure classes
const SIZE_CLASSES: Record<BadgeSize, string> = {
  default: 'badge',
  sm: 'badge-sm',
}

/** Resolve a protocol/scheme label (case-insensitive) to its color class. */
export function getProtocolColorClass(protocol: string): string {
  return PROTOCOL[protocol.toLowerCase().trim()] ?? DEFAULT_PROTOCOL_COLOR
}

/** Resolve kind color with fuzzy matching for plural/lowercase forms */
export function getKindColorClass(kind: string): string {
  // Direct match
  if (KIND[kind]) return KIND[kind]

  // Fuzzy: try capitalizing
  const capitalized = kind.charAt(0).toUpperCase() + kind.slice(1)
  if (KIND[capitalized]) return KIND[capitalized]

  // Fuzzy: strip trailing 's' for plural
  const singular = kind.endsWith('s') && !kind.endsWith('ss') ? kind.slice(0, -1) : kind
  const singularCap = singular.charAt(0).toUpperCase() + singular.slice(1)
  if (KIND[singularCap]) return KIND[singularCap]

  // Fuzzy: known plurals
  const pluralMap: Record<string, string> = {
    pods: 'Pod', deployments: 'Deployment', services: 'Service', ingresses: 'Ingress',
    gateways: 'Gateway', configmaps: 'ConfigMap', secrets: 'Secret',
    daemonsets: 'DaemonSet', statefulsets: 'StatefulSet', replicasets: 'ReplicaSet',
    jobs: 'Job', cronjobs: 'CronJob', nodes: 'Node', namespaces: 'Namespace',
    horizontalpodautoscalers: 'HorizontalPodAutoscaler',
    persistentvolumeclaims: 'PersistentVolumeClaim',
    poddisruptionbudgets: 'PodDisruptionBudget',
    networkpolicies: 'NetworkPolicy',
    ciliumnetworkpolicies: 'CiliumNetworkPolicy',
    ciliumclusterwidenetworkpolicies: 'CiliumClusterwideNetworkPolicy',
    clusternetworkpolicies: 'ClusterNetworkPolicy',
    rollouts: 'Rollout', httproutes: 'HTTPRoute', grpcroutes: 'GRPCRoute',
    events: 'Event', helmreleases: 'HelmRelease',
  }
  const mapped = pluralMap[kind.toLowerCase()]
  if (mapped && KIND[mapped]) return KIND[mapped]

  return DEFAULT_KIND_COLOR
}

export function getSeverityColorClass(severity: BadgeSeverity): string {
  return SEVERITY[severity]
}

/**
 * Badge component — the ONE source of truth for badge rendering.
 * Use severity for status badges, kind for resource type badges, or colorClass for custom.
 */
export function Badge({ severity, kind, protocol, tone, colorClass, size = 'default', className, onClick, title, children }: BadgeProps) {
  const color =
    colorClass ??
    (severity ? SEVERITY[severity]
      : protocol ? getProtocolColorClass(protocol)
      : tone ? TONE[tone]
      : kind ? getKindColorClass(kind)
      : '')
  const cls = clsx(SIZE_CLASSES[size], color, className)

  if (onClick) {
    return <button onClick={onClick} className={cls} title={title}>{children}</button>
  }
  return <span className={cls} title={title}>{children}</span>
}

// Re-export the raw color maps for backwards compat (used by badge-colors.ts consumers)
export { SEVERITY as BADGE_SEVERITY_COLORS, KIND as BADGE_KIND_COLORS, PROTOCOL as BADGE_PROTOCOL_COLORS, TONE as BADGE_TONE_COLORS }
