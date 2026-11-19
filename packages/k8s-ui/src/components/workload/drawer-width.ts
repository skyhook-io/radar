// Widths for the resource detail drawer, from the room it has: the content area
// beside the nav rail, less any docked right-side panel. The drawer overlays the
// view behind it rather than pushing it, so every pixel it takes is a pixel of
// list the user can no longer see.

export const DRAWER_MIN_WIDTH = 520
const DRAWER_MAX_SHARE = 0.7

const BASE_WIDTH = 550
const WIDE_WIDTH = 750
// Kept for the view behind: the Resources kind list (224–288px) plus a table's
// Name, Namespace and two status columns at their minimum widths (~700px). The
// drawer only grows into room beyond that, so up to ~1700px windows it stays at
// 550. The table spreads leftover room over all its columns, so on wide screens
// its later columns sit under the drawer whatever this reserve, unless the
// table lays itself out for the part the drawer leaves visible.
const LIST_RESERVE = 1000
// Past this a drawer's label/value rows only get sparser; a user who wants more
// width wants the full page.
const BASE_CAP = 900
const WIDE_CAP = 1100

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
  // Report kinds start wider, but grow into the same spare room: the list
  // behind keeps its reserve either way.
  const [base, cap] = isWideDrawerKind(kind) ? [WIDE_WIDTH, WIDE_CAP] : [BASE_WIDTH, BASE_CAP]
  return clampDrawerWidth(Math.min(cap, Math.max(base, available - LIST_RESERVE)), available)
}
