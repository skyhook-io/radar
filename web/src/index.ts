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
