// Centralized badge color utilities for consistent light/dark mode theming
// This file is the single source of truth for all badge colors across the application

// Kind badge colors - for K8s resource type badges
export const KIND_BADGE_COLORS: Record<string, string> = {
  // Workload controllers
  Deployment: 'bg-emerald-500/15 text-emerald-700 dark:bg-emerald-900/50 dark:text-emerald-300',
  StatefulSet: 'bg-cyan-500/15 text-cyan-700 dark:bg-cyan-900/50 dark:text-cyan-300',
  DaemonSet: 'bg-teal-500/15 text-teal-700 dark:bg-teal-900/50 dark:text-teal-300',
  ReplicaSet: 'bg-green-500/15 text-green-700 dark:bg-green-900/50 dark:text-green-300',
  Pod: 'bg-lime-500/15 text-lime-700 dark:bg-lime-900/50 dark:text-lime-300',

  // Networking
  Service: 'bg-blue-500/15 text-blue-700 dark:bg-blue-900/50 dark:text-blue-300',
  Ingress: 'bg-violet-500/15 text-violet-700 dark:bg-violet-900/50 dark:text-violet-300',
  Gateway: 'bg-violet-500/15 text-violet-700 dark:bg-violet-900/50 dark:text-violet-300',
  HTTPRoute: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',
  GRPCRoute: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',
  TCPRoute: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',
  TLSRoute: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',

  // Config
  ConfigMap: 'bg-amber-500/15 text-amber-700 dark:bg-amber-900/50 dark:text-amber-300',
  Secret: 'bg-red-500/15 text-red-700 dark:bg-red-900/50 dark:text-red-300',

  // Jobs
  Job: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',
  CronJob: 'bg-purple-500/15 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300',

  // Autoscaling & Storage
  HorizontalPodAutoscaler: 'bg-pink-500/15 text-pink-700 dark:bg-pink-900/50 dark:text-pink-300',
  PersistentVolumeClaim: 'bg-cyan-500/15 text-cyan-700 dark:bg-cyan-900/50 dark:text-cyan-300',

  // Special
  HelmRelease: 'bg-purple-500/20 text-purple-700 dark:text-purple-300',
  Event: 'bg-slate-500/15 text-slate-700 dark:bg-slate-900/50 dark:text-slate-300',
}

// Kind badge colors with border - for prominent badges in drawers/modals
export const KIND_BADGE_BORDERED: Record<string, string> = {
  // Workload controllers
  Deployment: 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30',
  StatefulSet: 'bg-cyan-500/20 text-cyan-700 dark:text-cyan-300 border border-cyan-500/30',
  DaemonSet: 'bg-teal-500/20 text-teal-700 dark:text-teal-300 border border-teal-500/30',
  ReplicaSet: 'bg-green-500/20 text-green-700 dark:text-green-300 border border-green-500/30',
  Pod: 'bg-lime-500/20 text-lime-700 dark:text-lime-300 border border-lime-500/30',
  PodGroup: 'bg-lime-500/20 text-lime-700 dark:text-lime-300 border border-lime-500/30',

  // Networking
  Internet: 'bg-blue-500/20 text-blue-700 dark:text-blue-300 border border-blue-500/30',
  Service: 'bg-blue-500/20 text-blue-700 dark:text-blue-300 border border-blue-500/30',
  Ingress: 'bg-violet-500/20 text-violet-700 dark:text-violet-300 border border-violet-500/30',
  Gateway: 'bg-violet-500/20 text-violet-700 dark:text-violet-300 border border-violet-500/30',
  HTTPRoute: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',
  GRPCRoute: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',
  TCPRoute: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',
  TLSRoute: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',

  // Config
  ConfigMap: 'bg-amber-500/20 text-amber-700 dark:text-amber-300 border border-amber-500/30',
  Secret: 'bg-red-500/20 text-red-700 dark:text-red-300 border border-red-500/30',

  // Jobs
  Job: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',
  CronJob: 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border border-purple-500/30',

  // Autoscaling & Storage
  HorizontalPodAutoscaler: 'bg-pink-500/20 text-pink-700 dark:text-pink-300 border border-pink-500/30',
  PersistentVolumeClaim: 'bg-cyan-500/20 text-cyan-700 dark:text-cyan-300 border border-cyan-500/30',
}

// Event type colors - for K8s event types (Normal, Warning)
export const EVENT_TYPE_COLORS: Record<string, string> = {
  Warning: 'bg-amber-500/20 text-amber-700 dark:text-amber-300',
  Normal: 'bg-green-500/20 text-green-700 dark:text-green-300',
}

