import { clsx } from 'clsx'
import type { ReactNode } from 'react'

// =============================================================================
// BADGE COMPONENT — single source of truth for all badge rendering
// All class names are STATIC STRINGS (required for Tailwind content scanning)
// =============================================================================

export type BadgeSeverity = 'success' | 'warning' | 'error' | 'info' | 'neutral'
export type BadgeSize = 'sm' | 'default'

interface BadgeProps {
  /** Severity-based coloring (status badges) */
  severity?: BadgeSeverity
  /** K8s resource kind coloring (kind badges) */
  kind?: string
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

  // Scaling & Storage
  HorizontalPodAutoscaler: 'bg-pink-100 text-pink-800 border-pink-300 dark:bg-pink-950/50 dark:text-pink-300 dark:border-pink-700/40',
  PersistentVolumeClaim:   'bg-cyan-100 text-cyan-800 border-cyan-300 dark:bg-cyan-950/50 dark:text-cyan-400 dark:border-cyan-700/40',
  PodDisruptionBudget:     'bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-700/40',

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

  // Events
  Event:                'bg-slate-100 text-slate-700 border-slate-300 dark:bg-slate-950/50 dark:text-slate-400 dark:border-slate-700/40',
}

const DEFAULT_KIND_COLOR = 'bg-fuchsia-50 text-fuchsia-700 border-fuchsia-200 dark:bg-fuchsia-950/40 dark:text-fuchsia-400 dark:border-fuchsia-800/40'

// Structure classes
const SIZE_CLASSES: Record<BadgeSize, string> = {
  default: 'badge',
  sm: 'badge-sm',
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
export function Badge({ severity, kind, colorClass, size = 'default', className, onClick, title, children }: BadgeProps) {
  const color = colorClass ?? (severity ? SEVERITY[severity] : kind ? getKindColorClass(kind) : '')
  const cls = clsx(SIZE_CLASSES[size], color, className)

  if (onClick) {
    return <button onClick={onClick} className={cls} title={title}>{children}</button>
  }
  return <span className={cls} title={title}>{children}</span>
}

// Re-export the raw color maps for backwards compat (used by badge-colors.ts consumers)
export { SEVERITY as BADGE_SEVERITY_COLORS, KIND as BADGE_KIND_COLORS }
