// Centralized badge color utilities for consistent light/dark mode theming
// Color definitions live in Badge.tsx (static strings for Tailwind scanning)
// This file re-exports them and provides helper functions for backwards compat

import { BADGE_SEVERITY_COLORS, BADGE_KIND_COLORS, getKindColorClass } from '../components/ui/Badge'

// Kind badge colors - re-exported from Badge.tsx (static strings, Tailwind-safe)
export const KIND_BADGE_COLORS: Record<string, string> = BADGE_KIND_COLORS

// Kind badge bordered - same as KIND_BADGE_COLORS (colors already include border classes)
export const KIND_BADGE_BORDERED: Record<string, string> = BADGE_KIND_COLORS

export { getKindColorClass }

// Event type colors - for K8s event types (Normal, Warning)
export const EVENT_TYPE_COLORS: Record<string, string> = {
  Warning: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950/40 dark:text-amber-400 dark:border-amber-800/40',
  Normal: 'bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950/40 dark:text-emerald-400 dark:border-emerald-800/40',
}

// Operation colors - for change events (add, update, delete)
export const OPERATION_COLORS: Record<string, string> = {
  add: 'text-emerald-700 dark:text-emerald-400',
  update: 'text-sky-700 dark:text-sky-400',
  delete: 'text-red-700 dark:text-red-400',
}

// Operation background colors - derived from SEVERITY
export const OPERATION_BADGE_COLORS: Record<string, string> = {
  add: BADGE_SEVERITY_COLORS.success,
  update: BADGE_SEVERITY_COLORS.info,
  delete: BADGE_SEVERITY_COLORS.error,
}

// Health badge colors - derived from SEVERITY
export const HEALTH_BADGE_COLORS: Record<string, string> = {
  healthy: BADGE_SEVERITY_COLORS.success,
  degraded: BADGE_SEVERITY_COLORS.warning,
  unhealthy: BADGE_SEVERITY_COLORS.error,
  unknown: BADGE_SEVERITY_COLORS.neutral,
}

// Helm release status colors - derived from SEVERITY
export const HELM_STATUS_COLORS: Record<string, string> = {
  deployed: BADGE_SEVERITY_COLORS.success,
  superseded: BADGE_SEVERITY_COLORS.neutral,
  failed: BADGE_SEVERITY_COLORS.error,
  'pending-install': BADGE_SEVERITY_COLORS.warning,
  'pending-upgrade': BADGE_SEVERITY_COLORS.warning,
  'pending-rollback': BADGE_SEVERITY_COLORS.warning,
  uninstalling: BADGE_SEVERITY_COLORS.warning,
  uninstalled: BADGE_SEVERITY_COLORS.neutral,
}

// Default fallback color
export const DEFAULT_BADGE_COLOR = 'bg-theme-elevated text-theme-text-secondary'

// =============================================================================
// SEVERITY COLORS - for status indicators, alerts, and feedback
// =============================================================================

// Severity badge colors — re-exported from Badge.tsx (static strings, Tailwind-safe)
export const SEVERITY_BADGE = BADGE_SEVERITY_COLORS

// Severity text colors - without background (for inline text, icons)
export const SEVERITY_TEXT = {
  success: 'text-emerald-700 dark:text-emerald-400',
  warning: 'text-amber-700 dark:text-amber-400',
  error: 'text-red-700 dark:text-red-400',
  info: 'text-sky-700 dark:text-sky-400',
  neutral: 'text-theme-text-secondary',
} as const

// Severity dot/indicator colors - solid colors for small status dots
export const SEVERITY_DOT = {
  success: 'bg-emerald-500',
  warning: 'bg-amber-500',
  error: 'bg-red-500',
  info: 'bg-sky-500',
  neutral: 'bg-theme-hover',
} as const

// Severity border colors - for bordered elements
export const SEVERITY_BORDER = {
  success: 'border-emerald-200 dark:border-emerald-800/40',
  warning: 'border-amber-200 dark:border-amber-800/40',
  error: 'border-red-200 dark:border-red-800/40',
  info: 'border-sky-200 dark:border-sky-800/40',
  neutral: 'border-theme-border',
} as const

// Combined severity styles - same as SEVERITY_BADGE (border comes from .badge class + border-color in color string)
export const SEVERITY_BADGE_BORDERED = {
  ...BADGE_SEVERITY_COLORS,
  debug: 'bg-gray-50 text-gray-600 border-gray-200 dark:bg-gray-900/30 dark:text-gray-400 dark:border-gray-700/40',
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

// =============================================================================
// VULNERABILITY SEVERITY COLORS - for Trivy and other security scanners
// =============================================================================

export const VULN_SEVERITY_BADGE: Record<string, string> = {
  CRITICAL: 'bg-red-100 text-red-700 border-red-300 dark:bg-red-950/50 dark:text-red-400 dark:border-red-700/40',
  HIGH: 'bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-700/40',
  MEDIUM: 'bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-950/50 dark:text-amber-300 dark:border-amber-700/40',
  LOW: 'bg-sky-100 text-sky-700 border-sky-300 dark:bg-sky-950/50 dark:text-sky-400 dark:border-sky-700/40',
  UNKNOWN: 'bg-gray-100 text-gray-700 border-gray-300 dark:bg-gray-950/50 dark:text-gray-400 dark:border-gray-700/40',
}

export const VULN_SEVERITY_BAR: Record<string, string> = {
  CRITICAL: 'bg-red-500',
  HIGH: 'bg-orange-500',
  MEDIUM: 'bg-yellow-500',
  LOW: 'bg-blue-500',
  UNKNOWN: 'bg-gray-500',
}

export const VULN_SEVERITY_TEXT: Record<string, string> = {
  CRITICAL: 'text-red-800 dark:text-red-400',
  HIGH: 'text-orange-700 dark:text-orange-400',
  MEDIUM: 'text-yellow-700 dark:text-yellow-400',
  LOW: 'text-blue-700 dark:text-blue-400',
  UNKNOWN: 'text-gray-600 dark:text-gray-400',
}
