// PROTOTYPE ONLY (workspace navigation options A–E). Not for merge.
// One module-level store so the sidebar, the workspace frame, the rail and the
// switcher all read the same option without threading props through k8s-ui.
import { useSyncExternalStore } from 'react'

export type NavProtoMode = 'off' | 'A' | 'B' | 'C' | 'D' | 'E'

export const NAV_PROTO_OPTIONS: { mode: NavProtoMode; label: string; short: string }[] = [
  { mode: 'off', label: 'Today', short: 'Integrations block inside Resources' },
  { mode: 'A', label: 'A · Tabs', short: 'Tabs across the top of Resources' },
  { mode: 'B', label: 'B · Sections', short: 'Domain sections replace Resources' },
  { mode: 'C', label: 'C · Own page', short: 'Own page, with a deliberate exit from Resources' },
  { mode: 'D', label: 'D · Apps', short: 'Separate apps behind a switcher' },
  { mode: 'E', label: 'E · No nav', short: 'No workspace in navigation; Home cards and rich lists' },
]

type State = { mode: NavProtoMode; section: string | null }
const KEY = 'radar.navProto'
const listeners = new Set<() => void>()

function read(): State {
  let mode: NavProtoMode = 'off'
  try {
    const fromUrl = new URLSearchParams(window.location.search).get('navproto')
    const stored = fromUrl ?? window.localStorage.getItem(KEY)
    if (stored && NAV_PROTO_OPTIONS.some((o) => o.mode === stored)) mode = stored as NavProtoMode
    if (fromUrl) window.localStorage.setItem(KEY, mode)
  } catch { /* storage can be denied; the prototype then starts at Today */ }
  return { mode, section: null }
}

let state: State = typeof window === 'undefined' ? { mode: 'off', section: null } : read()

function emit(next: State) {
  state = next
  try { window.localStorage.setItem(KEY, next.mode) } catch { /* ignore */ }
  listeners.forEach((l) => l())
}

export function setNavProtoMode(mode: NavProtoMode) { emit({ mode, section: null }) }
export function setNavProtoSection(section: string | null) { if (state.section !== section) emit({ ...state, section }) }

export function useNavProto(): State {
  return useSyncExternalStore(
    (l) => { listeners.add(l); return () => listeners.delete(l) },
    () => state,
    () => state,
  )
}

/** The Resources categories option B turns into rail sections, in rail order. */
export const PROTO_CORE_SECTIONS = ['Workloads', 'Networking', 'Configuration', 'Storage', 'Access Control', 'Cluster'] as const
export const PROTO_OTHER_SECTION = 'Other APIs'
