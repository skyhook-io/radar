// Top-level React component for Radar's web UI.
//
// This is the entrypoint the `@skyhook-io/radar-app` library publishes.
// It's also rendered by Radar's own binary via web/src/main.tsx — so
// Radar standalone and Radar-embedded-in-another-app (e.g., Radar Hub)
// share exactly one code path.
//
// Config model:
//   - apiBase  — base URL for REST/SSE/WS. Default '/api' (same-origin,
//                Radar's own binary). Hub passes a cluster-scoped URL
//                like '/c/abc/api' or 'https://api.radar.skyhook.io/c/abc/api'.
//   - basename — router basename. Default '' (mounted at root). Hub
//                passes '/c/abc' when embedding, so Radar's internal
//                paths (/topology, /resources/...) resolve correctly.
//
// Both are applied before any children render so downstream code that
// reads config synchronously (e.g. URL construction inside fetchJSON)
// sees the host's values.
import React from 'react';
import { BrowserRouter, MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider, MutationCache, QueryCache } from '@tanstack/react-query';

import App from './App';
import { ThemeProvider } from './context/ThemeContext';
import { ToastProvider, showApiError, showApiSuccess } from './components/ui/Toast';
import { setApiBase, setBasename } from './api/config';
import { NavCustomizationProvider } from './context/NavCustomization';
import type { NavCustomization } from './context/NavCustomization';

// Declare the shape of mutation meta here — inlined rather than in a
// separate side-effect-only module so consumers that tree-shake aggressively
// (package.json sets sideEffects: ["*.css"]) can't drop the augmentation.
// Any consumer that imports RadarApp will pull in this declaration.
declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      errorMessage?: string;
      successMessage?: string;
      successDetail?: string;
    };
  }
}

export interface RadarAppProps {
  /** API base URL (REST + SSE + WS). Defaults to '/api' (same-origin). */
  apiBase?: string;
  /** React Router basename. Defaults to '' (mounted at root). */
  basename?: string;
  /**
   * Router strategy:
   *   - 'browser' (default): BrowserRouter — URL bar reflects all navigation.
   *     Use when Radar owns routing. Library consumers should mount RadarApp
   *     above their own router (or replace the host's router with this one)
   *     and pass `basename` — React Router forbids nesting routers.
   *   - 'memory': MemoryRouter — URL bar does NOT change as Radar navigates.
   *     Escape hatch for tests and for host apps that can't restructure
   *     around a single top-level BrowserRouter.
   */
  router?: 'browser' | 'memory';
  /**
   * Optional QueryClient override. When consuming Radar inside another app
   * that already has a QueryClientProvider higher in the tree, you may
   * prefer to share its client rather than nest two providers.
   */
  queryClient?: QueryClient;
  /**
   * Slot-based customization of Radar's top nav. Use to inject host-app
   * brand, replace the kubeconfig context picker with a product-level
   * cluster switcher, and append items to the right action bar.
   * See ./context/NavCustomization for the slot shape.
   */
  navSlots?: NavCustomization;
}

// Default QueryClient with the same shape Radar's standalone binary uses.
// Extracted so both standalone + library consumers get identical
// toast-on-error / toast-on-success behavior.
function makeDefaultQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        refetchOnWindowFocus: false,
        retry: 1,
      },
    },
    mutationCache: new MutationCache({
      onError: (error, _variables, _context, mutation) => {
        const message = mutation.options.meta?.errorMessage;
        if (message) showApiError(message, (error as Error).message);
      },
      onSuccess: (_data, _variables, _context, mutation) => {
        const message = mutation.options.meta?.successMessage;
        if (message) showApiSuccess(message, mutation.options.meta?.successDetail);
      },
    }),
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.state.data !== undefined) {
          console.warn('[Background sync failed]', query.queryKey, (error as Error).message);
        }
      },
    }),
  });
}

export function RadarApp({
  apiBase,
  basename,
  router = 'browser',
  queryClient,
  navSlots,
}: RadarAppProps): React.ReactElement {
  // Apply runtime config during render so module-level singletons are set
  // before children construct URLs. getApiBase() / getAuthHeaders() /
  // getCredentialsMode() are read on every fetch, SSE connect, and WS
  // connect — so later calls to setApiBase() also take effect, but there's
  // no subscription so React won't re-render on change. Host apps should
  // pass props rather than mutate via setters after mount.
  if (apiBase !== undefined) setApiBase(apiBase);
  if (basename !== undefined) setBasename(basename);

  // Memo so we don't recreate the QueryClient on every render when the
  // consumer didn't pass one.
  const client = React.useMemo(() => queryClient ?? makeDefaultQueryClient(), [queryClient]);

  const inner = (
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <ToastProvider>
          <NavCustomizationProvider value={navSlots}>
            <App />
          </NavCustomizationProvider>
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );

  if (router === 'memory') {
    return <MemoryRouter initialEntries={['/']}>{inner}</MemoryRouter>;
  }

  return <BrowserRouter basename={basename || undefined}>{inner}</BrowserRouter>;
}

export default RadarApp;
