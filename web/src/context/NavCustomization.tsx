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
