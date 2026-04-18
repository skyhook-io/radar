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

export interface NavCustomization {
  /** Replaces Radar's Skyhook/radar logo + wordmark. */
  brandSlot?: ReactNode;
  /** Replaces the ContextSwitcher (kubeconfig-context picker). */
  contextSlot?: ReactNode;
  /** Appended to the right of the action bar (before or in place of UserMenu). */
  rightExtras?: ReactNode;
  /**
   * Hide OSS-distribution-specific chrome: GitHub star button,
   * update-available notifier, Radar's own auth UserMenu. Consumers
   * should render equivalent product-level chrome via `rightExtras`.
   */
  embedded?: boolean;
}

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
