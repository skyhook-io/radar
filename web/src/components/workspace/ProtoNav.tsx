// PROTOTYPE ONLY (workspace navigation options A–E). Not for merge.
import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import { ArrowLeft, ChevronDown, Compass, LayoutGrid } from 'lucide-react'
import {
  NAV_PROTO_OPTIONS,
  setNavProtoMode,
  useNavProto,
  type SelectedKindInfo,
  type SidebarCategoryDestination,
  type SidebarCategoryWorkspace,
} from '@skyhook-io/k8s-ui'
import { useAPIResources } from '../../api/apiResources'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { useVeleroSidebarWorkspace } from '../velero/useVeleroSidebarWorkspace'
import { useMergedSidebarWorkspaces } from './sidebar'

export type ProtoWorkspaces = Record<string, SidebarCategoryWorkspace> | undefined

const GROUP_CATEGORY: Record<string, string> = {
  'postgresql.cnpg.io': 'CloudNativePG',
  'barmancloud.cnpg.io': 'CloudNativePG',
  'velero.io': 'Velero',
}
// CloudNativePG kinds that have no screen of their own, mapped to the screen that lists them.
const CNPG_KIND_SCREEN: Record<string, string> = {
  backups: 'protection', scheduledbackups: 'protection', objectstores: 'protection',
  poolers: 'pooling', databases: 'declarations', publications: 'declarations', subscriptions: 'declarations',
}

export function useProtoWorkspaces(namespaces: string[]): ProtoWorkspaces {
  const { data: apiResources } = useAPIResources()
  const cnpg = useCNPGSidebarWorkspace({ apiResources, namespaces })
  const velero = useVeleroSidebarWorkspace({ apiResources })
  return useMergedSidebarWorkspaces(cnpg, velero)
}

export function protoCategoryForKind(map: ProtoWorkspaces, kind: SelectedKindInfo | null): string | null {
  if (!map || !kind) return null
  for (const [name, ws] of Object.entries(map)) {
    if (ws.destinations.some((d) => d.kind && d.kind.name === kind.name && d.kind.group === kind.group)) return name
  }
  const cat = GROUP_CATEGORY[kind.group]
  return cat && map[cat] ? cat : null
}

/** The screen a kind belongs to: its own list screen, or the screen that lists it. */
export function protoDestinationForKind(map: ProtoWorkspaces, kind: SelectedKindInfo | null): SidebarCategoryDestination | null {
  const cat = protoCategoryForKind(map, kind)
  if (!cat || !map || !kind) return null
  const ws = map[cat]
  const exact = ws.destinations.find((d) => d.kind && d.kind.name === kind.name && d.kind.group === kind.group)
  if (exact) return exact
  const want = cat === 'CloudNativePG' ? CNPG_KIND_SCREEN[kind.name] : undefined
  return (want && ws.destinations.find((d) => d.id === want)) || null
}

export function protoActiveCategory(map: ProtoWorkspaces): string | null {
  for (const [name, ws] of Object.entries(map ?? {})) if (ws.destinations.some((d) => d.active)) return name
  return null
}

/** Which workspace a URL belongs to, for the rail and the app switcher. */
export function protoWorkspaceForLocation(pathname: string, search: string): string | null {
  if (pathname === '/cnpg' || pathname.startsWith('/cnpg/')) return 'CloudNativePG'
  if (pathname === '/velero' || pathname.startsWith('/velero/')) return 'Velero'
  const m = pathname.match(/^\/resources\/([^/]+)/)
  if (m) {
    const group = new URLSearchParams(search).get('apiGroup') ?? ''
    if (group === 'velero.io' && ['schedules', 'backupstoragelocations', 'backups', 'restores'].includes(m[1])) return 'Velero'
  }
  return null
}

function Count({ n }: { n?: number | null }) {
  if (!n) return null
  return <span className="ml-1.5 rounded bg-amber-500/15 px-1.5 text-[11px] font-mono text-amber-600 dark:text-amber-300">{n}</span>
}

