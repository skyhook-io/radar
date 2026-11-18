// Widths for the resource detail drawer, from the room it has: the content area
// beside the nav rail, less any docked right-side panel. The drawer overlays the
// view behind it rather than pushing it, so every pixel it takes is a pixel of
// list the user can no longer see.

export const DRAWER_MIN_WIDTH = 520
const DRAWER_MAX_SHARE = 0.7

const BASE_WIDTH = 550
const WIDE_EXTRA = 200
// Kept for the view behind: the Resources kind list (288px) plus a table's Name,
// Namespace and two status columns at their minimum widths (~700px). The drawer
// only grows into room beyond that, so up to ~1700px windows it stays at 550.
// The table stretches its Name column across the whole content area, so on wide
// screens the columns right of Name sit under the drawer whatever this reserve.
const LIST_RESERVE = 1000
// Past this a drawer's label/value rows only get sparser; a user who wants more
// width wants the full page.
const BASE_CAP = 900

const WIDE_KINDS = new Set([
  'vulnerabilityreports', 'configauditreports', 'exposedsecretreports',
  'rbacassessmentreports', 'clusterrbacassessmentreports', 'clustercompliancereports',
  'sbomreports', 'clustersbomreports', 'policyreports', 'clusterpolicyreports',
])

export function isWideDrawerKind(kind: string): boolean {
  return WIDE_KINDS.has(kind.toLowerCase())
}

export function drawerWidthBounds(available: number): { min: number; max: number } {
  return { min: DRAWER_MIN_WIDTH, max: Math.max(DRAWER_MIN_WIDTH, Math.round(available * DRAWER_MAX_SHARE)) }
}

export function clampDrawerWidth(width: number, available: number): number {
  const { min, max } = drawerWidthBounds(available)
  return Math.max(min, Math.min(width, max))
}

export function defaultDrawerWidth(kind: string, available: number): number {
  const grown = Math.min(BASE_CAP, Math.max(BASE_WIDTH, available - LIST_RESERVE))
  const width = isWideDrawerKind(kind) ? grown + WIDE_EXTRA : grown
  return clampDrawerWidth(width, available)
}
