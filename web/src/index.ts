// @skyhook-io/radar-app — Radar's full web UI as a reusable React component.
//
// Source-only distribution (same model as @skyhook-io/k8s-ui): consumers
// are Vite/Next.js projects that transpile TS/TSX and resolve aliases at
// build time. No bundle step.
//
// Radar's own binary entry (main.tsx) renders the same RadarApp component,
// so standalone and embedded modes share exactly one code path.
import './react-query-meta';

export { RadarApp, type RadarAppProps } from './RadarApp';
export {
  setApiBase,
  setBasename,
  setAuthHeadersProvider,
  setCredentialsMode,
  getApiBase,
  getBasename,
} from './api/config';
export type { NavCustomization } from './context/NavCustomization';