// Operation colors - for change events (add, update, delete)
export const OPERATION_COLORS: Record<string, string> = {
  add: 'text-green-700 dark:text-green-300',
  update: 'text-blue-700 dark:text-blue-300',
  delete: 'text-red-700 dark:text-red-300',
}

// Operation background colors - for badges with background
export const OPERATION_BADGE_COLORS: Record<string, string> = {
  add: 'bg-green-500/20 text-green-700 dark:text-green-300',
  update: 'bg-blue-500/20 text-blue-700 dark:text-blue-300',
  delete: 'bg-red-500/20 text-red-700 dark:text-red-300',
}

// Health badge colors - for health state indicators
export const HEALTH_BADGE_COLORS: Record<string, string> = {
  healthy: 'bg-green-500/20 text-green-700 dark:text-green-300',
  degraded: 'bg-yellow-500/20 text-yellow-700 dark:text-yellow-300',
  unhealthy: 'bg-red-500/20 text-red-700 dark:text-red-300',
  unknown: 'bg-theme-hover/50 text-theme-text-secondary',
}

// Helm release status colors
export const HELM_STATUS_COLORS: Record<string, string> = {
  deployed: 'bg-green-500/20 text-green-700 dark:text-green-300',
  superseded: 'bg-theme-hover/50 text-theme-text-secondary',
  failed: 'bg-red-500/20 text-red-700 dark:text-red-300',
  'pending-install': 'bg-yellow-500/20 text-yellow-700 dark:text-yellow-300',
  'pending-upgrade': 'bg-yellow-500/20 text-yellow-700 dark:text-yellow-300',
  'pending-rollback': 'bg-yellow-500/20 text-yellow-700 dark:text-yellow-300',
  uninstalling: 'bg-orange-500/20 text-orange-700 dark:text-orange-300',
  uninstalled: 'bg-theme-hover/50 text-theme-text-secondary',
}

// Default fallback color
export const DEFAULT_BADGE_COLOR = 'bg-theme-elevated text-theme-text-secondary'

// =============================================================================
// SEVERITY COLORS - for status indicators, alerts, and feedback
// =============================================================================

// Severity badge colors - with background (for badges, pills, tags)
export const SEVERITY_BADGE = {
  success: 'bg-green-500/20 text-green-700 dark:text-green-300',
  warning: 'bg-amber-500/20 text-amber-700 dark:text-amber-300',
  error: 'bg-red-500/20 text-red-700 dark:text-red-300',
  info: 'bg-blue-500/20 text-blue-700 dark:text-blue-300',
  neutral: 'bg-theme-hover/50 text-theme-text-secondary',
} as const

// Severity text colors - without background (for inline text, icons)
export const SEVERITY_TEXT = {
  success: 'text-green-700 dark:text-green-300',
  warning: 'text-amber-700 dark:text-amber-300',
  error: 'text-red-700 dark:text-red-300',
  info: 'text-blue-700 dark:text-blue-300',
  neutral: 'text-theme-text-secondary',
} as const

// Severity dot/indicator colors - solid colors for small status dots
export const SEVERITY_DOT = {
  success: 'bg-green-500',
  warning: 'bg-yellow-500',
  error: 'bg-red-500',
  info: 'bg-blue-500',
  neutral: 'bg-theme-hover',
} as const

// Severity border colors - for bordered elements
export const SEVERITY_BORDER = {
  success: 'border-green-500/30',
  warning: 'border-amber-500/30',
  error: 'border-red-500/30',
  info: 'border-blue-500/30',
  neutral: 'border-theme-border',
} as const

// Combined severity styles - badge with border (for prominent indicators)
export const SEVERITY_BADGE_BORDERED = {
  success: 'bg-green-500/20 text-green-700 dark:text-green-300 border border-green-500/30',
  warning: 'bg-amber-500/20 text-amber-700 dark:text-amber-300 border border-amber-500/30',
  error: 'bg-red-500/20 text-red-700 dark:text-red-300 border border-red-500/30',
  info: 'bg-blue-500/20 text-blue-700 dark:text-blue-300 border border-blue-500/30',
  neutral: 'bg-theme-hover/50 text-theme-text-secondary border border-theme-border',
} as const

// Severity type
export type Severity = 'success' | 'warning' | 'error' | 'info' | 'neutral'

// =============================================================================
// RESOURCE STATUS COLORS - for K8s resource states
// =============================================================================

