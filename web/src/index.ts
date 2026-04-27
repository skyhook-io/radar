// @skyhook-io/radar-app — Radar's full web UI as a reusable React component.
//
// Source-only package (main points at .ts, no dist/). Consumers need a
// bundler that transpiles TSX and resolves workspace-style peer deps. The
// same source is consumed by Radar's binary via main.tsx.
export { RadarApp, type RadarAppProps } from './RadarApp';
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
export type { NavCustomization } from './context/NavCustomization';
export { ShortcutHelpOverlay } from './components/ui/ShortcutHelpOverlay';

// Shared cluster-switcher primitive — re-exported from @skyhook-io/k8s-ui so
// embedders (Radar Hub) can render a switcher visually identical to OSS Radar's
// kubeconfig ContextSwitcher without taking a direct dep on k8s-ui internals.
export { ClusterSwitcher } from '@skyhook-io/k8s-ui';
export type { ClusterSwitcherProps, ClusterSwitcherItem } from '@skyhook-io/k8s-ui';
