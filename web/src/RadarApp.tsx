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

export interface RadarAppProps {
  /** API base URL (REST + SSE + WS). Defaults to '/api' (same-origin). */
  apiBase?: string;
  /** React Router basename. Defaults to '' (mounted at root). */
  basename?: string;
  /**
   * Router strategy:
   *   - 'browser' (default): BrowserRouter — URL bar reflects all navigation.
   *     Use when Radar owns routing (standalone binary).
   *   - 'memory': MemoryRouter — URL bar does NOT change as Radar navigates.
   *     Use when embedding Radar inside another SPA that already has its own
   *     BrowserRouter; React Router forbids nesting browser routers, so this
   *     keeps Radar's internal nav working without hijacking the parent URL.
   */
  router?: 'browser' | 'memory';
  /**
   * Optional QueryClient override. When consuming Radar inside another app
   * that already has a QueryClientProvider higher in the tree, you may
   * prefer to share its client rather than nest two providers.
   */
  queryClient?: QueryClient;
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
}: RadarAppProps): React.ReactElement {
  // Apply runtime config before any child reads it. These are module-level
  // singletons; setting them before the tree renders is sufficient because
  // children only observe them on first call (not via subscription).
  if (apiBase !== undefined) setApiBase(apiBase);
  if (basename !== undefined) setBasename(basename);

  // Memo so we don't recreate the QueryClient on every render when the
  // consumer didn't pass one.
  const client = React.useMemo(() => queryClient ?? makeDefaultQueryClient(), [queryClient]);

  const inner = (
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <ToastProvider>
          <App />
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );

  // MemoryRouter only kept for tests / explicit opt-in. Nested BrowserRouter
  // error applies to any Router nested in any other Router, including memory
  // — so in practice library consumers embedding Radar should render it at
  // the top of their tree, not inside another Router. See RadarApp's
  // comment at the top for the integration pattern.
  if (router === 'memory') {
    return <MemoryRouter initialEntries={['/']}>{inner}</MemoryRouter>;
  }

  return <BrowserRouter basename={basename || undefined}>{inner}</BrowserRouter>;
}

export default RadarApp;
