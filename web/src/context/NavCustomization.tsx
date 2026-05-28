// Slot-based customization of Radar's top nav.
//
// Lets library consumers (e.g. Radar Hub) swap out the brand area, the
// context picker, and append items on the right of the action bar —
// without forking App.tsx or building a parallel nav.
//
// The `embedded` flag hides chrome that only makes sense for Radar's
// standalone OSS binary: GitHub star link, update-from-GitHub notifier,
// Radar's own OIDC/proxy-mode UserMenu. Consumers typically provide
// their own auth UI via `rightExtras`.
//
// Default (no provider): Radar renders its standalone nav unchanged.
import { createContext, useContext } from 'react';
import type { ReactNode } from 'react';

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
   * When set, Radar treats the host's fleet Checks page as the one canonical
   * Checks surface in Cloud: it removes its own per-cluster Audit tab and
   * redirects any route to /audit (the Home "Cluster Audit" card, ⌘K,
   * bookmarks) to the URL returned here — the host's fleet Checks page scoped
   * to this cluster. Navigated via window.location.replace (a cross-document
   * hop into the host's router) so the transient /audit URL stays out of
   * history. This keeps the per-cluster view and the host's fleet nav from
   * presenting two diverging Checks surfaces. Standalone Radar omits this and
   * keeps its single-cluster Audit tab.
   */
  clusterChecksHref?: () => string;
}

/**
 * Slot-based customization of Radar's top nav.
 *
 * Standalone-mode consumers pass `embedded: false` (or omit it) and may
 * optionally append items via `rightExtras`. Embedded-mode consumers must
 * supply `rightExtras` — Radar's OSS chrome (GitHub star, update notifier,
 * built-in UserMenu) is hidden, so the host app owns the right side of the
 * nav and must render its own user/auth UI there.
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
