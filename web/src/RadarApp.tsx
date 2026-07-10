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
//                like '/c/abc/api' or 'https://api.radarhq.io/c/abc/api'.
//   - basename — router basename. Default '' (mounted at root). Hub
//                passes '/c/abc' when embedding, so Radar's internal
//                paths (/topology, /resources/...) resolve correctly.
//
// Both are applied before any children render so downstream code that
// reads config synchronously (e.g. URL construction inside fetchJSON)
// sees the host's values.
import React from "react";
import { BrowserRouter, MemoryRouter } from "react-router-dom";
import {
  QueryClient,
  QueryClientProvider,
  MutationCache,
  QueryCache,
} from "@tanstack/react-query";

import App from "./App";
import { ThemeProvider } from "./context/ThemeContext";
import {
  ToastProvider,
  showApiError,
  showApiSuccess,
} from "./components/ui/Toast";
import { setApiBase, setBasename } from "./api/config";
import { NavCustomizationProvider } from "./context/NavCustomization";
import { FilterLocationBridge } from "./filter/FilterLocationBridge";
import type { NavCustomization } from "./context/NavCustomization";
import type { ClusterLoadState } from "./types/clusterLoadState";
import { TimelineSourceProvider } from "./context/TimelineSource";
import type { TimelineSourceConfig } from "./api/timelineSource";
import { DiagnoseCustomizationProvider } from "./context/DiagnoseCustomization";
import type { RenderDiagnoseAction } from "./context/DiagnoseCustomization";
import { defaultDiagnoseAction } from "./components/diagnose/LocalDiagnoseAction";
import { DiagnoseProvider } from "./components/diagnose/DiagnoseContext";

// Declare the shape of mutation meta here — inlined rather than in a
// separate side-effect-only module so consumers that tree-shake aggressively
// (package.json sets sideEffects: ["*.css"]) can't drop the augmentation.
// Any consumer that imports RadarApp will pull in this declaration.
declare module "@tanstack/react-query" {
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
  router?: "browser" | "memory";
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
  /**
   * Whether Radar may set the browser tab title (`document.title`) per view.
   * Defaults to OFF: embedders keep title ownership without opting out. The
   * standalone binary opts in (`web/src/main.tsx` renders
   * `<RadarApp manageDocumentTitle />`), and any full-page embed that wants
   * Radar's per-view titles can do the same.
   */
  manageDocumentTitle?: boolean;
  /**
   * Trailing string appended after the per-view label (only when
   * `manageDocumentTitle` is on). It's the *full* suffix including any
   * separator, so a host can rebrand (`' — My Cloud'`) or drop it (`''`).
   * Defaults to `' · Radar'`.
   */
  documentTitleSuffix?: string;
  /**
   * Injects a resource-level "Diagnose" action (e.g. a "Diagnose with AI"
   * button) into every resource detail action bar's right-aligned universal
   * actions. The host returns the node to render given the resource context.
   * Standalone Radar omits this and renders no Diagnose button — OSS stays
   * agent-free. See ./context/DiagnoseCustomization for the render-prop shape.
   */
  renderDiagnoseAction?: RenderDiagnoseAction;
  /**
   * Initial route for `router: 'memory'` (ignored for 'browser'). Lets a host
   * deep-link a specific view (e.g. '/topology') without owning the URL bar —
   * used with `navSlots.chrome: 'none'` to render a single per-cluster view
   * chromeless under the host's own chrome (Radar Hub's per-cluster destinations).
   */
  initialPath?: string;
  /**
   * Reports cluster-data warmup after the main connection is usable. Embedders
   * with their own chrome (Radar Hub) can render this in their topbar while
   * Radar runs with `navSlots.chrome: 'none'`.
   */
  onClusterLoadStateChange?: (state: ClusterLoadState) => void;
  /**
   * Selects the store backing the event timeline. Omit for the local event
   * store the Radar binary keeps (default, standalone behavior). Set
   * `{ mode: 'retained' }` when embedding behind a proxy that serves a
   * longer-horizon history at `{apiBase}/timeline/events` +
   * `{apiBase}/timeline/overview`; `maxRangeDays` caps how far back the
   * 'all' range reaches. Generic extension point — the backend that answers
   * the retained endpoints is the host's concern.
   *
   * Changing `mode` between renders remounts the timeline view (the local and
   * retained sources expose different `useEvents` hooks; remounting avoids a
   * React hook-order violation). Set it once at mount when possible.
   */
  timelineSource?: TimelineSourceConfig;
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
        if (message)
          showApiSuccess(message, mutation.options.meta?.successDetail);
      },
    }),
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.state.data !== undefined) {
          console.warn(
            "[Background sync failed]",
            query.queryKey,
            (error as Error).message,
          );
        }
      },
    }),
  });
}

export function RadarApp({
  apiBase,
  basename,
  router = "browser",
  queryClient,
  navSlots,
  manageDocumentTitle = false,
  documentTitleSuffix,
  renderDiagnoseAction,
  initialPath,
  onClusterLoadStateChange,
  timelineSource,
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
  const client = React.useMemo(
    () => queryClient ?? makeDefaultQueryClient(),
    [queryClient],
  );

  const inner = (
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <ToastProvider>
          <NavCustomizationProvider value={navSlots}>
            <FilterLocationBridge>
              <TimelineSourceProvider config={timelineSource}>
                <DiagnoseCustomizationProvider
                  value={renderDiagnoseAction ?? defaultDiagnoseAction}
                >
                  <DiagnoseProvider>
                    <App
                      manageDocumentTitle={manageDocumentTitle}
                      documentTitleSuffix={documentTitleSuffix}
                      onClusterLoadStateChange={onClusterLoadStateChange}
                    />
                  </DiagnoseProvider>
                </DiagnoseCustomizationProvider>
              </TimelineSourceProvider>
            </FilterLocationBridge>
          </NavCustomizationProvider>
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );

  if (router === "memory") {
    return (
      <MemoryRouter initialEntries={[initialPath || "/"]}>{inner}</MemoryRouter>
    );
  }

  return (
    <BrowserRouter basename={basename || undefined}>{inner}</BrowserRouter>
  );
}

export default RadarApp;
