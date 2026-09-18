// Slot-based customization of Radar's top bar.
//
// Lets library consumers (e.g. Radar Hub) swap out the brand area, the
// context picker, and append items on the right of the action bar —
// without forking App.tsx or rebuilding Radar's action chrome.
//
// The `embedded` flag hides chrome that only makes sense for Radar's
// standalone OSS binary: GitHub star link, update-from-GitHub notifier,
// Radar's own OIDC/proxy-mode UserMenu. Consumers typically provide
// their own auth UI via `rightExtras`.
//
// Default (no provider): Radar renders its standalone nav unchanged.
import { createContext, useContext } from 'react';
import type { ReactNode } from 'react';

/**
 * Per-cluster destinations an embedded host can take over with its own
 * fleet-scoped pages. See `fleetTakeoverHref`. 'issues' | 'gitops' | 'checks'
 * are also Radar view names (so route entry redirects too); 'certs' is
 * card-only (Radar has no certs view).
 */
export type FleetTakeoverTarget = 'issues' | 'gitops' | 'checks' | 'certs';

interface NavCustomizationBase {
  /** Replaces Radar's Skyhook/radar logo + wordmark. */
  brandSlot?: ReactNode;
  /** Replaces the ContextSwitcher (kubeconfig-context picker). */
  contextSlot?: ReactNode;
  /**
   * When set, a "Compare across clusters" option is added to the Compare
   * button in resource action bars. The host returns the URL that should
   * be navigated to (via window.location.assign — typically a hub fleet
   * route). Standalone Radar omits this and the compare action stays
   * single-cluster.
   */
  crossClusterCompareHref?: (ref: {
    kind: string;
    namespace: string;
    name: string;
    group?: string;
  }) => string;
  /**
   * Lets an embedded host (e.g. Radar Cloud) take over selected per-cluster
   * destinations with its OWN fleet pages scoped to this cluster, instead of
   * Radar rendering them inline. Given a semantic target the host returns the
   * URL to navigate to, or `undefined`/omits the hook to let Radar render its
   * own view as usual (standalone OSS does the latter for everything).
   *
   * This is how the Home dashboard's "fleet-shaped" cards reach the host's
   * canonical surfaces rather than a second, diverging per-cluster copy:
   *   - 'issues'  → the Active Issues panel + cluster-health issues count
   *   - 'gitops'  → the GitOps controllers card
   *   - 'checks'  → the Cluster Audit card (and any route to /audit; legacy
   *                 `clusterChecksHref` folded in here)
   *   - 'certs'   → the Certificate Health card
   *
   * View-shaped targets (issues / gitops / checks) are honored for every entry:
   * in-app nav (Home cards, ⌘K, "view all") hands straight to the host from
   * `setMainView` via `onHostNavigate` (smooth same-document hand-off, no
   * intermediate /<view> mount); a direct /<view> URL (bookmark/deep link)
   * funnels through a redirect effect that uses `window.location.replace` so
   * the transient URL stays out of history. 'certs' has no Radar view, so only
   * the card consults it. `onHostNavigate` is optional — without it everything
   * falls back to `window.location` (a hard reload).
   */
  fleetTakeoverHref?: (target: FleetTakeoverTarget) => string | undefined;
  /**
   * @deprecated Superseded by `fleetTakeoverHref('checks')`. Kept so consumers
   * still on the pre-1.7 hook keep working (App.tsx folds it into the 'checks'
   * target) — this makes adding `fleetTakeoverHref` an additive, non-breaking
   * change. Remove in a major release once all consumers have migrated.
   */
  clusterChecksHref?: () => string;
  /**
   * Optional smooth navigator for host-owned URLs. When the host takes a
   * destination over (`fleetTakeoverHref`, `crossClusterCompareHref`), Radar
   * would otherwise hand off via `window.location` — a full document reload
   * that cold-boots the host (white flash, re-auth, chrome teardown). A host
   * that can navigate SAME-DOCUMENT (e.g. Radar Cloud's cross-tree swap with a
   * View Transition) passes this so the hand-off morphs instead of reloading.
   * Omitted → Radar falls back to `window.location` (hard nav), so standalone
   * OSS / other hosts are unaffected.
   */
  onHostNavigate?: (url: string) => void;
  /**
   * Chrome level for embedded hosts. Embedded hosts own primary view
   * navigation in either mode. Default ('full', or omitted) retains Radar's
   * top bar for the brand, context, and action slots. 'none' suppresses the top
   * bar too, so the host owns all chrome and Radar renders only the active
   * view's content full-bleed. Radar Hub uses this to surface per-cluster views
   * (Topology / Resources / Traffic / Cost) as native cloud destinations under
   * one host-owned chrome, gated by a cluster picker. Only meaningful with
   * `embedded: true`.
   */
  chrome?: 'full' | 'none';
}

/**
 * Slot-based customization of Radar's top bar.
 *
 * Standalone-mode consumers pass `embedded: false` (or omit it) and may
 * optionally append items via `rightExtras`. Embedded-mode consumers must
 * supply `rightExtras` — Radar's OSS chrome (GitHub star, update notifier,
 * built-in UserMenu) is hidden, so the host app owns the right side of the
 * top bar and must render its own user/auth UI there. Embedded hosts provide
 * primary view navigation outside Radar.
 */
export type NavCustomization =
  | (NavCustomizationBase & {
      embedded?: false;
      /** Appended to the right of the action bar (before the UserMenu). */
      rightExtras?: ReactNode;
    })
  | (NavCustomizationBase & {
      embedded: true;
      /** Required in embedded mode: Radar's own UserMenu is hidden. */
      rightExtras: ReactNode;
    });

const NavCustomizationContext = createContext<NavCustomization>({});

export function NavCustomizationProvider({
  value,
  children,
}: {
  value: NavCustomization | undefined;
  children: ReactNode;
}) {
  return (
    <NavCustomizationContext.Provider value={value ?? {}}>
      {children}
    </NavCustomizationContext.Provider>
  );
}

export function useNavCustomization(): NavCustomization {
  return useContext(NavCustomizationContext);
}
