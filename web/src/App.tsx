import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { flushSync } from 'react-dom'
import { startViewTransitionSafe } from '@skyhook-io/k8s-ui/utils/view-transition'
import { englishPlural } from '@skyhook-io/k8s-ui/utils/pluralize'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams, useNavigationType, NavigationType } from 'react-router-dom'
import { HomeView } from './components/home/HomeView'
import { DebugOverlay } from './components/DebugOverlay'
import { GlobalDiagnoseButton } from './components/diagnose/LocalDiagnoseAction'
import { useDiagnoseLayout } from './components/diagnose/DiagnoseContext'
import { DiagnoseSurface } from './components/diagnose/DiagnoseSurface'
import { TopologyGraph, TopologySearch, TopologyFilterSidebar, TopologyControls, FreshnessControl, gitOpsRouteForKind, gitOpsRouteForResource, ScopePill, PaneLoader } from '@skyhook-io/k8s-ui'
import { initNavigationMap } from '@skyhook-io/k8s-ui/utils/navigation'
import { useAPIResources, CORE_RESOURCES } from './api/apiResources'
import { TimelineView } from './components/timeline/TimelineView'
import { ResourcesView } from './components/resources/ResourcesView'
import { serializeColumnFilters } from './components/resources/resource-utils'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { WorkloadViewRoute } from './components/workload/WorkloadView'
import { CompareViewRoute } from './components/compare/CompareViewRoute'
import { HelmView } from './components/helm/HelmView'
import { HelmCompareRoute } from './components/helm/HelmCompareRoute'
import { TrafficView } from './components/traffic/TrafficView'
import { CostView } from './components/cost/CostView'
import { AuditView } from './components/audit/AuditView'
import { IssuesPane } from './components/issues/IssuesPane'
import { GitOpsView } from './components/gitops/GitOpsView'
import { ApplicationsView } from './components/applications/ApplicationsView'
import { HelmReleaseDrawer } from './components/helm/HelmReleaseDrawer'
import { PortForwardProvider, PortForwardIndicator, PortForwardPanel } from './components/portforward/PortForwardManager'
import { DockProvider, BottomDock, useDock, useDockReservedHeight, useOpenLocalTerminal } from './components/dock'
import { DURATION_DOCK } from '@skyhook-io/k8s-ui/utils/animation'
import { ContextSwitcher } from './components/ContextSwitcher'
import { NamespaceSwitcher, type NamespaceSwitcherHandle } from './components/NamespaceSwitcher'
import { useNavCustomization } from './context/NavCustomization'
import type { FleetTakeoverTarget } from './context/NavCustomization'
import { PrimaryNavRail } from './components/nav/PrimaryNavRail'
import { useNavRailPinned } from './hooks/useNavRailPinned'
import { useMediaQuery } from './hooks/useMediaQuery'
import { ContextSwitchProvider, useContextSwitch } from './context/ContextSwitchContext'
import { ConnectionProvider, useConnection } from './context/ConnectionContext'
import { ConnectionErrorView } from './components/ConnectionErrorView'
import { CapabilitiesProvider, useCapabilitiesContext } from './contexts/CapabilitiesContext'
import { UserMenu } from './components/UserMenu'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { UpdateNotification } from './components/ui/UpdateNotification'
import { ShortcutHelpOverlay } from './components/ui/ShortcutHelpOverlay'
import { CommandPalette } from './components/ui/CommandPalette'
import { DiagnosticsOverlay } from './components/ui/DiagnosticsOverlay'
import { useEventSource } from './hooks/useEventSource'
import { debugNamespaceLog, useNamespaces, useNamespaceScope, useSetActiveNamespace, useSwitchContext, useAuthMe, useAudit } from './api/client'
import { buildAuditSeverityMap } from './utils/auditBadges'
import { routePath, apiUrl, getAuthHeaders, getCredentialsMode } from './api/config'
import { KeyboardShortcutProvider, useRegisterShortcut, useRegisterShortcuts, useSuppressBaseShortcuts } from './hooks/useKeyboardShortcuts'
import { useAnimatedUnmount } from './hooks/useAnimatedUnmount'
import { useDocumentTitle } from './hooks/useDocumentTitle'
import type { ClusterLoadState } from './types/clusterLoadState'
import { useClusterLoadState } from './hooks/useClusterLoadState'
import { Network, List, Clock, Package, Sun, Moon, Activity, Home, Star, Search, Bug, SquareTerminal, ShieldCheck, GitBranch, HelpCircle, Loader2, RefreshCw } from 'lucide-react'
import { useTheme } from './context/ThemeContext'
import { Tooltip } from './components/ui/Tooltip'
import { LargeClusterNamespacePicker } from './components/shared/LargeClusterNamespacePicker'
import { SettingsDialog } from './components/settings/SettingsDialog'
import { MyPermissionsDialog } from './components/settings/MyPermissionsDialog'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, SelectedHelmRelease, NodeKind, TopologyMode, Topology, K8sEvent } from './types'
import { kindToPlural, pluralToKind, openExternal, apiVersionToGroup, relatedResourcePath, searchHitToSelectedResource } from './utils/navigation'
import { type OmnibarHandle } from './components/ui/Omnibar'
import { RadarOmnibar } from './components/ui/RadarOmnibar'
import type { ContextSwitcherHandle } from './components/ContextSwitcher'

// All possible node kinds (core + GitOps)
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute',
  'Service', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HorizontalPodAutoscaler', 'Job', 'CronJob', 'PersistentVolumeClaim', 'Namespace',
  'Application', 'Kustomization', 'HelmRelease', 'GitRepository',
  'KnativeService', 'KnativeConfiguration', 'KnativeRevision', 'KnativeRoute',
  'Broker', 'Trigger', 'PingSource', 'ApiServerSource', 'ContainerSource', 'SinkBinding', 'Channel',
  'IngressRoute', 'IngressRouteTCP', 'IngressRouteUDP', 'Middleware', 'MiddlewareTCP',
  'TraefikService', 'ServersTransport', 'ServersTransportTCP', 'TLSOption', 'TLSStore',
  'HTTPProxy', // Contour
  'CAPICluster', 'MachineDeployment', 'MachineSet', 'Machine', 'MachinePool', // Cluster API
  'KubeadmControlPlane', 'ClusterClass', 'MachineHealthCheck',
]

// Default visible kinds (ReplicaSet hidden by default - noisy intermediate object)
const DEFAULT_VISIBLE_KINDS = ALL_NODE_KINDS.filter(k => k !== 'ReplicaSet')

// CRD kinds hidden by default in the topology (infrastructure plumbing).
// Users can re-enable via the filter sidebar.
const CRD_HIDDEN_BY_DEFAULT = new Set(['GatewayClass', 'IngressClass', 'NodePool', 'NodeClaim', 'NodeClass'])

// Top-bar height in px. The body frame's right-side surfaces (AI panel, resource +
// Helm drawers) all inset their top by this so they sit BELOW the header. 0 in
// chromeless embeds (the host owns the chrome, no Radar header). Keep in sync with
// the <header> py/line-height; the drawers historically hardcoded the same 49.
const APP_HEADER_HEIGHT = 49

// CAPI kinds shown in Fleet topology mode (+ Node for Machine→Node edges)
// Includes core CAPI kinds and all infrastructure provider kinds
const FLEET_MODE_KINDS = new Set<NodeKind>([
  'CAPICluster', 'MachineDeployment', 'MachineSet', 'Machine', 'MachinePool',
  'KubeadmControlPlane', 'ClusterClass', 'MachineHealthCheck', 'Node',
  // AWS provider
  'AWSManagedControlPlane', 'AWSManagedMachinePool', 'AWSMachine',
  'AWSMachineTemplate', 'AWSManagedCluster', 'AWSClusterControllerIdentity',
  'EKSConfig', 'EKSConfigTemplate',
  // GCP provider
  'GCPManagedControlPlane', 'GCPManagedMachinePool', 'GCPMachine',
  'GCPMachineTemplate', 'GCPManagedCluster',
  // Azure provider
  'AzureManagedControlPlane', 'AzureManagedMachinePool', 'AzureMachine',
  'AzureMachineTemplate', 'AzureManagedCluster',
])

// Convert API resource name back to topology node ID prefix
// Extended MainView type that includes traffic and cost
type ExtendedMainView = MainView | 'traffic' | 'cost' | 'workload' | 'checks' | 'gitops' | 'compare' | 'helmCompare' | 'issues' | 'applications'

