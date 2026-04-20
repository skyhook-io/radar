// Runtime configuration for Radar's frontend.
//
// When Radar runs as its own binary (standalone or in-cluster), the
// frontend is same-origin with the Go API server and `apiBase` is `/api`.
// When Radar's web is embedded as `@skyhook-io/radar-app` in another
// frontend (e.g. Radar Hub), the host app calls `setApiBase()` to point
// at a cluster-scoped proxy URL like `/c/{cluster_id}/api` before mounting.
//
// This module is deliberately a small mutable singleton rather than a
// React context so it works in non-React code paths (EventSource,
// WebSocket URL construction) without threading props through every
// call site. Multi-instance hosting (mounting two Radar apps for two
// clusters on the same page) is a V2 concern — it would require
// replacing this with a context.

let apiBase = '/api';
let basename = '';
let authHeadersProvider: () => Record<string, string> = () => ({});
let credentialsMode: RequestCredentials = 'same-origin';

export function getApiBase(): string {
  return apiBase;
}

/**
 * Sets the base URL used for REST API, SSE, and WebSocket requests.
 * Call before mounting the Radar app. Trailing slashes are stripped.
 *
 * Accepts either:
 *   - A relative path: `/api`, `/c/abc/api` (URLs derive scheme + host from window.location)
 *   - An absolute URL: `https://api.radarhq.io/c/abc/api` (URLs use that origin)
 */
export function setApiBase(url: string): void {
  apiBase = url.replace(/\/+$/, '');
}

/**
 * Returns the router basename (e.g. `/c/abc` when mounted in Radar Hub),
 * or `''` for standalone use.
 */
export function getBasename(): string {
  return basename;
}

/**
 * Sets the router basename. React Router strips this from location.pathname
 * inside the app, so all internal paths remain relative to `/`. Used by
 * full-page navigation helpers (auth redirects, etc.) where we still need
 * to construct a URL relative to the host app's origin.
 */
export function setBasename(value: string): void {
  basename = value.replace(/\/+$/, '');
}

/**
 * Builds an absolute path under the configured basename — used for full
 * window.location navigation (OIDC login redirect, return-path tracking)
 * where React Router doesn't apply.
 */
export function routePath(path: string): string {
  const prefix = basename;
  if (!prefix) return path;
  if (path.startsWith(prefix + '/') || path === prefix) return path;
  return prefix + path;
}

/**
 * Builds a WebSocket URL for a given API path.
 *
 * When apiBase is relative, uses window.location.host + apiBase + path.
 * When apiBase is absolute, swaps http(s) → ws(s) and appends path.
 */
export function getWsUrl(path: string): string {
  const base = apiBase;
  if (base.startsWith('http://') || base.startsWith('https://')) {
    return base.replace(/^http/, 'ws') + path;
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${window.location.host}${base}${path}`;
}

/** Convenience: `${apiBase}${path}` for fetch call sites. */
export function apiUrl(path: string): string {
  return getApiBase() + path;
}

/**
 * Register a function that returns HTTP headers to attach to every Radar
 * API request. Used by library consumers (e.g. Radar Hub) to inject
 * Authorization bearer tokens or session tokens. The provider is called
 * on each request so it can read fresh state.
 *
 * Standalone Radar leaves this empty — auth is cookie-based in OIDC or
 * proxy mode, no explicit headers needed.
 */
export function setAuthHeadersProvider(fn: () => Record<string, string>): void {
  authHeadersProvider = fn;
}

/** Internal: returns the registered auth headers for fetch() calls. */
export function getAuthHeaders(): Record<string, string> {
  return authHeadersProvider();
}

/**
 * Sets the `credentials` mode for all Radar fetches. Default 'same-origin'
 * (standalone Radar binary). Library consumers at a different origin should
 * set 'include' so browser cookies + CORS preflight cooperate.
 */
export function setCredentialsMode(mode: RequestCredentials): void {
  credentialsMode = mode;
}

export function getCredentialsMode(): RequestCredentials {
  return credentialsMode;
}
