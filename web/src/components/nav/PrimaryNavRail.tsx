import type { ComponentType } from 'react'
import type { ReactNode } from 'react'
import { Home, Network, List, Clock, AlertTriangle, Package, GitBranch, Boxes, Activity, DollarSign, ShieldCheck, Settings, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { clsx } from 'clsx'
import type { MainView } from '../../types'
import { Tooltip } from '../ui/Tooltip'

// The views the rail can navigate to. Broader than k8s-ui's ExtendedMainView
// (which omits 'applications') — it mirrors the navigable subset of App.tsx's
// own view union, so onNavigate accepts App's setMainView directly.
type NavRailView = MainView | 'issues' | 'traffic' | 'gitops' | 'applications' | 'cost' | 'checks'

// Primary left nav rail for standalone (non-embedded) Radar.
//
// Ported from radar-hub-web's src/shell/LeftRail.tsx, simplified for OSS:
// Radar's window is desktop-only (App.tsx outer `min-w-[800px]`), so there's
// no mobile slide-in sheet — just two committed states driven by a persisted
// pin (see useNavRailPinned):
//   - pinned   → labeled w-48 sidebar.
//   - unpinned → slim w-14 icon rail; labels surface as `left-full` fly-outs
//                on hover (NOT a whole-rail hover-expand, which would reflow
//                content under the cursor).
//
// In embedded mode (Radar Hub) this rail is not rendered at all — the host
// owns the left chrome via its own fleet LeftRail, and Radar falls back to
// its top-bar pill nav. That keeps the @skyhook-io/radar-app surface
// non-breaking and avoids triple-stacked left chrome.

interface NavItemDef {
  view: NavRailView
  icon: ComponentType<{ className?: string }>
  label: string
}

// The full standalone view set, flat (no group dividers). Order descends by
// day-to-day frequency: Home, then the Resources/Issues/Topology core ("what's
// running / what's wrong / how's it wired"), then app + temporal views,
// delivery, and finally the periodic posture/spend pair. The rail's vertical
// room lets us surface the views the 8-slot pill bar dropped (Issues,
// Applications, Cost).
const NAV_ITEMS: NavItemDef[] = [
  { view: 'home', icon: Home, label: 'Home' },
  { view: 'resources', icon: List, label: 'Resources' },
  { view: 'issues', icon: AlertTriangle, label: 'Issues' },
  { view: 'topology', icon: Network, label: 'Topology' },
  { view: 'applications', icon: Boxes, label: 'Applications' },
  { view: 'timeline', icon: Clock, label: 'Timeline' },
  { view: 'traffic', icon: Activity, label: 'Live Traffic' },
  { view: 'helm', icon: Package, label: 'Helm' },
  { view: 'gitops', icon: GitBranch, label: 'GitOps' },
  { view: 'checks', icon: ShieldCheck, label: 'Checks' },
  { view: 'cost', icon: DollarSign, label: 'Cost' },
]

interface PrimaryNavRailProps {
  // `string`, not ExtendedMainView: App.tsx's mainView is a superset
  // (adds 'applications'/'workload'/'compare') that isn't in k8s-ui's
  // ExtendedMainView. Active state only needs equality, so accept any
  // view id and compare against our NAV_ITEMS views.
  activeView: string
  onNavigate: (view: NavRailView) => void
  pinned: boolean
  onTogglePinned: () => void
  // Hidden on narrow windows where the rail is responsively forced slim — an
  // expand control there would just re-breach the content floor.
  showPinToggle?: boolean
  // Rail-bottom "me & my tools" cluster. onOpenSettings opens the Settings
  // dialog; accountSlot is the account control (App passes <UserMenu variant=…>,
  // which self-nulls without auth so the row vanishes in no-auth OSS).
  onOpenSettings?: () => void
  accountSlot?: ReactNode
}

export function PrimaryNavRail({ activeView, onNavigate, pinned, onTogglePinned, showPinToggle = true, onOpenSettings, accountSlot }: PrimaryNavRailProps) {
  return (
    <aside
      aria-label="Primary navigation"
      className={clsx(
        // Dedicated sidebar token = the DEEPEST layer of the elevation scale, so
        // the rail reads as chrome on EVERY view — not just ones where a `surface`
        // facet pane happens to sit next to it (the content floor is `base`, which
        // a `base` rail blends into). Neutral by design: the brand accent lives on
        // the active item, not the nav background.
        'shrink-0 flex flex-col bg-theme-sidebar border-r border-theme-border h-full transition-[width] duration-200 ease-[cubic-bezier(0.16,1,0.3,1)]',
        // w-44 (176px): OSS labels are short (longest is "Applications"), so the
        // rail is trimmer than Radar Cloud's w-60 (which carries long cluster
        // names). Keep in sync with the minWidth content-floor calc in App.tsx.
        pinned ? 'w-44' : 'w-14',
      )}
    >
      <BrandRow pinned={pinned} onNavigate={onNavigate} />

      {/* Pinned: the nav owns the flex space and scrolls internally on short
          windows, so the rail-bottom account/settings/pin row stays reachable.
          Slim: keep the nav at natural height with a spacer below — an
          overflow-y container would clip the `left-full` fly-out labels
          (overflow-y:auto forces overflow-x to clip), and those ARE the labels
          in slim mode. The slim short-window case is doubly-rare (slim is
          width-triggered) and yields to keeping fly-outs intact. */}
      <nav
        className={clsx(
          'flex flex-col gap-0.5 pt-3 px-2',
          pinned && 'flex-1 min-h-0 overflow-y-auto',
        )}
      >
        {NAV_ITEMS.map((item) => (
          <NavRailItem
            key={item.view}
            item={item}
            active={activeView === item.view}
            pinned={pinned}
            onNavigate={onNavigate}
          />
        ))}
      </nav>

      {!pinned && <div className="flex-1" />}

      {/* "Me & my tools" — account + settings, the conventional rail-bottom
          cluster (VS Code / Discord / ArgoCD / Radar Cloud's own UserOrgMenu).
          accountSlot self-nulls without auth, so no-auth OSS shows only Settings. */}
      <nav className="flex flex-col gap-0.5 px-2 pt-1 border-t border-theme-border/50">
        {accountSlot}
        {onOpenSettings && (
          <RailActionRow icon={Settings} label="Settings" pinned={pinned} onClick={onOpenSettings} />
        )}
      </nav>

      {/* Pin / unpin toggle — anchored at the bottom (VS Code / Linear pattern).
          The icon points the direction the rail will move: close-panel when
          expanded, open-panel when slim. Hidden when the rail is responsively
          forced slim (showPinToggle=false) — expanding there isn't available. */}
      {showPinToggle && (
      <div className="px-2 pb-2 pt-1 border-t border-theme-border/50">
        <Tooltip content={pinned ? 'Collapse navigation' : 'Expand navigation'} position="right" wrapperClassName="!block w-full shrink-0">
        <button
          type="button"
          onClick={onTogglePinned}
          aria-label={pinned ? 'Collapse navigation' : 'Expand navigation'}
          className="group/pin relative flex h-9 w-full items-center rounded-md text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-secondary transition-colors"
        >
          <span className="flex w-10 shrink-0 items-center justify-center">
            {pinned ? <PanelLeftClose className="w-[18px] h-[18px]" /> : <PanelLeftOpen className="w-[18px] h-[18px]" />}
          </span>
          {/* `hidden` (not sr-only) when collapsed: the button's aria-label is
              the accessible name; leaving an "Collapse" label in the a11y tree
              would contradict the "Expand navigation" label. */}
          <span className={clsx('text-[13px] font-medium', !pinned && 'hidden')}>Collapse</span>
        </button>
        </Tooltip>
      </div>
      )}
    </aside>
  )
}

function BrandRow({ pinned, onNavigate }: { pinned: boolean; onNavigate: (view: NavRailView) => void }) {
  // Clickable brand = secondary home affordance (logo→home convention). The
  // Home nav item below still carries the active state; the brand just navigates.
  return (
    <Tooltip content="Home" position="right" wrapperClassName="!block w-full shrink-0">
    <button
      type="button"
      onClick={() => onNavigate('home')}
      aria-label="Radar — go to home"
      // Height matches the top bar header (App.tsx — items-center + py-2 = 51px)
      // so the rail's brand divider and the header's bottom border form one line.
      className="flex h-[51px] w-full items-center border-b border-theme-border/50 shrink-0 transition-opacity hover:opacity-80"
    >
      <span className="flex w-14 shrink-0 items-center justify-center">
        <span className="relative w-7 h-7 rounded-lg overflow-hidden bg-emerald-500/10 border border-emerald-500/20">
          <img
            src="/images/radar/radar-icon.svg"
            alt=""
            aria-hidden
            className="w-full h-full p-0.5"
            onError={(e) => console.error('Radar logo asset failed to load:', (e.currentTarget as HTMLImageElement).src)}
          />
        </span>
      </span>
      <span className={clsx('flex flex-col leading-none text-left', !pinned && 'opacity-0 pointer-events-none')}>
        <span className="font-semibold text-[15px] tracking-tight text-theme-text-primary">Radar</span>
        <span className="text-[9px] mt-0.5 tracking-wide uppercase text-theme-text-tertiary">by Skyhook</span>
      </span>
    </button>
    </Tooltip>
  )
}

function NavRailItem({
  item,
  active,
  pinned,
  onNavigate,
}: {
  item: NavItemDef
  active: boolean
  pinned: boolean
  onNavigate: (view: NavRailView) => void
}) {
  const { icon: Icon, label, view } = item
  return (
    <div className={clsx('group/item relative', !pinned && 'w-10')}>
      <button
        type="button"
        onClick={() => onNavigate(view)}
        aria-current={active ? 'page' : undefined}
        className={clsx(
          'relative flex h-9 w-full items-center rounded-md text-sm font-medium transition-colors',
          // Slim mode: clip the hit area to the icon column so the (opacity-0)
          // label can't capture clicks meant for content to the right.
          !pinned && 'max-w-10 overflow-hidden',
          active
            ? 'bg-skyhook-600/10 dark:bg-skyhook-500/15 text-skyhook-700 dark:text-skyhook-300'
            : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
        )}
      >
        {/* Left-edge accent bar on active — reads even when the row tint is soft. */}
        <span
          aria-hidden
          className={clsx(
            'absolute left-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-r-full bg-skyhook-600 dark:bg-skyhook-400 transition-opacity',
            active ? 'opacity-100' : 'opacity-0',
          )}
        />
        <span className="flex w-10 shrink-0 items-center justify-center">
          <Icon className={clsx('w-[18px] h-[18px]', active ? 'text-skyhook-700 dark:text-skyhook-300' : 'text-theme-text-tertiary group-hover/item:text-theme-text-secondary')} />
        </span>
        <span className={clsx('pr-3 truncate', !pinned && 'opacity-0')}>{label}</span>
      </button>

      {/* Slim-mode fly-out label — sibling of the button so it escapes the
          button's overflow clip; pointer-events-none so it never eats clicks. */}
      {!pinned && (
        <span
          aria-hidden
          className="pointer-events-none absolute left-full top-1/2 z-50 ml-1 hidden -translate-y-1/2 whitespace-nowrap rounded-md border border-theme-border bg-theme-hover px-2.5 py-1 text-[13px] font-medium text-theme-text-primary opacity-0 shadow-lg shadow-black/30 transition-opacity duration-75 group-hover/item:block group-hover/item:opacity-100"
        >
          {label}
        </span>
      )}
    </div>
  )
}

// A rail-bottom action row — same icon-column + fly-out treatment as NavRailItem
// but it's an action (onClick), not navigation, so no active-state/accent bar.
// Exported so the account control (UserMenu rail variant) can match the look.
export function RailActionRow({
  icon: Icon,
  label,
  pinned,
  onClick,
}: {
  icon: ComponentType<{ className?: string }>
  label: string
  pinned: boolean
  onClick: () => void
}) {
  return (
    <div className={clsx('group/item relative', !pinned && 'w-10')}>
      <button
        type="button"
        onClick={onClick}
        className={clsx(
          'relative flex h-9 w-full items-center rounded-md text-sm font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors',
          !pinned && 'max-w-10 overflow-hidden',
        )}
      >
        <span className="flex w-10 shrink-0 items-center justify-center">
          <Icon className="w-[18px] h-[18px] text-theme-text-tertiary group-hover/item:text-theme-text-secondary" />
        </span>
        <span className={clsx('pr-3 truncate', !pinned && 'opacity-0')}>{label}</span>
      </button>
      {!pinned && (
        <span
          aria-hidden
          className="pointer-events-none absolute left-full top-1/2 z-50 ml-1 hidden -translate-y-1/2 whitespace-nowrap rounded-md border border-theme-border bg-theme-hover px-2.5 py-1 text-[13px] font-medium text-theme-text-primary opacity-0 shadow-lg shadow-black/30 transition-opacity duration-75 group-hover/item:block group-hover/item:opacity-100"
        >
          {label}
        </span>
      )}
    </div>
  )
}
