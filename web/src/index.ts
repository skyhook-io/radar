// @skyhook-io/radar-app — Radar's full web UI as a reusable React component.
//
// Source-only package (main points at .ts, no dist/). Consumers need a
// bundler that transpiles TSX and resolves workspace-style peer deps. The
// same source is consumed by Radar's binary via main.tsx.
export { RadarApp, type RadarAppProps } from './RadarApp';
export type { ClusterLoadState } from './types/clusterLoadState';
export {
  setApiBase,
  setBasename,
  setAuthHeadersProvider,
  setCredentialsMode,
  getApiBase,
  getBasename,
  getAuthHeaders,
  getCredentialsMode,
} from './api/config';
export type { NavCustomization, FleetTakeoverTarget } from './context/NavCustomization';
// Timeline data-source selection — lets an embedder back the timeline with a
// retained-history endpoint instead of Radar's local event store. Additive;
// absent = local (standalone behavior).
export type {
  TimelineSourceConfig,
  TimelineSourceCapabilities,
  TimelineOverviewBucket,
  TimelineCoverageSpan,
  TimelineOverviewResult,
} from './api/timelineSource';
export type {
  RenderDiagnoseAction,
  DiagnoseConsentCopy,
} from './context/DiagnoseCustomization';

// Standalone AI-diagnose surface — mount the investigation panel outside a
// full <RadarApp>. No router dependency, no client-side cluster state: the
// backend set via setApiBase() picks the cluster, so hosts remount
// <DiagnoseProvider key={cluster}> to switch. Mount order: ThemeProvider >
// DiagnoseCustomizationProvider > DiagnoseProvider > DiagnoseSurface, under a
// @tanstack/react-query QueryClientProvider.
export {
  DiagnoseProvider,
  useDiagnose,
  useDiagnoseLayout,
} from './components/diagnose/DiagnoseContext';
export type { Target as DiagnoseTarget } from './components/diagnose/DiagnoseContext';
export { DiagnoseSurface } from './components/diagnose/DiagnoseSurface';
export { DiagnoseCustomizationProvider } from './context/DiagnoseCustomization';
// The panel reads Radar's ThemeContext (throws unprovided). It follows
// localStorage['radar-theme'], so host and panel flip together.
export { ThemeProvider } from './context/ThemeContext';
export { ShortcutHelpOverlay } from './components/ui/ShortcutHelpOverlay';

// Shared cluster-switcher primitive — re-exported from @skyhook-io/k8s-ui so
// embedders (Radar Hub) can render a switcher visually identical to OSS Radar's
// kubeconfig ContextSwitcher without taking a direct dep on k8s-ui internals.
export { ClusterSwitcher } from '@skyhook-io/k8s-ui';
export type { ClusterSwitcherProps, ClusterSwitcherItem } from '@skyhook-io/k8s-ui';

// Shared namespace-scope picker primitive — re-exported so embedders (Radar
// Hub) can render a namespace filter visually identical to OSS Radar's, driving
// their own per-cluster scope (Hub via ?namespaces= on the embedded RadarApp).
export { NamespacePicker } from '@skyhook-io/k8s-ui';
export type {
  NamespacePickerProps,
  NamespacePickerHandle,
  NamespaceScopeView,
} from '@skyhook-io/k8s-ui';

// Shared bordered shell that groups the cluster + namespace segments into one
// pill — so Radar Hub's cluster top bar matches OSS Radar's header exactly.
export { ScopePill } from '@skyhook-io/k8s-ui';
export type { ScopePillProps } from '@skyhook-io/k8s-ui';

// Deep-link builders — so consumers (Radar Hub) construct deep links into a
// cluster view without hand-rolling Radar's internal URL format, which drifts
// silently when Radar re-routes. `resourcePath` opens the detail drawer for any
// kind incl. cluster-scoped; `buildWorkloadPath` is the namespaced-workload
// full-page view. Both return basename-relative paths; embedders prepend their
// cluster prefix (e.g. /c/:id).
export { resourcePath, buildWorkloadPath } from './utils/navigation';
export type { SelectedResource } from '@skyhook-io/k8s-ui/types/core';

// Injectable omnibar — the standalone search/command surface, decoupled from
// Radar's own data hooks so embedders (Radar Hub) can drive it with fleet
// search + their own command items while sharing the exact UX (pills, modifier
// autocomplete, kind-first ranking, match highlighting, keyboard nav, recents).
export { Omnibar } from './components/ui/Omnibar';
export type {
  OmnibarProps,
  OmnibarHandle,
  OmnibarRecent,
  OmnibarSearchResult,
} from './components/ui/Omnibar';
export { bestScore } from './components/ui/command-items';
export type { CommandItem } from './components/ui/command-items';
export type { SearchHit, SearchMatchedField } from './api/client';
