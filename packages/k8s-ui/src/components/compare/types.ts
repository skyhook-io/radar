export interface NamespacedRef {
  namespace: string
  name: string
  /**
   * Optional cluster scope. Populated by cross-cluster compare flows
   * (Radar Hub fleet search) so the same `kube-system/coredns` in two
   * different clusters can both be picked. Undefined in OSS single-
   * cluster compare; equality then falls back to namespace+name only.
   */
  clusterId?: string
  /** Display name for the cluster — surfaced in tray pills + diff side titles. */
  clusterName?: string
}

export type CompareSide = 'a' | 'b'

/** Per-side palette. One place for the A/B colors so the picker chip, drawer pill,
 *  tray pill, and table row highlight stay in sync if the palette ever changes. */
export const SIDE_TONES: Record<CompareSide, {
  /** Small label chip: filled background. */
  chipBg: string
  /** Outlined container: border + tint. */
  containerBorder: string
  containerBg: string
  /** Row-level highlight in the resources table. */
  rowBg: string
  rowBgHover: string
}> = {
  a: {
    chipBg: 'bg-blue-400/90 text-blue-950',
    containerBorder: 'border-blue-400/40',
    containerBg: 'bg-blue-500/10',
    rowBg: 'bg-blue-500/15',
    rowBgHover: 'group-hover/row:bg-blue-500/25',
  },
  b: {
    chipBg: 'bg-emerald-400/90 text-emerald-950',
    containerBorder: 'border-emerald-400/40',
    containerBg: 'bg-emerald-500/10',
    rowBg: 'bg-emerald-500/15',
    rowBgHover: 'group-hover/row:bg-emerald-500/25',
  },
}
