// Theme-aware badge classes for RBAC display (verbs, resources, API groups).
// Reuses the same light/dark pairs as Badge.tsx's SEVERITY/KIND maps so the
// RBAC views read with the same contrast as the rest of the app — the
// inherited `text-*-400` foreground without a light counterpart was washing
// out on light backgrounds.
//
// Centralized here so SA / Pod / RoleBinding renderers and the
// MyPermissions dialog all stay in sync; previous duplicate copies drifted.

import { BADGE_SEVERITY_COLORS, BADGE_KIND_COLORS } from '../components/ui/Badge'
import {
  RBAC_BLAST_READ_VERBS,
  RBAC_BLAST_WRITE_VERBS,
  RBAC_BLAST_DELETE_VERBS,
  RBAC_BLAST_ESCALATION_VERBS,
} from './rbac-blast-radius'

/** Theme-aware classes for an RBAC verb chip. */
export function rbacVerbBadgeClass(verb: string): string {
  if (verb === '*') return BADGE_SEVERITY_COLORS.error
  if (RBAC_BLAST_ESCALATION_VERBS.has(verb)) return BADGE_SEVERITY_COLORS.error
  if (RBAC_BLAST_DELETE_VERBS.has(verb)) return BADGE_SEVERITY_COLORS.error
  if (RBAC_BLAST_WRITE_VERBS.has(verb)) return BADGE_SEVERITY_COLORS.warning
  if (RBAC_BLAST_READ_VERBS.has(verb)) return BADGE_SEVERITY_COLORS.success
  return BADGE_SEVERITY_COLORS.info
}

/** Theme-aware classes for an RBAC resource chip (pods, configmaps, …). */
export const rbacResourceBadgeClass: string =
  BADGE_KIND_COLORS.HTTPRoute // purple-100/-700 in light, purple-950/-400 in dark

/**
 * Theme-aware kind badge for the four RBAC kinds. We can't put these in
 * Badge.tsx's KIND map without churn there, so reuse the closest existing
 * palette entry: blue/info for namespaced (Role/RoleBinding), purple for
 * cluster-scoped (ClusterRole/ClusterRoleBinding).
 */
export function rbacKindBadgeClass(kind: string): string {
  switch (kind) {
    case 'Role':
    case 'RoleBinding':
      return BADGE_KIND_COLORS.Service // blue-100/-700 light, blue-950/-400 dark
    case 'ClusterRole':
    case 'ClusterRoleBinding':
      return BADGE_KIND_COLORS.HelmRelease // purple-100/-700 light, purple-950/-400 dark
    default:
      return BADGE_KIND_COLORS.HelmRelease
  }
}

/** Theme-aware classes for an API group chip (notification.toolkit.fluxcd.io, …). */
export const rbacApiGroupBadgeClass: string =
  'bg-theme-hover/60 text-theme-text-secondary border-theme-border'

/** Theme-aware classes for a resourceName chip (rare — `resourceNames: [...]`). */
export const rbacResourceNameBadgeClass: string =
  BADGE_KIND_COLORS.PersistentVolumeClaim // cyan, distinct from resources

/** Theme-aware classes for a non-resource URL chip (/healthz etc). */
export const rbacNonResourceUrlBadgeClass: string =
  BADGE_KIND_COLORS.Application // orange, distinct from everything above