// Extract view from URL path
function getViewFromPath(pathname: string): ExtendedMainView {
  if (pathname.replace(/\/+$/, '') === '/helm/compare') return 'helmCompare'
  const path = pathname.replace(/^\//, '').split('/')[0]
  if (path === '' || path === 'home') return 'home'
  if (path === 'topology') return 'topology'
  if (path === 'resources') return 'resources'
  if (path === 'timeline') return 'timeline'
  if (path === 'helm') return 'helm'
  if (path === 'traffic') return 'traffic'
  if (path === 'cost') return 'cost'
  if (path === 'workload') return 'workload'
  if (path === 'checks' || path === 'audit') return 'checks'  // /audit = legacy → checks
  if (path === 'gitops') return 'gitops'
  if (path === 'applications') return 'applications'
  if (path === 'compare') return 'compare'
  if (path === 'issues') return 'issues'
  return 'home'
}

// The namespace scope filter is meaningful only on namespaced surfaces. On
// cluster-scoped views it does nothing, so we disable it with an explanation
// rather than leaving a dead control that silently ignores the pick:
//   - Cost is reported per-namespace across the whole cluster (the view IS the
//     breakdown; a filter would only hide rows).
//   - A GitOps detail tree spans namespaces — its controller lives in one
//     namespace but manages workloads across many.
//   - A cluster-scoped resource kind (Nodes, PVs, ClusterRoles…) has no
//     namespace at all.
// The pick itself is preserved so it re-applies when the user returns to a
// namespaced view.
function namespaceFilterDisabled(
  view: ExtendedMainView,
  pathname: string,
  apiResources?: { name: string; kind: string; namespaced: boolean }[],
): { disabled: boolean; tooltip?: string } {
  if (view === 'cost') {
    return {
      disabled: true,
      tooltip: 'Cost is reported per namespace across the whole cluster — the namespace filter doesn’t apply here.',
    }
  }
  const segments = pathname.replace(/^\//, '').split('/')
  if (view === 'gitops' && segments[1] === 'detail') {
    return {
      disabled: true,
      tooltip: 'This resource manages workloads across namespaces — the namespace filter doesn’t apply to its tree.',
    }
  }
  if (view === 'resources') {
    const kindSlug = segments[1]
    const match = kindSlug ? apiResources?.find(r => r.name === kindSlug) : undefined
    if (match && !match.namespaced) {
      return {
        disabled: true,
        tooltip: `${match.kind} is a cluster-scoped resource — namespaces don’t apply.`,
      }
    }
  }
  return { disabled: false }
}

// Browser tab label for every Radar view, derived from the route URL so it's
// correct regardless of which component renders it. A detail drawer that opens
// over a list (?resource=…) is deliberately NOT titled — it's the same page, so
// it keeps the list's title.
function radarPageTitle(pathname: string, search = '', apiResources?: { name: string; kind: string; group?: string }[]): string | null {
  const decode = (s: string) => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  }
  const capitalize = (text: string) =>
    text ? text.charAt(0).toUpperCase() + text.slice(1) : text
  const pluralKindTitle = (kind: string, resourceName: string) =>
    kind.toLowerCase() === resourceName.toLowerCase() ? kind : englishPlural(kind)
  const pathSegments = pathname.replace(/^\//, '').split('/').filter(Boolean)
  const view = getViewFromPath(pathname)

  // Full-page resource detail: /workload/<kind>/<ns>/<name> (name may contain '/').
  if (view === 'workload') return pathSegments.slice(3).map(decode).join('/') || null
  // Resources is browsed per-kind: /resources/<kind> → "<Kind>" (e.g. ConfigMap);
  // bare /resources (before it redirects to a default kind) → "Resources".
  if (view === 'resources') {
    const resourceName = decode(pathSegments[1] ?? '')
    if (!resourceName) return 'Resources'
    const match = apiResources?.find((r) => r.name === resourceName)
    return pluralKindTitle(match?.kind ?? pluralToKind(resourceName), resourceName)
  }
  // GitOps detail is /gitops/detail/<kind>/<ns>/<name> → the resource name;
  // anything else (the list) → "GitOps".
  if (view === 'gitops')
    return pathSegments[1] === 'detail' ? decode(pathSegments[4] ?? '') || 'GitOps' : 'GitOps'
  if (view === 'applications') {
    const appKey = new URLSearchParams(search).get('app')
    if (!appKey) return 'Applications'
    const decoded = decode(appKey)
    const slash = decoded.lastIndexOf('/')
    return slash >= 0 && slash < decoded.length - 1 ? decoded.slice(slash + 1) : decoded
  }

  // The landing view reads "Overview" rather than "Home" in the tab.
  if (view === 'home') return 'Overview'
  // Every other view's label is its id capitalized — getViewFromPath has already
  // normalized aliases (e.g. /audit → 'checks'), so no lookup table is needed.
  return capitalize(view)
}

function AuthBarrier({ authMode }: { authMode: string }) {
  useEffect(() => {
    if (authMode === 'oidc') {
      window.location.href = routePath('/auth/login')
    }
  }, [authMode])

  if (authMode === 'oidc') {
    return (
      <PaneLoader
        label="Redirecting to login…"
        className="flex-1 min-h-0 bg-theme-base"
      />
    )
  }

  return (
    <div className="flex-1 flex items-center justify-center bg-theme-base">
      <div className="flex flex-col items-center gap-4 max-w-md text-center">
        <div className="w-12 h-12 rounded-full bg-amber-500/10 flex items-center justify-center">
          <svg className="w-6 h-6 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M12 15v2m0 0v2m0-2h2m-2 0H10m4-6V7a4 4 0 00-8 0v4h8z" />
            <rect x="5" y="11" width="14" height="11" rx="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </div>
        <div>
          <p className="text-lg font-medium text-theme-text-primary">Authentication Required</p>
          <p className="text-sm text-theme-text-secondary mt-2">
            Radar is configured with proxy authentication. Access it through your organization's auth proxy to authenticate.
          </p>
        </div>
      </div>
    </div>
  )
}

// Identity of the "page" a non-URL-backed peek drawer belongs to. Pathname alone
// is not enough: Applications keeps the list and an app's detail on the same
// `/applications` pathname and distinguishes them with `?app=`, so a Back from
// detail to list would otherwise leave the peek orphaned. Only `app` is included
// (not the whole query) so filter/tab/namespace churn doesn't close the peek.
function peekOwnerKey(pathname: string, search: string): string {
  return `${pathname}\n${new URLSearchParams(search).get('app') ?? ''}`
}

interface AppProps {
  manageDocumentTitle?: boolean
  documentTitleSuffix?: string
  onClusterLoadStateChange?: (state: ClusterLoadState) => void
}

function AppInner({ manageDocumentTitle = false, documentTitleSuffix, onClusterLoadStateChange }: AppProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const navigationType = useNavigationType()
  const [searchParams, setSearchParams] = useSearchParams()
  const capabilities = useCapabilitiesContext()
  const openLocalTerminal = useOpenLocalTerminal()
  const navCustomization = useNavCustomization()
  // The AI panel is an absolute slot in the body frame (the column under the header):
  // it reserves a right gutter on the CONTENT only, so the navbar + nav rail stay
  // static. contentGutter is the docked panel width (0 when closed/overlay/maximized).
  const { open: diagnoseOpen, contentGutter } = useDiagnoseLayout()
  // Hand off to a host-owned URL. The host's `onHostNavigate` (Radar Cloud's
  // cross-tree swap) navigates same-document so the chrome morphs instead of
  // cold-booting; without it we fall back to a hard `window.location` nav.
  const goHost = useCallback(
    (url: string) => {
      if (navCustomization.onHostNavigate) navCustomization.onHostNavigate(url)
      else window.location.assign(url)
    },
    [navCustomization],
  )
  // Resolve every host-takeover URL ONCE (memoized on navCustomization) so the
  // setMainView intercept, redirect effect, nav-pill filtering, inline-view
  // gating, and the cert click handler all consume the SAME value — host
  // callbacks aren't guaranteed idempotent (scope / flags / signed URLs can
  // shift between calls). undefined = not taken over → Radar renders the view
  // itself. `clusterChecksHref` is the deprecated pre-1.7 hook, folded into the
  // 'checks' target for back-compat.
  const takeover: Record<FleetTakeoverTarget, string | undefined> = useMemo(
    () => ({
      issues: navCustomization.fleetTakeoverHref?.('issues'),
      gitops: navCustomization.fleetTakeoverHref?.('gitops'),
      checks: navCustomization.fleetTakeoverHref?.('checks') ?? navCustomization.clusterChecksHref?.(),
      certs: navCustomization.fleetTakeoverHref?.('certs'),
    }),
    [navCustomization],
  )
  const { pinned: navRailPinned, togglePinned: toggleNavRailPinned } = useNavRailPinned()
  // Standalone Radar gets the left nav rail; embedded hosts (Radar Hub) own
  // the left chrome via their own fleet rail and keep Radar's top-bar pills.
  const showNavRail = !navCustomization.embedded
  // Chromeless embed: the host (Radar Hub) owns ALL chrome and drives view
  // navigation + scope from its own UI, so Radar renders just the active view's
  // content — no top bar, no view-switcher. Used for per-cluster views surfaced
  // as native cloud destinations behind a cluster picker.
  const chromeless = navCustomization.embedded === true && navCustomization.chrome === 'none'
  // Force the slim rail on narrow windows: a pinned 176px rail needs viewport
  // ≥976 to keep content above its ~800px floor (collapsed needs only ≥856).
  // Below 976 we render collapsed regardless of the pin preference — a
  // temporary responsive override that does NOT touch the persisted value, so
  // the user's pinned state returns when they widen again. Fly-out labels cover
  // the collapsed state, so the manual toggle is hidden here rather than left
  // inert (expanding would just re-breach the floor).
  const railForcedSlim = useMediaQuery('(max-width: 975px)')
  const navRailEffectivePinned = navRailPinned && !railForcedSlim

  // Auth check — detect if auth is enabled but user is not authenticated
  const { data: authMe, isPending: authMePending } = useAuthMe()

  // Restore navigation path after session-expiry re-auth redirect
  useEffect(() => {
    const returnPath = sessionStorage.getItem('radar_return_path')
    if (returnPath) {
      sessionStorage.removeItem('radar_return_path')
      navigate(returnPath, { replace: true })
    }
  }, [navigate])

  // Parse namespaces from URL (supports both 'namespaces' and legacy 'namespace')
  const parseNamespacesFromURL = (params: URLSearchParams): string[] => {
    // Prefer 'namespaces' (plural, comma-separated)
    const nsParam = params.get('namespaces')
    if (nsParam) {
      return nsParam.split(',').map(s => s.trim()).filter(Boolean)
    }
    // Fall back to 'namespace' (singular) for backward compatibility
    const ns = params.get('namespace')
    if (ns) {
      return [ns]
    }
    return []
  }

  // Initialize state from URL
  const getInitialState = () => {
    const namespaces = parseNamespacesFromURL(searchParams)
    return {
      namespaces,
      topologyMode: (searchParams.get('mode') as TopologyMode) || 'resources',
      // Default to namespace grouping when viewing all namespaces
      grouping: (searchParams.get('group') as GroupingMode) || (namespaces.length === 0 ? 'namespace' : 'none'),
    }
  }

  // Get mainView from URL path
  const mainView = getViewFromPath(location.pathname)

  // Initialize the kind→plural discovery map app-wide (not just on ResourcesView
  // mount) so the omnibar can open a CRD hit with an irregular plural from any
  // view — kindToPlural would otherwise English-guess the route before a
  // resources view has run initNavigationMap().
  const { data: navApiResources } = useAPIResources()
  useEffect(() => { if (navApiResources) initNavigationMap(navApiResources) }, [navApiResources])

  // View-aware namespace scope: disabled on cluster-scoped surfaces so the
  // chip isn't a dead control next to the cluster switcher.
  // Fall back to the static core-resource list before discovery loads, so the
  // chip disables immediately on a cluster-scoped core kind (Nodes, PVs, …)
  // instead of flickering enabled until /api-resources resolves.
  const namespaceFilter = namespaceFilterDisabled(mainView, location.pathname, navApiResources ?? CORE_RESOURCES)

  // One URL-derived tab title for every view (see radarPageTitle). Driving it
  // from the URL — not the mounted component. Off unless the host opts in
  // (standalone passes manageDocumentTitle), so embedders keep title ownership.
  useDocumentTitle(manageDocumentTitle ? radarPageTitle(location.pathname, location.search, navApiResources) : null, documentTitleSuffix)

  // Workload slug after `/resources/` (defaults to `pods`). Bare `/resources` redirects to `/resources/pods`.
  const normalizedResourcesKindSlug = useMemo(() => {
    const m = location.pathname.match(/^\/resources(?:\/([^/]+))?/)
    const slug = m?.[1] ?? ''
    return slug || 'pods'
  }, [location.pathname])

  // Canonical URL — `/resources` is not stable for bookmarks/sharing; normalize to `/resources/pods`.
  useEffect(() => {
    const path = location.pathname.replace(/\/+$/, '') || '/'
    if (path !== '/resources') return
    navigate(
      { pathname: '/resources/pods', search: location.search, hash: location.hash },
      { replace: true },
    )
  }, [location.pathname, location.search, location.hash, navigate])

  // Set mainView by navigating to the path
  const setMainView = useCallback((view: ExtendedMainView, params?: Record<string, string>) => {
    // Host takeover: fleet-shaped views (issues/gitops/checks) are owned by the
    // host's fleet pages. Hand straight to the host instead of navigating to
    // our own /<view> first — that intermediate hop mounts the view machinery
    // and flashes the "Opening…" splash before the redirect effect bounces out.
    // Skipping it makes the hand-off a single smooth cross-tree swap. (Direct
    // /<view> URL entry still funnels through the redirect effect below.)
    if (view === 'issues' || view === 'gitops' || view === 'checks') {
      const href = takeover[view]
      if (href) {
        goHost(href)
        return
      }
    }

    const path = view === 'home' ? '/' : `/${view}`

    // Start fresh — keep only cross-view params (namespaces), discard all view-specific ones
    const newParams = new URLSearchParams()
    const globalNamespaces = searchParams.get('namespaces')
    if (globalNamespaces) {
      newParams.set('namespaces', globalNamespaces)
    }

    // Add any new params
    if (params) {
      for (const [key, value] of Object.entries(params)) {
        newParams.set(key, value)
      }
    }

    navigate({ pathname: path, search: newParams.toString() })
  }, [navigate, searchParams, takeover, goHost])

  // Cloud (embedded) takes over the "fleet-shaped" per-cluster views with its
  // own fleet pages scoped to this cluster — owned by the host's left rail — so
  // Radar drops the matching pills (see the nav below). In-app nav hands off in
  // setMainView (above); direct /<view> URL entry funnels through the redirect
  // effect below. Both consume the memoized `takeover` resolved above. Standalone
  // OSS (no fleetTakeoverHref) is unaffected and renders the in-app view.
  //
  // Has the host claimed this view? View-shaped targets only ('certs' has no
  // Radar view — only its Home card consults `takeover`). Used to drop the nav
  // pill and gate the inline view render in favor of the "Opening…" splash.
  const isViewTakenOver = (view: ExtendedMainView): boolean =>
    (view === 'issues' || view === 'gitops' || view === 'checks') && !!takeover[view]
  // The host's URL for the CURRENT view, if taken over. Drives the redirect
  // effect and the "Opening…" splash.
  const viewTakeoverHref =
    mainView === 'issues' || mainView === 'gitops' || mainView === 'checks'
      ? takeover[mainView]
      : undefined
  useEffect(() => {
    if (viewTakeoverHref) {
      window.location.replace(viewTakeoverHref)
    }
  }, [viewTakeoverHref])

  const [namespaces, setNamespaces] = useState<string[]>(getInitialState().namespaces)
  // For large clusters: force SSE to reconnect with namespace filter
  const [forceNamespaceFilter, setForceNamespaceFilter] = useState<string[] | undefined>(undefined)
  const [selectedResource, setSelectedResource] = useState<SelectedResource | null>(null)
  const [drawerInitialTab, setDrawerInitialTab] = useState<'detail' | 'yaml'>('detail')
  const [selectedHelmRelease, setSelectedHelmRelease] = useState<SelectedHelmRelease | null>(null)
  const [topologyMode, setTopologyMode] = useState<TopologyMode>(getInitialState().topologyMode)
  const [groupingMode, setGroupingMode] = useState<GroupingMode>(getInitialState().grouping)
  const [showPolicyEffect, setShowPolicyEffect] = useState(false)
  // Topology filter state
  const [visibleKinds, setVisibleKinds] = useState<Set<NodeKind>>(() => new Set(DEFAULT_VISIBLE_KINDS))
  const [filterSidebarCollapsed, setFilterSidebarCollapsed] = useState(false)
  // Topology node-search → canvas focus request (nonce lets the same node re-focus)
  const [topologyFocus, setTopologyFocus] = useState<{ id: string; nonce: number } | null>(null)
  // Track CRD kinds that have been auto-added to visibleKinds so we don't override user toggles
  const seededCRDKindsRef = useRef<Set<string>>(new Set())

  // Topology live-update pause state
  const [topologyPaused, setTopologyPaused] = useState(false)
  const [displayedTopology, setDisplayedTopology] = useState<typeof topology>(null)
  const pendingTopologyRef = useRef<typeof topology>(null)

  // Help overlay state
  const [showHelp, setShowHelp] = useState(false)

  // Command palette state
  const [showCommandPalette, setShowCommandPalette] = useState(false)

  // Settings dialog state
  const [showSettings, setShowSettings] = useState(false)
  const [showMyPermissions, setShowMyPermissions] = useState(false)

  // Listen for "open-settings" DOM event (used by MCPSetupDialog etc.)
  useEffect(() => {
    const handler = () => setShowSettings(true)
    window.addEventListener('radar:open-settings', handler)
    return () => window.removeEventListener('radar:open-settings', handler)
  }, [])

  // Listen for "open-local-terminal" DOM event — the AI surface is portaled above
  // the DockProvider, so it can't call useOpenLocalTerminal directly; it dispatches
  // this instead (mirrors the open-settings pattern).
  useEffect(() => {
    const handler = (e: Event) => {
      const { command, title } = (e as CustomEvent).detail ?? {}
      openLocalTerminal({ initialCommand: command, title })
    }
    window.addEventListener('radar:open-local-terminal', handler)
    return () => window.removeEventListener('radar:open-local-terminal', handler)
  }, [openLocalTerminal])

  // Diagnostics overlay state
  const [showDiagnostics, setShowDiagnostics] = useState(false)

  // The peek drawer "expanded" into a fullscreen overlay = ?full=1 with a selected
  // resource, on ANY view (resources list, topology graph, GitOps, Applications…) —
  // the underlying view stays mounted. URL-derived so Back/Forward/refresh behave
  // (non-list peeks aren't URL-backed, so refresh drops the overlay gracefully).
  // Used by the routing effects below; the render uses `expandedView` (gated on what
  // actually renders) — see further down.
  const drawerExpanded = !!selectedResource && searchParams.get('full') === '1'

  // On mobile there's no room for the side drawer — a resource detail is always full-screen.
  const isMobile = useMediaQuery('(max-width: 639px)')

  // On a history Pop (back/forward) the URL is authoritative. The URL-write
  // effect, running with not-yet-synced state, would otherwise write the stale
  // state back and revert the Pop — and oscillate with the URL→state read
  // effect (infinite re-render, React #185, blank page). Suppress the writer
  // for the synchronous reconciliation burst after a Pop, then auto-clear (see
  // the arming effect) so later user-driven writes are never affected.
  const skipUrlWriteAfterPopRef = useRef(false)

  // Close resource drawer when the /resources route no longer matches the
  // selected drawer resource. This covers both in-view kind switches and
  // cross-kind navigations from expanded drawers (for example Node -> View Pods).
  const prevResourcesKindKeyRef = useRef<string | null>(null)
  // Owner-key (pathname + ?app) a non-URL-backed peek was opened on; see
  // navigateToResource and peekOwnerKey.
  const peekOwnerKeyRef = useRef<string | null>(null)
  const currentResourceKindSlug = normalizedResourcesKindSlug.toLowerCase()
  const currentResourceGroup = searchParams.get('apiGroup') ?? ''
  const selectedResourceKindSlug = selectedResource ? kindToPlural(selectedResource.kind).toLowerCase() : ''
  const selectedResourceGroup = selectedResource?.group ?? ''
  const selectedResourceRouteMismatch = mainView === 'resources' && !!selectedResource && (
    selectedResourceKindSlug !== currentResourceKindSlug ||
    selectedResourceGroup !== currentResourceGroup
  )
  const resourcesKindRouteChanged = mainView === 'resources' &&
    prevResourcesKindKeyRef.current !== null &&
    prevResourcesKindKeyRef.current !== `${currentResourceGroup}/${currentResourceKindSlug}`

  // A peek opened outside /resources (topology, GitOps, Applications) carries no
  // URL backing, so the only signal that the page beneath it has navigated is
  // that its owner-key (pathname + ?app) no longer matches where it was opened.
  // Hiding it here, at render time, closes the orphan on Back without adding
  // another clearing effect. The /resources case is URL-backed and handled above;
  // an expanded drawer (drawerExpanded) only exists on /resources (?full=1), so it
  // is excluded here and never treated as an orphan.
  const peekRouteOrphaned = !!selectedResource && !drawerExpanded && mainView !== 'resources' &&
    peekOwnerKeyRef.current !== null &&
    peekOwnerKeyRef.current !== peekOwnerKey(location.pathname, location.search)

  // In Applications the inline WorkloadView (?workload) and the peek drawer are
  // mutually exclusive — never two detail surfaces at once. ?workload is the
  // single source of truth: while it's set the peek yields to the inline view.
  // (Opening a child peek from Applications clears ?workload, see onOpenResource.)
  const appsInlineWorkloadActive = mainView === 'applications' && searchParams.has('workload')

  const routeSelectedResource =
    (resourcesKindRouteChanged && selectedResourceRouteMismatch) || peekRouteOrphaned || appsInlineWorkloadActive
      ? null
      : selectedResource

  useEffect(() => {
    if (mainView !== 'resources') {
      prevResourcesKindKeyRef.current = null
      return
    }
    const key = `${currentResourceGroup}/${currentResourceKindSlug}`
    const prev = prevResourcesKindKeyRef.current
    prevResourcesKindKeyRef.current = key

    if (prev !== null && prev !== key && selectedResourceRouteMismatch) {
      setSelectedResource(null)
    }
  }, [mainView, currentResourceKindSlug, currentResourceGroup, selectedResourceRouteMismatch])

  // Animation hooks for smooth mount/unmount transitions
  const resourceDrawer = useAnimatedUnmount(!!routeSelectedResource, 300)
  const helmDrawer = useAnimatedUnmount(!!(mainView === 'helm' && selectedHelmRelease), 300)
  const helpOverlay = useAnimatedUnmount(showHelp, 300)
  const commandPaletteAnim = useAnimatedUnmount(showCommandPalette, 300)
  const diagnosticsOverlay = useAnimatedUnmount(showDiagnostics, 300)

  // Hold last valid values so drawers can animate out before data disappears
  const lastResourceRef = useRef(routeSelectedResource)
  if (routeSelectedResource) lastResourceRef.current = routeSelectedResource
  const drawerResource = routeSelectedResource || lastResourceRef.current

  // Effective fullscreen state — keyed off the resource that's ACTUALLY rendering
  // (routeSelectedResource), not the raw selection, so an orphaned/mismatched peek
  // can't inert the shell with no visible drawer. ?full=1 on any view, or forced on
  // mobile (no room for a side drawer). Drives the inert backdrop + shortcut suppression.
  const expandedView = !!routeSelectedResource && (searchParams.get('full') === '1' || isMobile)
  useSuppressBaseShortcuts(expandedView)
  // Held value for the drawer's `expanded` prop so closing an expanded drawer slides
  // it out at full size instead of running a collapse morph mid-dismiss. Tracks the
  // live state while a resource is selected; frozen during the slide-out.
  const lastExpandedRef = useRef(expandedView)
  if (routeSelectedResource) lastExpandedRef.current = expandedView
  const drawerExpandedProp = routeSelectedResource ? expandedView : lastExpandedRef.current

  const lastHelmReleaseRef = useRef(selectedHelmRelease)
  if (selectedHelmRelease) lastHelmReleaseRef.current = selectedHelmRelease
  const drawerHelmRelease = selectedHelmRelease || lastHelmReleaseRef.current

  // Navigate to a resource — uses View Transitions cross-fade when drawer is already open
  const navigateToResource = useCallback((res: SelectedResource, tab: 'detail' | 'yaml' = 'detail') => {
    // Record the page this peek was opened on. Outside /resources the drawer is
    // not URL-backed, so this ref is what lets the render-time gate below close
    // the peek when the page under it changes (e.g. browser Back off a GitOps
    // detail page, or Applications detail → list via ?app). window.location is
    // read (not the `location` closure) so the value is always current
    // regardless of this callback's memoization.
    peekOwnerKeyRef.current = peekOwnerKey(window.location.pathname, window.location.search)
    const update = () => { setDrawerInitialTab(tab); setSelectedResource(res) }
    // Skip the cross-fade animation entirely on first open (no
    // `selectedResource`); otherwise route through
    // startViewTransitionSafe to swallow the InvalidStateError that
    // the API rejects with on rapid back-to-back navigations.
    // (SKY-833 bug 49)
    if (selectedResource) {
      startViewTransitionSafe(() => flushSync(update))
    } else {
      update()
    }
  }, [selectedResource])

  // Navigate from a detector finding (Audit / Issues) to the resources list for
  // its kind, opening the resource. Shared by both queues — the body was
  // duplicated verbatim at each render site. Encodes the opened resource in the
  // URL (?resource=ns/name) — the same deep-link shape the resources view
  // round-trips — so refresh/share keeps the drawer open instead of dropping it.
  const navigateToResourceList = useCallback((resource: SelectedResource) => {
    const pluralKind = kindToPlural(resource.kind)
    setSelectedResource({ ...resource, kind: pluralKind })
    const newParams = new URLSearchParams(searchParams)
    newParams.delete('kind')
    newParams.delete('mode')
    newParams.delete('group')
    // Open as a normal drawer — never inherit a stale ?full=1/tab from an
    // expanded view we're navigating away from (only expand/drill set those).
    newParams.delete('full')
    newParams.delete('tab')
    newParams.set('resource', resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name)
    if (resource.group) {
      newParams.set('apiGroup', resource.group)
    } else {
      newParams.delete('apiGroup')
    }
    navigate({ pathname: `/resources/${pluralKind}`, search: newParams.toString() })
  }, [searchParams, navigate])

  const navigateToHelmRelease = useCallback((namespace: string, name: string, storageNamespace?: string) => {
    const newParams = new URLSearchParams()
    const globalNamespaces = searchParams.get('namespaces')
    if (globalNamespaces) {
      newParams.set('namespaces', globalNamespaces)
    }
    newParams.set('release', `${namespace}/${name}`)
    if (storageNamespace) {
      newParams.set('releaseStorage', storageNamespace)
    }
    setSelectedHelmRelease({ namespace, name, storageNamespace })
    if (mainView === 'helm') {
      setSearchParams(newParams, { replace: true })
      return
    }
    navigate({ pathname: '/helm', search: newParams.toString() })
  }, [mainView, searchParams, navigate, setSearchParams])

  // From the Issues queue: special controller/manager subjects route to their
  // rich detail pages, not the generic resource drawer that's a dead-end for
  // them. Member resources (Pods, Services, …) fall through to resources.
  const navigateFromIssue = useCallback((resource: SelectedResource) => {
    if (resource.kind === 'HelmRelease' && resource.group === 'helm.sh' && resource.namespace) {
      navigateToHelmRelease(resource.namespace, resource.name)
      return
    }
    const gitOpsPath = gitOpsRouteForResource({
      apiVersion: resource.group ? `${resource.group}/v1` : 'v1',
      kind: resource.kind,
      metadata: { namespace: resource.namespace ?? '', name: resource.name },
    })
    if (gitOpsPath) {
      navigate(gitOpsPath)
      return
    }
    navigateToResourceList(resource)
  }, [navigate, navigateToHelmRelease, navigateToResourceList])

  // Collapse the over-list fullscreen back to the drawer = drop ?full=1 (and the
  // resource-scoped ?tab) in place. The button means "collapse THIS to a drawer"
  // regardless of how we got here (expand, deep link, or a drill trail), so it
  // scrubs rather than walking history — `navigate(-1)` would leave the app on a
  // deep link, or step back to the previous resource after a drill. Browser Back
  // keeps its own natural history walk (it pops the ?full=1 entry → collapse).
  const handleCollapseFromExpanded = useCallback(() => {
    const p = new URLSearchParams(searchParams)
    p.delete('full')
    p.delete('tab')
    setSearchParams(p, { replace: true })
  }, [searchParams, setSearchParams])

  // Close the peek and drop any expand flags. Outside /resources the drawer isn't
  // URL-backed, so a lingering ?full=1/tab would make the next peek reopen
  // fullscreen instead of as a side drawer. (On /resources, ResourcesView's own
  // updateURL also scrubs these — deleting them here too is idempotent.)
  const closeDrawer = useCallback(() => {
    setSelectedResource(null)
    setDrawerInitialTab('detail')
    if (searchParams.has('full') || searchParams.has('tab')) {
      const p = new URLSearchParams(searchParams)
      p.delete('full')
      p.delete('tab')
      setSearchParams(p, { replace: true })
    }
  }, [searchParams, setSearchParams])

  // Theme toggle for keyboard shortcut
  const { toggleTheme } = useTheme()

  // Context switching for command palette
  const switchContext = useSwitchContext()

  // Refs for dropdown components to trigger them via shortcuts
  const namespaceSwitcherRef = useRef<NamespaceSwitcherHandle>(null)
  const omnibarRef = useRef<OmnibarHandle>(null)

  const contextSwitcherRef = useRef<ContextSwitcherHandle>(null)

  // View switching keyboard shortcuts
  // `g`+mnemonic sequences cover every view. Numeric 1–N can't: there are 11
  // views and only 9 single digits, so `10`/`11` never match a keypress (a
  // KeyboardEvent.key is one character). `g`-prefixed mnemonics scale, are the
  // GitHub/Linear convention, and their second keys are all distinct (no clash
  // with the scoped `g g` table shortcut). The letters are fixed regardless of
  // position, so reordering the rail never changes a shortcut.
  const VIEW_SHORTCUT_KEYS: Record<ExtendedMainView, string> = {
    home: 'g h', resources: 'g r', issues: 'g i', topology: 'g t',
    applications: 'g a', timeline: 'g l', traffic: 'g f', helm: 'g m',
    gitops: 'g o', checks: 'g u', cost: 'g c',
    // Non-rail views (reachable via deep links / actions, not the rail) get no
    // dedicated mnemonic — listed for exhaustiveness so the type stays total.
    workload: '', compare: '', helmCompare: '',
  }
  const views = Object.keys(VIEW_SHORTCUT_KEYS).filter(
    (v): v is ExtendedMainView => VIEW_SHORTCUT_KEYS[v as ExtendedMainView] !== '',
  )
  useRegisterShortcuts([
    ...views.map((view) => ({
      id: `view-${view}`,
      keys: VIEW_SHORTCUT_KEYS[view],
      description: `Go to ${view.charAt(0).toUpperCase() + view.slice(1)}`,
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => setMainView(view),
    })),
    {
      id: 'switch-namespace',
      keys: 'n',
      description: 'Switch namespace',
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => namespaceSwitcherRef.current?.open(),
    },
    {
      id: 'switch-context',
      keys: 'c',
      description: 'Switch context',
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => contextSwitcherRef.current?.open(),
    },
    {
      id: 'theme-toggle',
      keys: 't',
      description: 'Toggle dark/light theme',
      category: 'General' as const,
      scope: 'global' as const,
      handler: () => toggleTheme(),
    },
    {
      id: 'help-toggle',
      keys: '?',
      description: 'Show keyboard shortcuts',
      category: 'General' as const,
      scope: 'global' as const,
      // Radar owns the shortcut registry even in a chromeless embed, so its `?`
      // overlay is the one that actually lists the working shortcuts. The host
      // (Radar Hub) drives it from its own chrome by dispatching a `?` keydown —
      // it has no registry of its own to populate a competing overlay with.
      handler: () => setShowHelp(prev => !prev),
    },
    {
      id: 'command-palette',
      keys: 'Cmd+k',
      description: 'Search resources & commands',
      category: 'General' as const,
      scope: 'global' as const,
      allowInInputs: true,
      // Standalone focuses the top-center omnibar; embedded opens the modal. In
      // a chromeless embed the HOST owns ⌘K (its own omnibar), so do nothing —
      // otherwise both the host omnibar and Radar's palette fire on one ⌘K.
      handler: () => { if (showNavRail) omnibarRef.current?.focus(); else if (!chromeless) setShowCommandPalette(true) },
    },
    {
      id: 'diagnostics',
      keys: 'Ctrl+Shift+d',
      description: 'Open diagnostics',
      category: 'General' as const,
      scope: 'global' as const,
      allowInInputs: true,
      handler: () => setShowDiagnostics(prev => !prev),
    },
    // Settings exposes local-binary controls that don't apply to embedded hosts.
    // Register the shortcut only when standalone (matching the gear button) —
    // `enabled: false` would still list it in the `?` help overlay, which shows
    // all registered shortcuts regardless of enabled state.
    ...(showNavRail
      ? [{
          id: 'open-settings',
          keys: 'g s',
          description: 'Open settings',
          category: 'General' as const,
          scope: 'global' as const,
          handler: () => setShowSettings(true),
        }]
      : []),
  ])

  // Separate registration for help-close — its `enabled` changes with showHelp,
  // and keeping it in the batch above would cause all stable shortcuts to churn.
  useRegisterShortcut({
    id: 'help-close',
    keys: 'Escape',
    description: 'Close overlay',
    category: 'General',
    scope: 'global',
    handler: () => setShowHelp(false),
    enabled: showHelp,
  })

  // Compute effective grouping mode:
  // - All namespaces: must use 'namespace' or 'app' (no 'none')
  // - Single/specific namespaces with 'none': use 'namespace' internally but hide header
  const hasNamespaceFilter = namespaces.length > 0
  const effectiveGroupingMode: GroupingMode = useMemo(() => {
    if (!hasNamespaceFilter && groupingMode === 'none') {
      // All namespaces view - force namespace grouping
      return 'namespace'
    }
    if (hasNamespaceFilter && groupingMode === 'none') {
      // Filtered namespaces with "no grouping" - use namespace grouping for layout
      return 'namespace'
    }
    return groupingMode
  }, [hasNamespaceFilter, groupingMode])

  // Hide group header when viewing a single namespace with namespace grouping —
  // the namespace name is already shown in the breadcrumb/picker. Preserve headers
  // for app/label grouping so those group boundaries remain visible.
  const hideGroupHeader = namespaces.length === 1 && effectiveGroupingMode === 'namespace'

  // Fetch available namespaces
  const { data: availableNamespaces } = useNamespaces()

  // Per-user view filter served by the backend. Loaded eagerly so the
  // picker can render its current state without showing the multi-select
  // fallback during the initial scope fetch.
  const { data: namespaceScope } = useNamespaceScope()

  // Context switch state
  const { isSwitching, targetContext, progressMessage, updateProgress, endSwitch } = useContextSwitch()

  // Connection state (for graceful startup)
  const { connection, retry: retryConnection, isRetrying, updateFromSSE: updateConnectionFromSSE } = useConnection()

  // The app's content surface is ready to show: auth resolved, not mid context-
  // switch, and the cluster connection is live. The main content area gates on
  // exactly this, and so do the overlay drawers — otherwise a deep-link/refresh
  // with `?resource=`/`?release=` renders the drawer on top of the connecting/
  // switching splash, pushing the centered loading logo off-center and showing an
  // empty drawer over a not-yet-loaded view. Gating both on the SAME readiness so
  // a drawer only ever sits over a real content surface.
  const contentReady = !isSwitching && !authMePending &&
    !(authMe?.authEnabled && !authMe?.username) && connection.state === 'connected'

  const { clusterLoadState, showHomeClusterLoadFallback, clusterLoadInitial } = useClusterLoadState({
    namespaces,
    mainView,
    chromeless,
    contentReady,
    onClusterLoadStateChange,
  })
  // Suppress the topbar warmup label during the initial dashboard fetch only on
  // Home, where the center "Loading dashboard…" splash already covers it. Off
  // Home there's no splash, so keep the label as the only text cue.
  const showClusterWarmupLabel = clusterLoadState.loading && !(clusterLoadInitial && mainView === 'home')

  // Query client for cache invalidation
  const queryClient = useQueryClient()

  // SSE-driven cache invalidation, split into two cadences so constant status
  // churn on large clusters doesn't force the *expensive* queries (big resource
  // lists + dashboard) to refetch every 3s. The core distinction: add/delete
  // changes what rows/counts exist (membership — keep fast); update is mostly
  // status/restart/health noise that can fire constantly on a 10k-pod cluster
  // and shouldn't drag a giant list onto a 3s cadence.
  //
  //   FAST (3s): detail drawer for any change (one cheap mounted object), and
  //     on add/delete: the list, counts, and dashboard. GitOps + cert keep
  //     their existing every-batch behavior — Phase 2 makes GitOps relevance-aware.
  //   SLOW (15s): list + dashboard for kinds with update churn. A kind that also
  //     had an add/delete in the window gets refreshed by both tiers (an extra
  //     refetch per 15s at most) — that's fine and avoids a stale-list bug:
  //     deduping by "was structural this window" would wrongly suppress an
  //     update that arrived *after* the fast structural flush already ran.
  const fastInvalidationRef = useRef<{
    changedKinds: Set<string>   // every changed kind (any op) → detail drawer
    structuralKinds: Set<string> // add/delete kinds → list membership + counts + dashboard
    secretsChanged: boolean
    timer: number | null
  }>({ changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null })
  const slowInvalidationRef = useRef<{
    updatedKinds: Set<string>    // update-only churn → throttled list + dashboard
    timer: number | null
  }>({ updatedKinds: new Set(), timer: null })
  const timelineInvalidationRef = useRef<{ timer: number | null }>({ timer: null })

  const handleK8sEvent = useCallback((event: K8sEvent) => {
    // The timeline consumes every frame — including the K8s Event kind the
    // resource tiers skip below (warnings like BackOff are timeline content).
    // Its own trailing throttle keeps the live view fresh within seconds
    // while batching bursts into one refetch; the 60s poll on useChanges
    // remains the no-SSE fallback.
    const tl = timelineInvalidationRef.current
    if (tl.timer === null) {
      tl.timer = window.setTimeout(() => {
        queryClient.invalidateQueries({ queryKey: ['changes'] })
        timelineInvalidationRef.current = { timer: null }
      }, 5000)
    }

    // Skip K8s Event kind — informational, not resource mutations
    if (event.kind === 'Event') return

    const kind = kindToPlural(event.kind)
    const structural = event.operation === 'add' || event.operation === 'delete'

    const fast = fastInvalidationRef.current
    fast.changedKinds.add(kind)
    if (structural) fast.structuralKinds.add(kind)
    if (kind === 'secrets') fast.secretsChanged = true

    const slow = slowInvalidationRef.current
    if (!structural) slow.updatedKinds.add(kind)

    // FAST tier — membership-sensitive + cheap, bounded 3s latency.
    if (fast.timer === null) {
      fast.timer = window.setTimeout(() => {
        const f = fastInvalidationRef.current
        for (const k of f.changedKinds) {
          queryClient.invalidateQueries({ queryKey: ['resource', k] }) // open detail drawer stays live
        }
        for (const k of f.structuralKinds) {
          queryClient.invalidateQueries({ queryKey: ['resources', k] }) // list membership changed
        }
        if (f.structuralKinds.size > 0) {
          queryClient.invalidateQueries({ queryKey: ['resource-counts'] })
          queryClient.invalidateQueries({ queryKey: ['dashboard'] })
        }
        if (f.secretsChanged) {
          queryClient.invalidateQueries({ queryKey: ['secret-cert-expiry'] })
        }
        // GitOps behavior unchanged from before — refreshes every batch when a
        // GitOps view is mounted (Phase 2 will make this relevance-aware).
        queryClient.invalidateQueries({ queryKey: ['gitops-tree'] })
        queryClient.invalidateQueries({ queryKey: ['gitops-insights'] })
        fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
      }, 3000)
    }

    // SLOW tier — throttle the expensive queries for status-only churn. Only
    // updates schedule it; structural changes are fully handled by the fast tier.
    if (!structural && slow.timer === null) {
      slow.timer = window.setTimeout(() => {
        const s = slowInvalidationRef.current
        for (const k of s.updatedKinds) {
          queryClient.invalidateQueries({ queryKey: ['resources', k] })
        }
        queryClient.invalidateQueries({ queryKey: ['dashboard'] }) // health reflects status updates
        slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }
      }, 15000)
    }
  }, [queryClient])

  // Clear pending invalidation timers on unmount. Reset the refs (not just
  // clearTimeout) so a same-instance remount doesn't inherit a non-null timer
  // id — handleK8sEvent only schedules when timer === null, so a stale id would
  // silently wedge all further SSE-driven invalidation.
  useEffect(() => () => {
    if (fastInvalidationRef.current.timer !== null) clearTimeout(fastInvalidationRef.current.timer)
    if (slowInvalidationRef.current.timer !== null) clearTimeout(slowInvalidationRef.current.timer)
    if (timelineInvalidationRef.current.timer !== null) clearTimeout(timelineInvalidationRef.current.timer)
    fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
    slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }
    timelineInvalidationRef.current = { timer: null }
  }, [])

  // SSE connection for real-time updates — no namespace filter for small/medium clusters (frontend filters).
  // forceNamespaceFilter is only set for large clusters that require server-side filtering.
  // Fleet mode uses 'resources' topology on the backend — filtering is client-side
  const sseMode = topologyMode === 'fleet' ? 'resources' : topologyMode
  const { topology, connected: eventStreamConnected, connecting: eventStreamConnecting, reconnect: reconnectEventStream } = useEventSource(namespaces, sseMode as 'resources' | 'traffic', {
    onContextSwitchComplete: endSwitch,
    onContextSwitchProgress: updateProgress,
    onContextChanged: () => {
      // Clear all React Query caches when cluster context changes
      // This ensures helm releases, resources, etc. are refetched from the new cluster
      // removeQueries clears cached data, invalidateQueries triggers refetch
      queryClient.removeQueries()
      queryClient.invalidateQueries()

      // Cancel any pending SSE-driven invalidation — old cluster's events are irrelevant
      if (fastInvalidationRef.current.timer !== null) clearTimeout(fastInvalidationRef.current.timer)
      if (slowInvalidationRef.current.timer !== null) clearTimeout(slowInvalidationRef.current.timer)
      if (timelineInvalidationRef.current.timer !== null) clearTimeout(timelineInvalidationRef.current.timer)
      fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
      slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }
      timelineInvalidationRef.current = { timer: null }

      // Close any open drawers/overlays — old cluster's resources don't exist on the new one
      // (?full=1 is cleared by the URL reset below).
      setSelectedResource(null)
      setSelectedHelmRelease(null)

      // Reset URL to current view with no resource-specific params.
      // Old cluster's selected pod/resource/kind don't exist on the new cluster.
      navigate({ pathname: location.pathname, search: '' }, { replace: true })

      // Auto-unpause so the new cluster's topology loads immediately
      setTopologyPaused(false)
      pendingTopologyRef.current = null
    },
    onConnectionStateChange: updateConnectionFromSSE,
    onDeferredReady: () => {
      // Deferred informers (secrets, events, configmaps, etc.) have finished syncing.
      // Refetch dashboard so counts, warning events, and cert health fill in.
      queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    },
    onK8sEvent: handleK8sEvent,
  }, forceNamespaceFilter, showPolicyEffect)
  // On large clusters (where the server requires namespace filtering), keep
  // SSE's server-side filter in lockstep with the user's namespace pick.
  // Without this, header switches and deep-link loads can leave SSE filtered
  // to a stale namespace while sidebar/topology show a different one. Small
  // clusters never set forceNamespaceFilter and skip this path entirely.
  useEffect(() => {
    const isLarge = forceNamespaceFilter !== undefined || topology?.requiresNamespaceFilter === true
    if (!isLarge) return
    if (namespaces.length === 0) {
      setForceNamespaceFilter(prev => (prev === undefined ? prev : undefined))
      return
    }
    setForceNamespaceFilter(prev => {
      const cur = prev ? [...prev].sort() : []
      const next = [...namespaces].sort()
      if (cur.length === next.length && cur.every((ns, i) => ns === next[i])) return prev
      return [...namespaces]
    })
  }, [namespaces, forceNamespaceFilter, topology?.requiresNamespaceFilter])

  // Apply live topology updates only when not paused. While paused, buffer the
  // latest snapshot so we can apply it instantly when the user resumes.
  useEffect(() => {
    if (!topologyPaused) {
      setDisplayedTopology(topology)
    } else {
      pendingTopologyRef.current = topology
    }
  }, [topology, topologyPaused])

  const handleTogglePause = useCallback(() => {
    setTopologyPaused(prev => {
      if (prev && pendingTopologyRef.current !== null) {
        // Resuming — apply the buffered snapshot immediately
        setDisplayedTopology(pendingTopologyRef.current)
        pendingTopologyRef.current = null
      }
      return !prev
    })
  }, [])

  // Track CRD discovery status from topology (more direct than cluster-info)
  // When discovery completes, topology will auto-update via SSE with new CRD nodes
  const crdDiscoveryStatus = topology?.crdDiscoveryStatus
  const clusterConnectionState = connection.state
  const clusterConnected = clusterConnectionState === 'connected'
  const liveUpdatesDisconnected = clusterConnected && !eventStreamConnected && !eventStreamConnecting
  const headerConnectionLabel =
    clusterConnectionState === 'disconnected' ? 'Disconnected' :
    clusterConnectionState === 'connecting' ? 'Connecting' :
    liveUpdatesDisconnected ? 'Live updates disconnected' :
    clusterLoadState.loading ? `Connected — ${clusterLoadState.message}` :
    crdDiscoveryStatus === 'discovering' ? 'Connected — discovering Custom Resources...' :
    'Connected'
  const headerConnectionDisplayLabel =
    clusterConnectionState === 'disconnected' ? 'Disconnected' :
    clusterConnectionState === 'connecting' ? 'Connecting' :
    liveUpdatesDisconnected ? 'Live updates disconnected' :
    showClusterWarmupLabel ? clusterLoadState.message :
    crdDiscoveryStatus === 'discovering' ? 'Discovering Custom Resources…' :
    ''
  const showHeaderReconnect =
    clusterConnectionState === 'disconnected' ||
    liveUpdatesDisconnected
  const headerReconnect = clusterConnectionState === 'disconnected' ? retryConnection : reconnectEventStream
  const headerReconnectPending = clusterConnectionState === 'disconnected' ? isRetrying : false

  // Debug: log discovery status changes
  useEffect(() => {
    if (crdDiscoveryStatus) {
      console.log('[CRD Discovery] Status:', crdDiscoveryStatus)
    }
  }, [crdDiscoveryStatus])

  // Auto-add CRD kinds (not in ALL_NODE_KINDS) to visibleKinds the first time they appear.
  // Uses a ref to track which kinds have been seeded so user toggle-off choices are preserved.
  const allNodeKindsSet = useMemo(() => new Set<string>(ALL_NODE_KINDS), [])
  useEffect(() => {
    if (!topology?.nodes) return
    const newKinds: NodeKind[] = []
    for (const node of topology.nodes) {
      const k = node.kind as string
      if (!allNodeKindsSet.has(k) && !seededCRDKindsRef.current.has(k)) {
        seededCRDKindsRef.current.add(k)
        if (!CRD_HIDDEN_BY_DEFAULT.has(k)) {
          newKinds.push(node.kind)
        }
      }
    }
    if (newKinds.length > 0) {
      setVisibleKinds(prev => {
        const next = new Set(prev)
        for (const k of newKinds) next.add(k)
        return next
      })
    }
  }, [topology, allNodeKindsSet])

  // Handle node selection - convert TopologyNode to SelectedResource for the drawer
  const handleNodeClick = useCallback((node: TopologyNode) => {
    // Skip Internet node - it's not a real resource
    if (node.kind === 'Internet') return

    // For PodGroup, we can't open a single resource drawer
    // TODO: Could show a list of pods in the group
    if (node.kind === 'PodGroup') return

    const namespace = (node.data.namespace as string) || ''
    // GitOps CRs (Application/Kustomization/HelmRelease/etc.) have a dedicated
    // detail page with tree + insights + ops that the drawer can't reproduce.
    // Route there from the main topology when the node is one of those kinds;
    // everything else falls back to the drawer.
    const gitOpsPath = gitOpsRouteForKind(node.kind, namespace, node.name)
    if (gitOpsPath) {
      navigate(gitOpsPath)
      return
    }

    navigateToResource({
      kind: kindToPlural(node.kind),
      namespace,
      name: node.name,
      group: apiVersionToGroup(node.data.apiVersion as string | undefined),
    })
  }, [navigate, navigateToResource])

  // Serialize namespaces for stable dependency tracking
  const namespacesKey = namespaces.join(',')

  // The server is canonical for the per-user namespace pick. Mirror its
  // `actives` into App.tsx state so consumer hooks (SSE, dashboard, resource
  // lists) stay in lockstep with the picker. The dedicated URL-write effect
  // below propagates the mirrored state to `?namespaces=`.
  const setActiveNamespace = useSetActiveNamespace()
  // Defer the state flip to onSuccess. Setting namespaces to [] before the
  // server-side pref has actually been cleared makes React Query refetch
  // under the new empty key while the server still returns the previous
  // pick's scope, caching stale data under the new key with no later
  // invalidation. onSettled would do the same on errors, leaving the UI
  // showing "All namespaces" while data is still namespace-scoped — onSuccess
  // keeps state aligned with the server.
  //
  // Don't touch the URL here either: setSearchParams on a still-set state
  // trips the URL→state sync into firing setNamespaces([]) and a duplicate
  // mutation immediately, which re-introduces the same race. The state→URL
  // effect propagates state=[] → URL on its own after onSuccess flips state.
  const clearAllNamespaces = useCallback(() => {
    if (namespaceScope?.cacheScoped) return
    if (namespaces.length === 0) return
    setActiveNamespace.mutate(
      { namespaces: [] },
      { onSuccess: () => setNamespaces([]) },
    )
  }, [namespaceScope?.cacheScoped, namespaces.length, setActiveNamespace])
  const initialBookmarkReconciledRef = useRef(false)
  const scopeActives = useMemo(() => namespaceScope?.actives ?? [], [namespaceScope?.actives])
  const namespaceScopeKey = useMemo(() => namespaceScope ? [...scopeActives].sort().join(',') : null, [namespaceScope, scopeActives])
  useEffect(() => {
    if (!namespaceScope) return
    const sortedScope = [...scopeActives].sort()
    const sortedState = [...namespaces].sort()
    const sameAsState = sortedScope.length === sortedState.length && sortedScope.every((ns, i) => ns === sortedState[i])
    debugNamespaceLog('app:scope-mirror', {
      scopeActives,
      stateNamespaces: namespaces,
      sameAsState,
      initialBookmarkReconciled: initialBookmarkReconciledRef.current,
    })

    // First-load bookmark reconciliation: if the URL had namespaces that
    // differ from the server pick when the scope first arrives, push the
    // URL choice to the server so shared/bookmarked deep links keep
    // working. The ref flips on the first scope load regardless of whether
    // the URL had namespaces — subsequent runs mirror server → state.
    if (!initialBookmarkReconciledRef.current) {
      initialBookmarkReconciledRef.current = true
      if (!sameAsState && sortedState.length > 0) {
        if (namespaceScope.cacheScoped && (!namespaceScope.namespaceRescope || sortedState.length !== 1)) {
          debugNamespaceLog('app:scope-mirror-cache-scope-preserve', {
            stateNamespaces: sortedState,
            scopeActives: sortedScope,
          })
          setNamespaces(scopeActives)
          return
        }
        debugNamespaceLog('app:scope-mirror-bookmark-to-server', {
          stateNamespaces: sortedState,
          scopeActives: sortedScope,
        })
        setActiveNamespace.mutate({ namespaces: sortedState })
        return
      }
    }

    if (!sameAsState) {
      debugNamespaceLog('app:scope-mirror-set-namespaces', { nextNamespaces: scopeActives })
      setNamespaces(scopeActives)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- namespaces and setActiveNamespace are intentionally excluded; we only react to server-side changes.
  }, [namespaceScope, namespaceScopeKey])

  // Arm the skip on every history Pop (location.key changes per nav), then
  // clear it on the next macrotask. The revert/oscillation is a synchronous
  // re-render burst, so a macrotask-deferred clear covers it; clearing
  // afterward means a stale arm can't survive into an unrelated later write
  // (e.g. a Pop that changes none of the write effect's deps would otherwise
  // leave the flag set and silently drop the next user-driven URL write).
  useEffect(() => {
    if (navigationType !== NavigationType.Pop) {
      // Any non-Pop navigation clears the guard. Without this, a Push/Replace
      // that lands before the macrotask fires would run this cleanup (cancelling
      // the timeout) and re-run as a no-op, leaving the flag stuck true and
      // silently suppressing all later URL writes.
      skipUrlWriteAfterPopRef.current = false
      return
    }
    skipUrlWriteAfterPopRef.current = true
    const id = setTimeout(() => { skipUrlWriteAfterPopRef.current = false }, 0)
    return () => clearTimeout(id)
  }, [location.key, navigationType])

  // Update URL query params when state changes (path is handled by setMainView)
  // Read from window.location.search (not React Router's searchParams) to preserve
  // params set by child components via window.history.replaceState (e.g., kind from ResourcesView).
  useEffect(() => {
    // Don't write (and revert) the URL while state is still catching up to a
    // Pop — the read effect below owns syncing state from the popped URL. The
    // flag auto-clears on the next macrotask, so this never blocks a later
    // user-driven write.
    if (skipUrlWriteAfterPopRef.current) return
    const currentSearch = window.location.search
    const params = new URLSearchParams(currentSearch)

    // Update namespaces param
    if (namespaces.length > 0) {
      params.set('namespaces', namespaces.join(','))
    } else {
      params.delete('namespaces')
    }
    // Remove legacy 'namespace' param if present
    params.delete('namespace')

    // Topology-specific params: only set when on topology view, clean up otherwise
    if (mainView === 'topology') {
      if (topologyMode !== 'resources') {
        params.set('mode', topologyMode)
      } else {
        params.delete('mode')
      }
      if (groupingMode !== 'none' && (namespaces.length === 0 || groupingMode !== 'namespace')) {
        params.set('group', groupingMode)
      } else {
        params.delete('group')
      }
    } else {
      params.delete('mode')
      params.delete('group')
    }

    // Only update if params actually changed vs current URL
    if (params.toString() !== new URLSearchParams(currentSearch).toString()) {
      debugNamespaceLog('app:url-write', {
        namespaces,
        currentSearch,
        nextSearch: params.toString(),
        mainView,
      })
      setSearchParams(params, { replace: true })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- reads window.location.search, not searchParams
  }, [namespacesKey, topologyMode, groupingMode, mainView, setSearchParams])

  // Sync namespace + helm picks from the query string only when the query
  // string changes. If this also ran on pathname / mainView changes, a view
  // whose URL omits ?namespaces= would clear App state and POST [] to the
  // server while the per-user pick was still narrowed — the picker would
  // show the server scope but lists/dashboard would stay on "all namespaces".
  useEffect(() => {
    const urlNamespaces = parseNamespacesFromURL(searchParams)
    debugNamespaceLog('app:url-sync', {
      search: searchParams.toString(),
      urlNamespaces,
      stateNamespaces: namespaces,
      namespacesKey,
    })

    if (urlNamespaces.join(',') !== namespacesKey) {
      if (namespaceScope?.cacheScoped && (!namespaceScope.namespaceRescope || urlNamespaces.length !== 1)) {
        const scopedNamespaces = namespaceScope.actives ?? []
        debugNamespaceLog('app:url-sync-cache-scope-preserve', { scopedNamespaces })
        setNamespaces(scopedNamespaces)
        return
      }
      debugNamespaceLog('app:url-sync-set-namespaces', { nextNamespaces: urlNamespaces })
      setNamespaces(urlNamespaces)
      if (namespaceScope) {
        const sortedURL = [...urlNamespaces].sort()
        const sortedScope = [...(namespaceScope.actives ?? [])].sort()
        const same = sortedURL.length === sortedScope.length && sortedURL.every((ns, i) => ns === sortedScope[i])
        if (!same) {
          debugNamespaceLog('app:url-sync-mutate-server', {
            urlNamespaces,
            scopeActives: namespaceScope.actives ?? [],
          })
          setActiveNamespace.mutate({ namespaces: urlNamespaces })
        }
      }
    }

    const releaseParam = searchParams.get('release')
    if (releaseParam) {
      const slashIdx = releaseParam.indexOf('/')
      if (slashIdx > 0) {
        const ns = releaseParam.slice(0, slashIdx)
        const name = releaseParam.slice(slashIdx + 1)
        setSelectedHelmRelease({ namespace: ns, name, storageNamespace: searchParams.get('releaseStorage') || undefined })
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- run only when searchParams change; namespacesKey/namespaceScope are read for that transition
  }, [searchParams])

  useEffect(() => {
    if (navigationType !== NavigationType.Pop || mainView !== 'resources') return
    const kindFromPath = location.pathname.match(/^\/resources\/([^/]+)/)?.[1] ?? ''
    const resourceParam = searchParams.get('resource')
    if (kindFromPath && resourceParam) {
      const slashIdx = resourceParam.indexOf('/')
      const ns = slashIdx > 0 ? resourceParam.slice(0, slashIdx) : ''
      const name = slashIdx > 0 ? resourceParam.slice(slashIdx + 1) : resourceParam
      const apiGroup = searchParams.get('apiGroup') ?? ''
      const next: SelectedResource = { kind: kindFromPath, namespace: ns, name, group: apiGroup }
      setSelectedResource(prev => {
        if (
          prev &&
          prev.kind === next.kind &&
          prev.namespace === next.namespace &&
          prev.name === next.name &&
          (prev.group ?? '') === (next.group ?? '')
        ) return prev
        return next
      })
    } else if (kindFromPath && !resourceParam) {
      setSelectedResource(prev => (prev === null ? prev : null))
    }
  }, [navigationType, mainView, location.pathname, searchParams])

  // Auto-adjust grouping when namespaces change
  useEffect(() => {
    if (namespaces.length === 0 && groupingMode === 'none') {
      // Switching to all namespaces - enable namespace grouping by default
      setGroupingMode('namespace')
    } else if (namespaces.length > 0 && groupingMode === 'namespace') {
      // Switching to specific namespaces - disable namespace grouping
      setGroupingMode('none')
    }
    // Intentionally runs ONLY when the namespace selection changes. It reads the
    // current groupingMode but must not re-run when grouping changes, or it would
    // immediately revert a manual/fleet grouping choice. namespacesKey is the
    // manual dependency standing in for the namespaces array.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namespacesKey])

  // Clear resource selection when changing views or namespaces
  // But preserve selectedResource when navigating TO resources view (e.g., from Helm deep link)
  const prevMainView = useRef(mainView)
  useEffect(() => {
    const navigatingToResources = mainView === 'resources' && prevMainView.current !== 'resources'
    prevMainView.current = mainView

    // The URL is the source of truth for what's selected. A deep link
    // (?resource=, ?release=) seeds the selection on mount; the effects that
    // run during that same mount must not wipe a selection the URL still
    // asserts. (On a real view switch the URL no longer carries the param, so
    // the clear proceeds.) Without this, deep-linking straight to a Helm
    // release lands on the release list with no drawer.
    // (drawerExpanded is URL-derived from ?full=1, so leaving /resources drops it
    // automatically — no explicit reset needed.)
    const params = new URLSearchParams(window.location.search)
    if (!navigatingToResources && !params.has('resource')) {
      setSelectedResource(null)
    }
    if (!params.has('release')) {
      setSelectedHelmRelease(null)
    }
  }, [mainView])

  // Clear resource selection when namespaces change — but keep a selection the
  // URL still asserts (deep link, or a release/resource the user is viewing
  // while they adjust the namespace scope filter).
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    if (!params.has('resource')) setSelectedResource(null)
    if (!params.has('release')) setSelectedHelmRelease(null)
  }, [namespacesKey])

  // Filter topology based on visible kinds (uses displayedTopology which respects pause)
  const filteredTopology = useMemo((): Topology | null => {
    if (!displayedTopology) return null

    // Fleet mode overrides visible kinds to show only CAPI resources + Node
    const effectiveKinds = topologyMode === 'fleet' ? FLEET_MODE_KINDS : visibleKinds

    // Filter by namespace (client-side) and by visible kinds
    const nsSet = namespaces.length > 0 ? new Set(namespaces) : null
    const filteredNodes = displayedTopology.nodes.filter(node =>
      effectiveKinds.has(node.kind) &&
      (!nsSet || nsSet.has(node.data.namespace as string) || !(node.data.namespace as string))
    )
    const filteredNodeIds = new Set(filteredNodes.map(n => n.id))

    // Keep edges where both source and target are visible
    // Also respect skipIfKindVisible - hide shortcut edges when intermediate kind is shown
    const filteredEdges = displayedTopology.edges.filter(edge => {
      // Both endpoints must be visible
      if (!filteredNodeIds.has(edge.source) || !filteredNodeIds.has(edge.target)) {
        return false
      }
      // If this is a shortcut edge, hide it when the intermediate kind is visible
      if (edge.skipIfKindVisible && effectiveKinds.has(edge.skipIfKindVisible as NodeKind)) {
        return false
      }
      return true
    })

    return {
      ...displayedTopology,
      nodes: filteredNodes,
      edges: filteredEdges,
    }
  }, [displayedTopology, visibleKinds, namespaces, topologyMode])

  // Cluster Audit findings, joined onto topology nodes by the audit key the
  // backend stamps on each node (data.auditKey). The graph surfaces DANGER only
  // (warnings would turn a dense graph into a heatmap); the node component reads
  // data.auditDanger. Re-runs only when findings change, and copies nodes only
  // when there are findings to attach — no overhead on clusters with none.
  const audit = useAudit(namespaces)
  const auditSeverityMap = useMemo(
    () => buildAuditSeverityMap(audit.data?.findings, audit.data?.checks),
    [audit.data?.findings, audit.data?.checks],
  )
  const topologyWithAudit = useMemo((): Topology | null => {
    if (!filteredTopology) return null
    if (auditSeverityMap.size === 0) return filteredTopology
    return {
      ...filteredTopology,
      nodes: filteredTopology.nodes.map(node => {
        const counts = auditSeverityMap.get(node.data.auditKey as string)
        if (!counts) return node
        return { ...node, data: { ...node.data, auditDanger: counts.danger, auditWarning: counts.warning, auditMessages: counts.messages } }
      }),
    }
  }, [filteredTopology, auditSeverityMap])

  // The graph node id of the currently open resource, used to highlight it on
  // the canvas. Looked up from the topology (not reconstructed) because node
  // ids are `<lowercaseKind>/<ns>/<name>` with special prefixes for CRD
  // collisions — rebuilding the string can't match those reliably.
  const selectedNodeId = useMemo(() => {
    if (!selectedResource) return undefined
    const ns = selectedResource.namespace || ''
    const match = topology?.nodes.find(n =>
      ((n.data.namespace as string) || '') === ns &&
      n.name === selectedResource.name &&
      (kindToPlural(n.kind) === selectedResource.kind || n.kind === selectedResource.kind)
    )
    return match?.id
  }, [selectedResource, topology])

  // Filter handlers
  const handleToggleKind = useCallback((kind: NodeKind) => {
    setVisibleKinds(prev => {
      const next = new Set(prev)
      if (next.has(kind)) {
        next.delete(kind)
      } else {
        next.add(kind)
      }
      return next
    })
  }, [])

  const handleShowAllKinds = useCallback(() => {
    // Include all static kinds plus any dynamic CRD kinds from the topology
    const allKinds = new Set<NodeKind>(ALL_NODE_KINDS)
    if (topology?.nodes) {
      for (const node of topology.nodes) {
        allKinds.add(node.kind)
      }
    }
    setVisibleKinds(allKinds)
  }, [topology])

  const handleHideAllKinds = useCallback(() => {
    setVisibleKinds(new Set())
  }, [])

  const navActiveView = mainView === 'helmCompare' ? 'helm' : mainView

  return (
    <PortForwardProvider>
    {/* Preserve the ~800px content floor: the rail is a fixed-width sibling, so
        the outer minimum must include it (176px pinned / 56px collapsed) or the
        content column (min-w-0, shrinkable) would fall below the old desktop
        floor at small windows. Embedded mode has no rail → plain 800. */}
    <div
      className={`relative flex bg-theme-base ${navCustomization.embedded ? 'h-full min-h-0' : 'h-screen'}`}
      style={{ minWidth: 800 + (showNavRail ? (navRailEffectivePinned ? 176 : 56) : 0) }}
    >
      {showNavRail && (
        <PrimaryNavRail
          activeView={navActiveView}
          onNavigate={setMainView}
          pinned={navRailEffectivePinned}
          onTogglePinned={toggleNavRailPinned}
          showPinToggle={!railForcedSlim}
          onOpenSettings={() => setShowSettings(true)}
          accountSlot={<UserMenu variant="rail" pinned={navRailEffectivePinned} />}
        />
      )}
      {/* `relative` makes this column the containing block for the absolute
          overlays it hosts (BottomDock, expanded ResourceDetailDrawer) so they
          span the content area AFTER the rail rather than the full viewport
          under it. `fixed` splashes (connecting/switching) are unaffected. */}
      <div className="relative flex flex-col flex-1 min-w-0 h-full">
      {/* Header — suppressed in chromeless embed; the host owns the chrome.
          @container: the header's responsive layout keys off its OWN width (≈ viewport
          − nav rail), not the viewport, so it collapses gracefully on narrow windows.
          The AI panel docks BELOW the navbar and pushes only the content region, so the
          navbar is never squeezed by it — these thresholds react to real window width. */}
      {!chromeless && (
      <header className="@container relative z-50 flex items-center justify-between px-4 py-2 bg-theme-base/90 backdrop-blur-sm border-b border-theme-border/50">
        {/* Left: Logo + Cluster info. In the standalone (nav-rail) layout this
            is a FIXED-WIDTH column so the omnibar after it is force-pinned: the
            scope pill + status dot can change width (cluster/namespace value)
            without ever shifting the search box. The pill's own name/value caps
            keep it inside this width; the embedded/pill layout keeps auto width
            (its center bar is absolutely positioned). */}
        <div className={`flex items-center gap-4 shrink-0 ${showNavRail ? 'w-[492px]' : ''}`}>
          {/* Standalone rail owns the brand; only the embedded/pill layout
              shows it in the header (host may override via brandSlot). */}
          {navCustomization.brandSlot ?? (showNavRail ? null : <Logo />)}

          <div className={`flex items-center gap-2 min-w-0 ${showNavRail ? 'flex-1' : ''}`}>
            {navCustomization.contextSlot ? (
              // Embedded host supplies its own cluster switcher — keep the two
              // controls separate (the host owns the cluster chip's styling).
              <>
                {navCustomization.contextSlot}
                <NamespaceSwitcher
                  ref={namespaceSwitcherRef}
                  disabled={namespaceFilter.disabled}
                  disabledTooltip={namespaceFilter.tooltip}
                />
              </>
            ) : (
              // Standalone: cluster + namespace as one "scope" pill — two
              // borderless segments split by a divider, reading as a single
              // "what am I looking at" unit. The shared ScopePill shell is the
              // same one Radar Hub's cluster top bar uses, so the two match.
              <ScopePill>
                <ContextSwitcher ref={contextSwitcherRef} variant="segment" />
                <NamespaceSwitcher
                  ref={namespaceSwitcherRef}
                  variant="segment"
                  disabled={namespaceFilter.disabled}
                  disabledTooltip={namespaceFilter.tooltip}
                />
              </ScopePill>
            )}
            {/* Connection status — a fixed-size dot (state in the tooltip), an
                optional reconnect button, and when the header is wide enough
                (xl+), a label. The label is nowrap and unbounded: it overflows
                the fixed left column into the empty gap before the centered
                search box rather than shifting anything (the dot + pill are
                shrink-0, so layout stays put — the search box never moves).
                Where the gap is smaller than the label, its tail tucks under
                the omnibar's solid background. Below xl it's the dot alone
                unless reconnect is available. */}
            <div className="ml-1 flex items-center gap-1.5 shrink-0">
              <Tooltip
                content={headerConnectionLabel}
                delay={100}
                position="bottom"
              >
                <span
                  className={`block w-2.5 h-2.5 shrink-0 rounded-full ${
                    clusterConnectionState === 'disconnected' || liveUpdatesDisconnected
                      ? 'bg-red-500'
                      : clusterConnectionState === 'connecting' || crdDiscoveryStatus === 'discovering' || clusterLoadState.loading
                        ? 'bg-amber-400 animate-pulse'
                        : 'bg-green-500'
                  }`}
                />
              </Tooltip>
              {showNavRail && headerConnectionDisplayLabel && (
                <span className="hidden xl:flex items-center gap-1.5 whitespace-nowrap text-[11px] text-theme-text-tertiary">
                  {showClusterWarmupLabel && <Loader2 className="w-3 h-3 animate-spin" />}
                  {headerConnectionDisplayLabel}
                </span>
              )}
              {showHeaderReconnect && (
                <Tooltip content="Reconnect" delay={100} position="bottom">
                  <button
                    type="button"
                    onClick={headerReconnect}
                    disabled={headerReconnectPending}
                    aria-label={clusterConnectionState === 'disconnected' ? 'Reconnect cluster' : 'Reconnect live updates'}
                    className="p-1 text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50 disabled:pointer-events-none"
                  >
                    <RefreshCw className={`w-3 h-3 ${headerReconnectPending ? 'animate-spin' : ''}`} />
                  </button>
                </Tooltip>
              )}
            </div>
            {/* Port forwards indicator — shown only when sessions exist */}
            <PortForwardIndicator />
          </div>
        </div>

        {/* Center: View tabs — embedded/pill layout only. Standalone Radar
            navigates via the left rail (showNavRail), so the pill bar is
            suppressed there to avoid a duplicate primary nav. */}
        {!showNavRail && (
        <div className="@min-[920px]:absolute @min-[920px]:left-1/2 @min-[920px]:-translate-x-1/2 flex items-center gap-0.5 bg-theme-elevated/50 rounded-full p-1 ml-2 @min-[920px]:ml-0">
          {([
            { view: 'home' as const, icon: Home, label: 'Home' },
            { view: 'topology' as const, icon: Network, label: 'Topology' },
            { view: 'resources' as const, icon: List, label: 'Resources' },
            { view: 'timeline' as const, icon: Clock, label: 'Timeline' },
            { view: 'helm' as const, icon: Package, label: 'Helm' },
            { view: 'gitops' as const, icon: GitBranch, label: 'GitOps' },
            // Applications is intentionally hidden from the pill bar for now —
            // the bar is full, and the view's primary home is Cloud's fleet
            // rail. The view still exists and is reachable via /applications
            // and the view-switching shortcuts. Same treatment as Cost below.
            { view: 'traffic' as const, icon: Activity, label: 'Live Traffic' },
            // Cost is intentionally hidden from the pill bar for now — the view still
            // exists and is reachable via /cost, the Home dashboard card, and the
            // command palette (⌘K). Remove this comment to restore it.
            { view: 'checks' as const, icon: ShieldCheck, label: 'Checks' },
          ] as const)
            // In Cloud, fleet-shaped views (Checks, Issues, GitOps) are owned by
            // the host's left rail; the per-cluster view is just that fleet page
            // filtered to this cluster, so duplicating it as a peer pill here
            // would be a second copy that teleports out of the cluster shell.
            // Drop any pill the host took over — cluster-scoped access stays
            // available via the Home cards (redirected by the takeover effect
            // above), ⌘K, and bookmarks. Standalone OSS keeps every pill.
            .filter(({ view }) => !isViewTakenOver(view))
            .map(({ view, icon: Icon, label }) => (
            <Tooltip key={view} content={label} delay={100} position="bottom">
              <button
                onClick={() => setMainView(view)}
                className={`flex items-center gap-1 px-2 py-1 text-[13px] rounded-full transition-colors ${
                  mainView === view || (mainView === 'helmCompare' && view === 'helm')
                    ? 'bg-skyhook-600 dark:bg-skyhook-500 text-white shadow-glow-brand-sm'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
                }`}
              >
                <Icon className="w-4 h-4" />
                {/* Labels appear only when the absolute-centered nav has
                    enough horizontal room past the left section. Right-side
                    chrome that adds further pressure (Connected text, star
                    count) is intentionally pushed to the next tier (xl) so
                    label rendering and right-side expansion stay decoupled.
                    Per-button Tooltip discloses labels on hover when the
                    icon-only viewport is in effect. The 1440 anchor is an
                    off-system breakpoint chosen by measurement at the time
                    of this PR — recompute if the cluster switcher cap or
                    other left-section chrome changes appreciably. */}
                <span className="hidden @min-[1264px]:inline">{label}</span>
              </button>
            </Tooltip>
          ))}
        </div>
        )}

        {/* Center: omnibar — standalone search + command surface (the ⌘K entry).
            Its container is one of three EQUAL flex-1 columns (see the left/right
            groups): equal side columns keep this middle column — and the search
            box centered in it — pinned regardless of how wide the left-side
            chrome gets (cluster name, namespace label, "Discovering…" /
            "Disconnected" text). Overflowing side content truncates instead of
            dragging the box. Same pattern as Radar Hub's ClusterTopBar. */}
        {showNavRail && (
          <div className="hidden @min-[720px]:flex flex-1 justify-center min-w-0 px-3">
            <RadarOmnibar
              ref={omnibarRef}
              onNavigateView={(view) => setMainView(view)}
              onNavigateKind={(kind, group) => {
                const params = new URLSearchParams(searchParams)
                params.delete('kind')
                if (group) params.set('apiGroup', group); else params.delete('apiGroup')
                params.delete('resource')
                params.delete('full')
                params.delete('tab')
                navigate({ pathname: `/resources/${kind}`, search: params.toString() })
              }}
              onSwitchContext={(name) => switchContext.mutate({ name }, { onSettled: () => setNamespaces([]) })}
              onSetNamespaces={(ns) => { setNamespaces(ns); setActiveNamespace.mutate({ namespaces: ns }) }}
              onToggleTheme={toggleTheme}
              onShowDiagnostics={() => setShowDiagnostics(true)}
              onOpenResource={(hit) => navigateToResourceList(searchHitToSelectedResource(hit))}
            />
          </div>
        )}

        {/* Right: Controls */}
        <div className="flex items-center gap-3 shrink-0">
          {/* Command palette trigger — embedded only; standalone has the
              top-center omnibar (which is the ⌘K surface). */}
          {!showNavRail && (
          <button
            onClick={() => setShowCommandPalette(true)}
            aria-label="Open command palette"
            className="hidden lg:flex items-center gap-2 h-7 px-2.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
          >
            <Search className="w-3.5 h-3.5" />
            <kbd className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded border border-theme-border-light">
              {typeof navigator !== 'undefined' && navigator.platform.includes('Mac') ? '⌘' : 'Ctrl+'}K
            </kbd>
          </button>
          )}

          {/* GitHub star — hidden in embedded mode (not OSS-distribution chrome). */}
          {!navCustomization.embedded && (
            <div className="hidden @min-[1100px]:block">
              <GitHubStarButton />
            </div>
          )}

          {/* AI investigations (self-hides when no agent CLI is present) */}
          <GlobalDiagnoseButton />

          {/* Local terminal */}
          {capabilities.localTerminal && (
            <Tooltip content="Open local terminal">
            <button
              onClick={() => openLocalTerminal()}
              aria-label="Open local terminal"
              className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              <SquareTerminal className="w-4 h-4" />
            </button>
            </Tooltip>
          )}

          {/* Theme toggle — hidden in embedded mode. Host apps (e.g. Radar
              Cloud) own the user-theme preference and mount their own picker
              in the account menu; a second toggle in Radar's topbar would
              fight them (one writes to Radar's localStorage key, the other
              to the host's cookie/backend) and the user would see the theme
              bounce on every navigation between host routes and /c/:id. */}
          {!navCustomization.embedded && (
            <div className="hidden @min-[920px]:flex items-center">
              <ThemeToggle />
            </div>
          )}

          {/* Help + Report-a-bug — standalone only (the left rail owns chrome;
              embedded hosts provide their own help/support). These replace the
              old floating bottom-right pair. Settings moved to the rail bottom. */}
          {showNavRail && (
            <>
              <Tooltip content="Keyboard shortcuts (?)">
              <button
                onClick={() => setShowHelp(true)}
                aria-label="Show keyboard shortcuts"
                className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                <HelpCircle className="w-4 h-4" />
              </button>
              </Tooltip>
              <Tooltip content="Report a bug / Diagnostics">
              <button
                onClick={() => setShowDiagnostics(true)}
                aria-label="Open diagnostics"
                className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                <Bug className="w-4 h-4" />
              </button>
              </Tooltip>
            </>
          )}

          {/* Account moved to the rail bottom (standalone). Embedded never showed
              Radar's UserMenu — the host provides its own via rightExtras. */}

          {/* Consumer-provided extras (e.g. Radar Hub's Install button +
              avatar menu) appended to the right of the action bar. */}
          {navCustomization.rightExtras}
        </div>
      </header>
      )}

      {/* Body frame — every content state lives here and reflows left of the docked
          AI panel (an absolute slot in this column). The header + nav rail are OUTSIDE
          this wrapper, so they never move when the panel opens. */}
      <div className="relative flex flex-1 flex-col min-h-0" style={{ paddingRight: contentGutter, transition: 'padding-right 0.2s ease' }}>

      {/* Auth barrier - show when auth is enabled but user is not authenticated */}
      {authMe?.authEnabled && !authMe?.username && authMe.authMode === 'proxy' && (
        <AuthBarrier authMode="proxy" />
      )}
      {authMe?.authEnabled && !authMe?.username && authMe.authMode === 'oidc' && (
        <AuthBarrier authMode="oidc" />
      )}

      {/* Connection error view - show when disconnected */}
      {!isSwitching && !(authMe?.authEnabled && !authMe?.username) && connection.state === 'disconnected' && (
        <ConnectionErrorView
          connection={connection}
          onRetry={retryConnection}
          isRetrying={isRetrying}
        />
      )}

      {/* Connecting view — shown during initial connection or retry.
          Icon is pane-anchored so its screen position matches the
          host hub splash across cross-document transitions. */}
      {!isSwitching && !(authMe?.authEnabled && !authMe?.username) && connection.state === 'connecting' && (
        <PaneLoader
          label="Connecting to cluster"
          className="flex-1 min-h-0 bg-theme-base"
        >
          {connection.context && (
            <span className="mt-1 block text-sm font-normal tracking-normal text-theme-text-secondary">
              {connection.context}
            </span>
          )}
          {connection.progressMessage && (
            <span className="mt-3 block text-xs font-normal tracking-normal text-theme-text-tertiary animate-pulse">
              {connection.progressMessage}
            </span>
          )}
        </PaneLoader>
      )}

      {/* Context switching overlay */}
      {isSwitching && (
        <PaneLoader
          label="Switching context"
          className="flex-1 min-h-0 bg-theme-base"
        >
          {targetContext && (
            <span className="mt-2 block text-xs font-normal tracking-normal text-theme-text-tertiary">
              {targetContext.provider ? (
                <span className="flex items-center justify-center gap-1.5">
                  <span className="text-blue-400 font-medium">{targetContext.provider}</span>
                  {targetContext.account && (
                    <>
                      <span className="text-theme-text-tertiary/50">•</span>
                      <span>{targetContext.account}</span>
                    </>
                  )}
                  {targetContext.region && (
                    <>
                      <span className="text-theme-text-tertiary/50">•</span>
                      <span>{targetContext.region}</span>
                    </>
                  )}
                  <span className="text-theme-text-tertiary/50">•</span>
                  <span className="text-theme-text-secondary font-medium">{targetContext.clusterName}</span>
                </span>
              ) : (
                <span>{targetContext.raw}</span>
              )}
            </span>
          )}
          {progressMessage && (
            <span className="mt-3 block text-xs font-normal tracking-normal text-theme-text-tertiary animate-pulse">
              {progressMessage}
            </span>
          )}
        </PaneLoader>
      )}

      {/* Main content - only show when connected and authenticated */}
      {/* inert while a fullscreen detail overlay covers the views — keeps the
          retained background list out of the focus order + a11y tree (the visual
          cover already blocks pointer events). */}
      {contentReady && <div className="flex-1 flex overflow-hidden" inert={expandedView}>
        <ErrorBoundary>
        {/* Home dashboard */}
        {mainView === 'home' && (
          <HomeView
            namespaces={namespaces}
            topology={topology}
            fallbackClusterLoadState={showHomeClusterLoadFallback ? clusterLoadState : undefined}
            onNavigateToView={setMainView}
            onNavigateToResourceKind={(kind, apiGroup, filters) => {
              // Navigate to resources view with kind in URL path
              console.debug('[filters] App.onNavigateToResourceKind:', { kind, apiGroup, filters })
              const newParams = new URLSearchParams(searchParams)
              newParams.delete('kind') // kind is now in the path
              newParams.delete('mode')
              newParams.delete('resource')
              newParams.delete('full') // don't carry an expanded-overlay flag onto a fresh kind list
              newParams.delete('tab')
              newParams.delete('group') // Clear topology grouping param to avoid leaking into resources view
              if (apiGroup) {
                newParams.set('apiGroup', apiGroup)
              } else {
                newParams.delete('apiGroup')
              }
              // Apply column filters if provided
              if (filters && Object.keys(filters).length > 0) {
                const filtersStr = serializeColumnFilters(filters)
                if (filtersStr) {
                  newParams.set('filters', filtersStr)
                }
              } else {
                newParams.delete('filters')
              }
              const targetURL = `/resources/${kind}?${newParams.toString()}`
              console.debug('[filters] App.onNavigateToResourceKind: navigating to', targetURL)
              navigate({ pathname: `/resources/${kind}`, search: newParams.toString() })
            }}
            onNavigateToResource={navigateFromIssue}
            // Certs has no Radar view, so it can't ride the view-redirect effect
            // above — wire the Certificate Health card straight to the host's
            // fleet Certs page (scoped to this cluster) when claimed. `assign`
            // (not replace): the user is navigating forward from a card, so this
            // belongs in history. Omitted → the card falls back to Radar's own
            // TLS-secrets resource list.
            onNavigateToCerts={
              takeover.certs ? () => goHost(takeover.certs!) : undefined
            }
          />
        )}

        {/* Topology view */}
        {mainView === 'topology' && (
          <>
            {topology?.requiresNamespaceFilter && namespaces.length === 0 ? (
              /* Large cluster: prompt user to select a namespace */
              <div className="flex-1 flex items-center justify-center">
                <div className="max-w-md w-full mx-4 text-center">
                  <div className="bg-theme-surface border border-theme-border rounded-xl shadow-lg p-6">
                    <div className="w-12 h-12 mx-auto mb-4 rounded-full bg-blue-500/10 flex items-center justify-center">
                      <Network className="w-6 h-6 text-blue-400" />
                    </div>
                    <h2 className="text-lg font-semibold text-theme-text-primary mb-2">
                      Large Cluster Detected
                    </h2>
                    <p className="text-sm text-theme-text-secondary mb-5">
                      This cluster has too many resources to render the full topology.
                      Select a namespace to explore.
                    </p>
                    <div className="relative">
                      <LargeClusterNamespacePicker
                        namespaces={availableNamespaces}
                        onSelect={(ns) => {
                          setNamespaces([ns])
                          setActiveNamespace.mutate({ namespaces: [ns] })
                          // Large clusters need server-side filtering — reconnect SSE with namespace
                          setForceNamespaceFilter([ns])
                        }}
                      />
                    </div>
                  </div>
                </div>
              </div>
            ) : (
              <>
                {/* Filter sidebar */}
                <TopologyFilterSidebar
                  nodes={topology?.nodes || []}
                  visibleKinds={visibleKinds}
                  onToggleKind={handleToggleKind}
                  onShowAll={handleShowAllKinds}
                  onHideAll={handleHideAllKinds}
                  collapsed={filterSidebarCollapsed}
                  onToggleCollapse={() => setFilterSidebarCollapsed(prev => !prev)}
                  hiddenKinds={topology?.hiddenKinds}
                  onEnableHiddenKind={(kind) => {
                    setVisibleKinds(prev => new Set(prev).add(kind as NodeKind))
                    console.log(`[topology] User requested to show hidden kind: ${kind}`)
                  }}
                />

                <div className="flex-1 relative">
                  <TopologyGraph
                    topology={topologyWithAudit}
                    viewMode={topologyMode}
                    groupingMode={effectiveGroupingMode}
                    hideGroupHeader={hideGroupHeader}
                    onNodeClick={handleNodeClick}
                    selectedNodeId={selectedNodeId}
                    paused={topologyPaused}
                    onTogglePause={handleTogglePause}
                    onMaximizeNamespace={(ns) => setActiveNamespace.mutate({ namespaces: [ns] })}
                    namespaceBreadcrumb={namespaces.length === 1 ? namespaces[0] : undefined}
                    onClearNamespace={namespaces.length >= 1 ? () => setActiveNamespace.mutate({ namespaces: [] }) : undefined}
                    namespacesKey={namespaces.join(',')}
                    focusNodeId={topologyFocus?.id}
                    focusNonce={topologyFocus?.nonce}
                  />

                  {/* Topology node search overlay - top left */}
                  <TopologySearch
                    nodes={filteredTopology?.nodes ?? []}
                    allNodes={topology?.nodes}
                    viewModeLabel={topologyMode === 'fleet' ? 'Fleet' : topologyMode === 'traffic' ? 'Network Flow' : 'Resources'}
                    onNodeSelect={handleNodeClick}
                    onZoomToNode={(id) => setTopologyFocus((prev) => ({ id, nonce: (prev?.nonce ?? 0) + 1 }))}
                    // Stack below the namespace breadcrumb (shown only for a single
                    // namespace) so the two don't overlap in the top-left corner.
                    triggerClassName={namespaces.length === 1 ? 'top-12 left-3' : 'top-3 left-3'}
                  />

                  {/* Topology controls overlay - top right */}
                  <TopologyControls
                    viewMode={topologyMode}
                    onViewModeChange={(mode) => {
                      setTopologyMode(mode)
                      // Fleet mode: namespace grouping for structure, but expanded (not collapsed chips)
                      if (mode === 'fleet') setGroupingMode('namespace')
                    }}
                    groupingMode={groupingMode}
                    onGroupingModeChange={setGroupingMode}
                    showNoGrouping={hasNamespaceFilter}
                    showPolicyEffect={showPolicyEffect}
                    onShowPolicyEffectChange={setShowPolicyEffect}
                    showFleetMode={displayedTopology?.nodes?.some(n => FLEET_MODE_KINDS.has(n.kind as NodeKind)) ?? false}
                    onNavigateToTraffic={() => setMainView('traffic')}
                    leadingSlot={
                      <FreshnessControl
                        mode="auto"
                        paused={topologyPaused}
                        connectionState={connection.state}
                      />
                    }
                  />
                </div>
              </>
            )}
          </>
        )}

        {/* Resources view */}
        {mainView === 'resources' && (
          <ResourcesView
            namespaces={namespaces}
            selectedResource={routeSelectedResource}
            onResourceClick={(res) => res ? navigateToResource(res) : setSelectedResource(null)}
            onResourceClickYaml={(res) => navigateToResource(res, 'yaml')}
            onKindChange={() => setSelectedResource(null)}
            onClearNamespaces={clearAllNamespaces}
          />
        )}

        {/* Timeline view */}
        {mainView === 'timeline' && (
          <TimelineView
            namespaces={namespaces}
            onResourceClick={(resource) => {
              navigate(relatedResourcePath(resource))
            }}
            initialViewMode={(searchParams.get('view') as 'list' | 'swimlane') || undefined}
            initialFilter={(searchParams.get('filter') as 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy') || undefined}
            initialTimeRange={(searchParams.get('time') as '5m' | '30m' | '1h' | '6h' | '24h' | 'all') || undefined}
            requiresNamespaceFilter={topology?.requiresNamespaceFilter && namespaces.length === 0}
            availableNamespaces={availableNamespaces}
            onNamespaceSelect={(ns) => {
              setNamespaces([ns])
              setActiveNamespace.mutate({ namespaces: [ns] })
            }}
          />
        )}

        {mainView === 'helm' && (
          <HelmView
            namespaces={namespaces}
            selectedRelease={selectedHelmRelease}
            onReleaseClick={navigateToHelmRelease}
          />
        )}

        {mainView === 'helmCompare' && (
          <HelmCompareRoute />
        )}

        {/* GitOps view (inline only when the host hasn't taken it over — see
            the takeover splash below). */}
        {mainView === 'gitops' && !isViewTakenOver('gitops') && (
          <GitOpsView
            namespaces={namespaces}
            onOpenResource={(resource) => {
              // Route through navigateToResource so the peek records the page it
              // opened on — that's what lets Back off the GitOps detail page close
              // the drawer instead of orphaning it on the list.
              navigateToResource(resource)
            }}
            onClearNamespaces={clearAllNamespaces}
          />
        )}

        {/* Applications view — deployable software grouped by app/release evidence */}
        {mainView === 'applications' && (
          <ApplicationsView
            namespaces={namespaces}
            onOpenResource={(resource) => {
              // The peek and the inline WorkloadView are mutually exclusive: drop
              // the inline workload selection so the app graph (not a second
              // detail panel) sits behind the peek. Search-only change keeps the
              // pathname — and thus the peek's owner-path — intact.
              const params = new URLSearchParams(window.location.search)
              if (params.has('workload') || params.has('tab') || params.has('run')) {
                params.delete('workload')
                params.delete('tab')
                params.delete('run')
                navigate({ pathname: window.location.pathname, search: params.toString() }, { replace: true })
              }
              navigateToResource(resource)
            }}
          />
        )}

        {/* Traffic view */}
        {mainView === 'traffic' && (
          <TrafficView namespaces={namespaces} />
        )}

        {/* Cost detail view */}
        {mainView === 'cost' && (
          <CostView onBack={() => setMainView('home')} />
        )}

        {/* Takeover splash. When the host claims the current view via
            fleetTakeoverHref, the redirect effect above is mid-flight — render a
            brief splash instead of the inline view (which would flash + fire its
            own fetches) while the cross-document nav lands. Covers checks /
            issues / gitops with one block since only one view is active. */}
        {viewTakeoverHref && (
          <PaneLoader
            label="Opening…"
            className="flex-1 min-h-0 bg-theme-base"
          />
        )}

        {/* Best practices detail view (inline only when the host hasn't taken
            Checks over — standalone OSS, or Cloud without a checks takeover). */}
        {mainView === 'checks' && !isViewTakenOver('checks') && (
          <AuditView
            namespaces={namespaces}
            onNavigateToResource={navigateToResourceList}
          />
        )}

        {/* Issues — per-cluster live triage queue (hidden route: not yet in the
            nav `views` list; reachable at /issues). Same shared <IssuesView> the
            Hub fleet uses; a GitOps reconciler subject routes to its detail page,
            other resources open the standard resource view. Inline only when the
            host hasn't taken it over. */}
        {mainView === 'issues' && !isViewTakenOver('issues') && (
          <IssuesPane
            namespaces={namespaces}
            onNavigateToResource={navigateFromIssue}
          />
        )}

        {/* Workload full view — the standalone fullscreen route for non-list
            surfaces and deep links. Expand-from-drawer is the ?full=1 overlay on
            /resources instead, so it never routes here. */}
        {mainView === 'workload' && (
          <WorkloadViewRoute
            onNavigateToResource={(resource) => {
              navigate(relatedResourcePath(resource))
            }}
          />
        )}

        {/* Compare two resources of the same kind side-by-side */}
        {mainView === 'compare' && <CompareViewRoute />}

        </ErrorBoundary>
      </div>}
      </div>{/* /body frame */}

      {/* Resource detail drawer — stays mounted, expands to full-screen WorkloadView.
          Gated on contentReady so it never renders over the connecting/switching
          splash (which would push the centered logo off-center). */}
      {contentReady && resourceDrawer.shouldRender && drawerResource && (
        <ResourceDetailDrawer
          resource={drawerResource}
          initialTab={drawerInitialTab}
          // No Radar header in chromeless embeds (Radar Hub) — anchor the drawer
          // to the top of the content area instead of leaving a 49px gap.
          headerHeight={chromeless ? 0 : undefined}
          rightInset={contentGutter}
          isOpen={resourceDrawer.isOpen}
          expanded={drawerExpandedProp}
          onClose={closeDrawer}
          onNavigate={(res) => navigateToResource(res)}
          canCollapseToDrawer={!isMobile}
          onExpand={(_res, opts) => {
            // Grow the peek into a fullscreen overlay (?full=1, pushed so Back
            // collapses) over whatever view is underneath — list, topology graph,
            // GitOps, Applications — which stays mounted. Carry the YAML tab when
            // expanding from the drawer's YAML view so the editor (and its
            // session-persisted draft) is right there, not behind the Overview tab.
            const p = new URLSearchParams(searchParams)
            p.set('full', '1')
            if (opts?.yaml) p.set('tab', 'yaml')
            setSearchParams(p)
          }}
          // On mobile there's no drawer to collapse back to, so the collapse/back
          // control closes the resource (returns to the list) instead.
          onCollapse={isMobile ? closeDrawer : handleCollapseFromExpanded}
          onNavigateToResource={(resource) => {
            // Drill into a related resource while expanded: stay in the over-list
            // overlay for the new resource (pushed, so Back walks resource→resource
            // still expanded). The backdrop list follows to the new kind.
            const pluralKind = kindToPlural(resource.kind)
            setSelectedResource({ ...resource, kind: pluralKind })
            const p = new URLSearchParams()
            const ns = searchParams.get('namespaces')
            if (ns) p.set('namespaces', ns)
            p.set('resource', resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name)
            if (resource.group) p.set('apiGroup', resource.group)
            p.set('full', '1')
            navigate({ pathname: `/resources/${pluralKind}`, search: p.toString() })
          }}
        />
      )}

      {/* Helm release drawer — same contentReady gate as the resource drawer. */}
      {contentReady && helmDrawer.shouldRender && drawerHelmRelease && (
        <HelmReleaseDrawer
          release={drawerHelmRelease}
          isOpen={helmDrawer.isOpen}
          rightInset={contentGutter}
          onClose={() => {
            setSelectedHelmRelease(null)
            const params = new URLSearchParams(window.location.search)
            params.delete('release')
            params.delete('releaseStorage')
            setSearchParams(params, { replace: true })
          }}
          onNavigateToResource={(resource) => {
            setSelectedHelmRelease(null)
            const newParams = new URLSearchParams()
            const globalNamespaces = searchParams.get('namespaces')
            if (globalNamespaces) newParams.set('namespaces', globalNamespaces)
            if (resource.group) newParams.set('apiGroup', resource.group)
            navigate({ pathname: `/resources/${resource.kind}`, search: newParams.toString() })
            setSelectedResource(resource)
          }}
        />
      )}

      {/* AI investigation panel — an absolute slot in this column (the body frame),
          below the header and right of the nav rail, sharing the frame with the
          drawers above. Docked = right slot (pushes content via contentGutter);
          maximized = fills the frame. */}
      {diagnoseOpen && <DiagnoseSurface topInset={chromeless ? 0 : APP_HEADER_HEIGHT} />}

      {/* Port Forward floating panel (indicator lives in header) */}
      <PortForwardPanel />

      {/* Update notification — hidden in embedded mode (OSS download nudge). */}
      {!navCustomization.embedded && <UpdateNotification />}

      {/* Bottom Dock for Terminal/Logs */}
      <BottomDock />

      {/* Spacer for dock */}
      <DockSpacer />

      {/* Floating action buttons — embedded only, and not in chromeless (the
          host owns help/diagnostics chrome). Standalone moved help + bug to
          visible top-bar icons (the rail owns chrome). */}
      {!showNavRail && !chromeless && (
        <FloatingButtons showHelp={showHelp} showCommandPalette={showCommandPalette} showDiagnostics={showDiagnostics} onHelp={() => setShowHelp(true)} onBugReport={() => setShowDiagnostics(true)} />
      )}

      {/* Keyboard shortcut help overlay */}
      {helpOverlay.shouldRender && <ShortcutHelpOverlay isOpen={helpOverlay.isOpen} onClose={() => setShowHelp(false)} currentView={mainView} />}

      {/* Command palette */}
      {commandPaletteAnim.shouldRender && (
        <CommandPalette
          isOpen={commandPaletteAnim.isOpen}
          onClose={() => setShowCommandPalette(false)}
          onNavigateView={(view) => setMainView(view)}
          onNavigateKind={(kind, group) => {
            const params = new URLSearchParams(searchParams)
            params.delete('kind')
            if (group) params.set('apiGroup', group)
            else params.delete('apiGroup')
            params.delete('resource')
            params.delete('full')
            params.delete('tab')
            navigate({ pathname: `/resources/${kind}`, search: params.toString() })
            // Focus the table search after navigation — the user came from ⌘K
            // (keyboard flow) and expects to type a resource name immediately.
            setTimeout(() => {
              (document.querySelector('input[placeholder="Search... (press /)"]') as HTMLInputElement)?.focus()
            }, 100)
          }}
          onSwitchContext={(name) => switchContext.mutate(
            { name },
            // Namespace filter from the previous context may not exist in the
            // new one — clear it so resource lists don't silently go empty.
            // The server clears all per-user picks on context switch already;
            // local state mirrors that via the namespace-scope effect.
            { onSettled: () => setNamespaces([]) },
          )}
          onSetNamespaces={(ns) => {
            if (namespaceScope?.cacheScoped && ns.length !== 1) return
            setNamespaces(ns)
            setActiveNamespace.mutate({ namespaces: ns })
          }}
          onToggleTheme={toggleTheme}
          onShowDiagnostics={() => setShowDiagnostics(true)}
        />
      )}

      {/* Diagnostics overlay */}
      {diagnosticsOverlay.shouldRender && <DiagnosticsOverlay isOpen={diagnosticsOverlay.isOpen} onClose={() => setShowDiagnostics(false)} />}

      {/* Settings dialog */}
      <SettingsDialog
        open={showSettings}
        onClose={() => setShowSettings(false)}
        onShowMyPermissions={() => {
          setShowSettings(false)
          setShowMyPermissions(true)
        }}
      />
      <MyPermissionsDialog open={showMyPermissions} onClose={() => setShowMyPermissions(false)} />

      {/* Debug overlay — dev mode, standalone only. Embedded hosts (Radar Hub)
          own their own dev tooling; ours would collide with theirs bottom-right. */}
      {import.meta.env.DEV && showNavRail && <DebugOverlay />}
      </div>
    </div>
    </PortForwardProvider>
  )
}

// Spacer component that adds padding when dock is open
function DockSpacer() {
  const { tabs, isResizing } = useDock()
  const dockInset = useDockReservedHeight()
  const location = useLocation()
  // Traffic view manages its own layout — spacer would break its flex sizing
  if (tabs.length === 0 || location.pathname === '/traffic') return null
  return (
    <div
      className="shrink-0"
      style={{
        height: dockInset,
        transition: isResizing ? 'none' : `height ${DURATION_DOCK}ms cubic-bezier(0.4, 0, 0.2, 1)`,
      }}
    />
  )
}

// Floating action buttons that position themselves above the dock
function FloatingButtons({ showHelp, showCommandPalette, showDiagnostics, onHelp, onBugReport }: { showHelp: boolean; showCommandPalette: boolean; showDiagnostics: boolean; onHelp: () => void; onBugReport: () => void }) {
  const { tabs } = useDock()
  if (showHelp || showCommandPalette || showDiagnostics) return null
  // When dock tab bar is visible (36px), shift the buttons up above it
  const bottom = tabs.length > 0 ? 'bottom-10' : 'bottom-2'
  const btnClass = 'w-7 h-7 flex items-center justify-center rounded-full bg-theme-elevated/80 hover:bg-theme-hover border border-theme-border-light text-theme-text-tertiary hover:text-theme-text-secondary text-xs font-medium shadow-sm backdrop-blur-sm transition-all'
  return (
    <div className={`fixed ${bottom} right-4 z-40 flex items-center gap-1.5`}>
      <Tooltip content="Report bug / Diagnostics" position="top">
        <button onClick={onBugReport} aria-label="Open diagnostics" className={btnClass}>
          <Bug className="w-3.5 h-3.5" />
        </button>
      </Tooltip>
      <Tooltip content="Keyboard shortcuts (?)" position="top">
        <button onClick={onHelp} aria-label="Show keyboard shortcuts" className={btnClass}>
          ?
        </button>
      </Tooltip>
    </div>
  )
}

// Main App component wrapped with providers
function App({ manageDocumentTitle = false, documentTitleSuffix, onClusterLoadStateChange }: AppProps) {
  return (
    <ConnectionProvider>
      <CapabilitiesProvider>
        <ContextSwitchProvider>
          <DockProvider>
            <KeyboardShortcutProvider>
              <AppInner
                manageDocumentTitle={manageDocumentTitle}
                documentTitleSuffix={documentTitleSuffix}
                onClusterLoadStateChange={onClusterLoadStateChange}
              />
            </KeyboardShortcutProvider>
          </DockProvider>
        </ContextSwitchProvider>
      </CapabilitiesProvider>
    </ConnectionProvider>
  )
}

// Header brand: emerald-square radar icon + stacked "Radar" / "by Skyhook"
// wordmark. Shares its visual shape with the radar-hub-web shell so the
// standalone OSS app and the embedded Cloud experience read as the same
// product, and is narrow enough to leave room for the cluster switcher
// and nav block on standard laptop viewports.
function Logo() {
  return (
    <div className="flex items-center gap-2.5">
      <div className="relative w-7 h-7 rounded-lg overflow-hidden flex-shrink-0 bg-emerald-500/10 border border-emerald-500/20">
        <img
          src="/images/radar/radar-icon.svg"
          alt=""
          aria-hidden
          className="w-full h-full p-0.5"
          // Fail loud on a missing/blocked asset rather than rendering an
          // empty emerald square next to the wordmark — the latter reads
          // as broken chrome with no diagnostics. Most likely cause is a
          // build/deploy path mismatch.
          onError={(e) =>
            console.error('Radar logo asset failed to load:', (e.currentTarget as HTMLImageElement).src)
          }
        />
      </div>
      <div className="flex flex-col leading-none">
        <span className="font-semibold text-[15px] tracking-tight text-theme-text-primary">Radar</span>
        <span className="text-[9px] mt-0.5 tracking-wide uppercase text-theme-text-tertiary">by Skyhook</span>
      </div>
    </div>
  )
}

// GitHub star button with live star count + programmatic starring via gh CLI
// Shows a callout popover when the backend says shouldPrompt is true (synced with CLI state)
function GitHubStarButton() {
  const [starCount, setStarCount] = useState<number | null>(null)
  const [starred, setStarred] = useState(false)
  const [ghAvailable, setGhAvailable] = useState(false)
  const [showCallout, setShowCallout] = useState(false)
  const calloutRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLAnchorElement>(null)

  useEffect(() => {
    // Fetch star count from GitHub public API
    fetch('https://api.github.com/repos/skyhook-io/radar')
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (data && typeof data.stargazers_count === 'number') setStarCount(data.stargazers_count) })
      .catch(() => {})

    // Check if user already starred (via backend/gh CLI) and whether to show prompt
    fetch(apiUrl('/github/starred'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (data) {
          setStarred(data.starred)
          setGhAvailable(data.ghAvailable)
          if (data.shouldPrompt && !data.starred) {
            // Delay the callout, then re-check in case CLI prompted during the wait
            setTimeout(() => {
              fetch(apiUrl('/github/starred'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
                .then(res => res.ok ? res.json() : null)
                .then(fresh => {
                  if (fresh?.shouldPrompt && !fresh.starred) {
                    setShowCallout(true)
                  }
                })
                .catch(() => {})
            }, 3000)
          }
        }
      })
      .catch(() => {})
  }, [])

  const handleDismiss = useCallback(() => {
    setShowCallout(false)
    fetch(apiUrl('/github/dismiss'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() }).catch(() => {})
  }, [])

  // Close callout when clicking outside
  useEffect(() => {
    if (!showCallout) return
    const handleClickOutside = (e: MouseEvent) => {
      if (
        calloutRef.current && !calloutRef.current.contains(e.target as Node) &&
        buttonRef.current && !buttonRef.current.contains(e.target as Node)
      ) {
        handleDismiss()
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [showCallout, handleDismiss])

  const handleClick = (e: React.MouseEvent) => {
    if (starred) return // Already starred, just let the link open GitHub

    if (ghAvailable) {
      // Star via backend gh CLI
      e.preventDefault()
      fetch(apiUrl('/github/star'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() })
        .then(res => res.ok ? res.json() : null)
        .then(data => {
          if (data?.starred) {
            setStarred(true)
            setShowCallout(false)
            setStarCount(prev => prev !== null ? prev + 1 : prev)
          }
        })
        .catch(() => {
          // Fallback: open GitHub in browser
          openExternal('https://github.com/skyhook-io/radar')
        })
    } else {
      // No gh CLI — link opens GitHub; dismiss the callout
      setShowCallout(false)
      fetch(apiUrl('/github/dismiss'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() }).catch(() => {})
    }
  }

  return (
    <div className="relative">
      <a
        ref={buttonRef}
        href="https://github.com/skyhook-io/radar"
        target="_blank"
        rel="noopener noreferrer"
        onClick={handleClick}
        aria-label={starred ? 'Open Radar on GitHub' : 'Star Radar on GitHub'}
        className="flex items-center gap-1.5 h-7 px-2 rounded-md transition-colors bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary"
      >
        <svg className="w-4 h-4" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
        <Star className={`w-3 h-3 hidden xl:block ${starred ? 'text-yellow-500 fill-current' : ''}`} />
        {starCount !== null && (
          <>
            <span className="w-px h-3 bg-theme-border hidden xl:block" />
            <span className="text-xs tabular-nums hidden xl:inline">{starCount.toLocaleString()}</span>
          </>
        )}
      </a>

      {/* Callout popover — synced with CLI star.json state */}
      {showCallout && (
        <div
          ref={calloutRef}
          className="absolute top-full right-0 mt-2 w-64 p-3 bg-theme-surface border border-theme-border rounded-lg shadow-lg z-50"
        >
          {/* Arrow */}
          <div className="absolute -top-1.5 right-4 w-3 h-3 bg-theme-surface border-l border-t border-theme-border rotate-45" />
          <p className="text-sm text-theme-text-primary mb-2">
            Enjoying Radar? Show your support with a star!
          </p>
          <div className="flex items-center gap-2">
            <a
              href="https://github.com/skyhook-io/radar"
              target="_blank"
              rel="noopener noreferrer"
              onClick={handleClick}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-yellow-500/15 text-yellow-500 hover:bg-yellow-500/25 rounded-md transition-colors"
            >
              <Star className="w-3.5 h-3.5" />
              Star on GitHub
            </a>
            <button
              onClick={handleDismiss}
              className="px-2 py-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            >
              Maybe later
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// Theme toggle button component
function ThemeToggle() {
  const { theme, toggleTheme } = useTheme()

  return (
    <Tooltip content={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}>
    <button
      onClick={toggleTheme}
      aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
      className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
    >
      {theme === 'dark' ? (
        <Sun className="w-4 h-4" />
      ) : (
        <Moon className="w-4 h-4" />
      )}
    </button>
    </Tooltip>
  )
}

export default App