// Pod/workload status colors - maps common K8s status strings to severity
export const RESOURCE_STATUS_COLORS: Record<string, string> = {
  // Success states
  running: SEVERITY_BADGE.success,
  active: SEVERITY_BADGE.success,
  succeeded: SEVERITY_BADGE.success,
  bound: SEVERITY_BADGE.success,
  ready: SEVERITY_BADGE.success,
  available: SEVERITY_BADGE.success,

  // Warning states
  pending: SEVERITY_BADGE.warning,
  progressing: SEVERITY_BADGE.warning,
  suspended: SEVERITY_BADGE.warning,
  'scaled to 0': SEVERITY_BADGE.warning,
  waiting: SEVERITY_BADGE.warning,

  // Error states
  failed: SEVERITY_BADGE.error,
  error: SEVERITY_BADGE.error,
  crashloopbackoff: SEVERITY_BADGE.error,
  imagepullbackoff: SEVERITY_BADGE.error,
  evicted: SEVERITY_BADGE.error,
  oomkilled: SEVERITY_BADGE.error,

  // Info/completed states
  completed: SEVERITY_BADGE.info,
  terminated: SEVERITY_BADGE.info,

  // Unknown/neutral
  unknown: SEVERITY_BADGE.neutral,
}

// Helper functions

/**
 * Get severity badge classes (with background)
 */
export function getSeverityBadge(severity: Severity): string {
  return SEVERITY_BADGE[severity]
}

/**
 * Get severity text classes (no background)
 */
export function getSeverityText(severity: Severity): string {
  return SEVERITY_TEXT[severity]
}

/**
 * Get severity dot classes (solid background for indicators)
 */
export function getSeverityDot(severity: Severity): string {
  return SEVERITY_DOT[severity]
}

/**
 * Get severity badge with border classes
 */
export function getSeverityBadgeBordered(severity: Severity): string {
  return SEVERITY_BADGE_BORDERED[severity]
}

/**
 * Get resource status badge color from a status string
 * Automatically maps common K8s status strings to appropriate colors
 */
export function getResourceStatusColor(status: string): string {
  if (!status) return SEVERITY_BADGE.neutral
  const statusLower = status.toLowerCase()
  return RESOURCE_STATUS_COLORS[statusLower] || SEVERITY_BADGE.neutral
}

/**
 * Map a health/status level to a severity
 */
export function healthToSeverity(health: string): Severity {
  switch (health.toLowerCase()) {
    case 'healthy':
    case 'success':
    case 'running':
    case 'ready':
      return 'success'
    case 'degraded':
    case 'warning':
    case 'pending':
      return 'warning'
    case 'unhealthy':
    case 'error':
    case 'failed':
      return 'error'
    case 'info':
      return 'info'
    default:
      return 'neutral'
  }
}

// =============================================================================
// LEGACY HELPER FUNCTIONS - for backward compatibility
// =============================================================================

/**
 * Get the badge color classes for a K8s resource kind
 */
export function getKindBadgeColor(kind: string): string {
  return KIND_BADGE_COLORS[kind] || DEFAULT_BADGE_COLOR
}

/**
 * Get the badge color classes with border for a K8s resource kind
 */
export function getKindBadgeBordered(kind: string): string {
  return KIND_BADGE_BORDERED[kind] || 'bg-theme-hover/50 text-theme-text-secondary border border-theme-border'
}

/**
 * Get the badge color classes for a K8s event type (Normal, Warning)
 */
export function getEventTypeColor(eventType: string): string {
  return EVENT_TYPE_COLORS[eventType] || DEFAULT_BADGE_COLOR
}

/**
 * Get the text color classes for a change operation (add, update, delete)
 */
export function getOperationColor(operation: string): string {
  return OPERATION_COLORS[operation] || 'text-theme-text-secondary'
}

/**
 * Get the badge color classes for a change operation (add, update, delete)
 */
export function getOperationBadgeColor(operation: string): string {
  return OPERATION_BADGE_COLORS[operation] || DEFAULT_BADGE_COLOR
}

/**
 * Get the badge color classes for a health state
 */
export function getHealthBadgeColor(healthState: string): string {
  return HEALTH_BADGE_COLORS[healthState] || HEALTH_BADGE_COLORS.unknown
}

/**
 * Get the badge color classes for a Helm release status
 */
export function getHelmStatusColor(status: string): string {
  const statusLower = status.toLowerCase()
  return HELM_STATUS_COLORS[statusLower] || 'bg-theme-hover/50 text-theme-text-secondary'
}