/** Option A: the tab strip across the top of Resources. */
export function ProtoWorkspaceTabs({ map, current }: { map: ProtoWorkspaces; current: string | null }) {
  const navigate = useNavigate()
  const entries = Object.entries(map ?? {})
  return (
    <div className="flex shrink-0 items-end gap-1 border-b border-theme-border bg-theme-surface px-4 pt-2">
      {[['', null] as const, ...entries].map(([name, ws]) => {
        const on = (name || null) === current
        const Icon = ws?.icon ?? LayoutGrid
        return (
          <button
            key={name || 'all'}
            type="button"
            onClick={() => (ws ? ws.destinations[0]?.onSelect() : navigate('/resources'))}
            className={clsx(
              '-mb-px flex items-center gap-2 rounded-t-lg border px-3.5 py-2 text-sm font-medium transition-colors',
              on ? 'border-theme-border border-b-theme-base bg-theme-base text-theme-text-primary' : 'border-transparent text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover',
            )}
          >
            <Icon className="h-4 w-4" />
            {ws ? ws.label ?? name : 'All kinds'}
            {ws && <Count n={ws.count} />}
          </button>
        )
      })}
    </div>
  )
}

/** A workspace's own screens as tabs (A, C and E). */
export function ProtoSubTabs({ ws, activeId }: { ws: SidebarCategoryWorkspace; activeId?: string | null }) {
  return (
    <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-theme-border bg-theme-base px-5 py-1.5">
      {ws.destinations.map((d) => {
        const on = activeId ? d.id === activeId : !!d.active
        return (
          <button
            key={d.id}
            type="button"
            onClick={d.onSelect}
            className={clsx('flex items-center whitespace-nowrap rounded-md px-3 py-1.5 text-[13px] font-medium', on ? 'bg-skyhook-600/10 text-skyhook-700 dark:bg-skyhook-500/15 dark:text-skyhook-300' : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary')}
          >
            {d.label}
            <Count n={d.count} />
          </button>
        )
      })}
    </div>
  )
}

/** Option C: the page header with a way back to Resources. */
export function ProtoOwnPageHeader({ name, ws }: { name: string; ws: SidebarCategoryWorkspace }) {
  const navigate = useNavigate()
  const Icon = ws.icon ?? LayoutGrid
  return (
    <div className="flex shrink-0 items-center gap-3 border-b border-theme-border bg-theme-surface px-5 py-2.5">
      <button type="button" onClick={() => navigate('/resources')} className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary">
        <ArrowLeft className="h-3.5 w-3.5" />Resources
      </button>
      <span className="h-4 w-px bg-theme-border" />
      <Icon className="h-4 w-4 text-theme-text-secondary" />
      <span className="text-sm font-semibold text-theme-text-primary">{ws.label ?? name}</span>
      <span className="text-xs text-theme-text-tertiary">{name}</span>
    </div>
  )
}

/** Option D: the app switcher in the top bar. */
export function ProtoAppSwitcher({ map }: { map: ProtoWorkspaces }) {
  const navigate = useNavigate()
  const location = useLocation()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [open])
  const current = protoWorkspaceForLocation(location.pathname, location.search)
  const currentWs = current ? map?.[current] : undefined
  const CurrentIcon = currentWs?.icon ?? Compass
  const apps = [{ name: '', label: 'Cluster explorer', product: 'Resources, Topology, Issues…', icon: Compass, go: () => navigate('/') },
    ...Object.entries(map ?? {}).map(([name, ws]) => ({ name, label: ws.label ?? name, product: name, icon: ws.icon ?? LayoutGrid, go: () => ws.destinations[0]?.onSelect() }))]
  return (
    <div ref={ref} className="relative">
      <button type="button" onClick={() => setOpen((o) => !o)} className="flex h-9 shrink-0 items-center gap-2 whitespace-nowrap rounded-lg border border-theme-border bg-theme-elevated px-3 text-sm font-semibold text-theme-text-primary hover:bg-theme-hover">
        <CurrentIcon className="h-4 w-4" />{currentWs ? currentWs.label ?? current : 'Cluster explorer'}<ChevronDown className="h-3.5 w-3.5 text-theme-text-tertiary" />
      </button>
      {open && (
        <div className="absolute left-0 top-11 z-50 w-72 rounded-xl border border-theme-border bg-theme-elevated p-1.5 shadow-theme-lg">
          {apps.map((a) => (
            <button key={a.name || 'explorer'} type="button" onClick={() => { setOpen(false); a.go() }} className={clsx('flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left hover:bg-theme-hover', (a.name || null) === current && 'bg-theme-hover')}>
              <a.icon className="h-4 w-4 text-theme-text-secondary" />
              <span className="flex flex-col"><span className="text-sm font-medium text-theme-text-primary">{a.label}</span><span className="text-xs text-theme-text-tertiary">{a.product}</span></span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

/** Option E: one Home card per workspace. */
export function ProtoHomeCards({ namespaces }: { namespaces: string[] }) {
  const map = useProtoWorkspaces(namespaces)
  return (
    <>
      {Object.entries(map ?? {}).map(([name, ws]) => {
        const Icon = ws.icon ?? LayoutGrid
        return (
          <button key={name} type="button" onClick={() => ws.destinations[0]?.onSelect()} className="group h-[260px] min-w-[260px] flex-1 rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left">
            <div className="flex h-full w-full flex-col">
              <div className="flex items-center justify-between border-b border-theme-border/50 px-5 py-3">
                <div className="flex items-center gap-2"><Icon className="h-4 w-4 text-theme-text-secondary" /><span className="text-xs font-semibold uppercase tracking-wider text-theme-text-secondary">{ws.label ?? name}</span></div>
                <span className={clsx('text-[11px] font-medium', ws.count ? 'text-amber-500' : 'text-emerald-500')}>{ws.count ? `${ws.count} need attention` : 'No active signals'}</span>
              </div>
              <div className="flex-1 space-y-1.5 px-5 py-3">
                {ws.destinations.map((d) => (
                  <div key={d.id} className="flex items-center justify-between text-[13px] text-theme-text-secondary"><span>{d.label}</span>{d.count ? <span className="font-mono text-[11px] text-amber-500">{d.count}</span> : null}</div>
                ))}
              </div>
              <div className="border-t border-theme-border/50 px-5 py-2.5 text-[11px] text-theme-text-tertiary group-hover:text-theme-text-secondary">Open {ws.label ?? name} →</div>
            </div>
          </button>
        )
      })}
    </>
  )
}

/** The floating switcher that changes the prototype option live. */
export function ProtoSwitcher() {
  const { mode } = useNavProto()
  const current = NAV_PROTO_OPTIONS.find((o) => o.mode === mode)
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-4 z-[90] flex justify-center">
      <div className="pointer-events-auto flex items-center gap-1 rounded-full border border-amber-500/40 bg-theme-elevated/95 p-1 shadow-theme-lg backdrop-blur">
        <span className="px-3 text-[11px] font-semibold uppercase tracking-wider text-amber-600 dark:text-amber-300">Nav prototype</span>
        {NAV_PROTO_OPTIONS.map((o) => (
          <button key={o.mode} type="button" title={o.short} onClick={() => setNavProtoMode(o.mode)} className={clsx('rounded-full px-3 py-1.5 text-xs font-medium', o.mode === mode ? 'bg-skyhook-600 text-white' : 'text-theme-text-secondary hover:bg-theme-hover')}>
            {o.label}
          </button>
        ))}
        <span className="hidden max-w-[260px] truncate px-3 text-[11px] text-theme-text-tertiary xl:block">{current?.short}</span>
      </div>
    </div>
  )
}
