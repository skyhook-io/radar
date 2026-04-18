// @skyhook-io/radar-app — the Radar frontend as a reusable React component.
//
// Source-only distribution (same pattern as @skyhook-io/k8s-ui): consumers
// are Vite/Next.js projects that transpile TS/TSX and resolve aliases at
// build time. No bundle step here.
//
// The RadarApp component lives in web/src/RadarApp.tsx so Radar's own
// binary (web/src/main.tsx) and external consumers share exactly one
// implementation. Do not re-implement here.
export { RadarApp, type RadarAppProps } from '../../../web/src/RadarApp';
export {
  setApiBase,
  setBasename,
  setAuthHeadersProvider,
  setCredentialsMode,
  getApiBase,
  getBasename,
} from '../../../web/src/api/config';
export type { NavCustomization } from '../../../web/src/context/NavCustomization';
