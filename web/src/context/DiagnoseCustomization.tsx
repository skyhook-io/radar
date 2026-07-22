// Slot-based injection of a resource-level "Diagnose" action, and of the
// consent card's trust copy.
//
// Lets an embedding host (e.g. Radar Hub) inject a "Diagnose with AI" button
// into every resource detail action bar — without forking WorkloadView or the
// shared ResourceActionsBar. The host returns whatever node should render in
// the action bar's right-aligned universal-actions area, given the resource
// context.
//
// Default (no provider): Radar renders no Diagnose button — OSS stays
// agent-free.
import { createContext, useContext, useMemo } from 'react';
import type { ReactNode } from 'react';

/** Render prop for the resource-level Diagnose action. */
export type RenderDiagnoseAction = (ctx: {
  kind: string;
  namespace: string;
  name: string;
  /** Coarse health of the resource (from its status badge), so the entry point can
   *  adapt: an urgent "Diagnose" on a problem vs. a quiet "ask AI" when fine/unknown. */
  health?: "problem" | "healthy" | "unknown";
}) => ReactNode;

/**
 * Trust copy for the first-run consent card.
 *
 * The card makes concrete, checkable claims about *where* the agent runs,
 * *whose* model account it bills, and *where* the transcript is stored. Those
 * claims are only true of OSS's bring-your-own-local-CLI agent. A host that
 * runs the agent anywhere else (Radar Cloud runs it as a sandboxed Job under a
 * managed key) MUST supply its own copy — shipping OSS's over a different data
 * flow states the opposite of what happens.
 *
 * Radar owns the card's chrome (icon, layout, Approve/Cancel) either way; a
 * host only replaces the claims.
 */
export type DiagnoseConsentCopy = {
  title: string;
  body: ReactNode;
  /** Detail list under the body; each entry is rendered as its own "•" row. */
  bullets?: ReactNode[];
  /** Label for the settings link. `null` hides it — for hosts with one fixed
   *  agent and no isolation choice, where it would open an empty dialog. */
  settingsLabel?: string | null;
  approveLabel?: string;
};

// One context for the whole customization seam — the values are host config
// set once at mount, so per-value re-render isolation buys nothing.
export interface DiagnoseCustomization {
  renderAction: RenderDiagnoseAction | undefined;
  consentCopy: DiagnoseConsentCopy | undefined;
  // undefined = default (CustomEvent → Radar's own Settings dialog);
  // null = hide the settings affordances.
  onOpenSettings: (() => void) | null | undefined;
}

const DEFAULTS: DiagnoseCustomization = {
  renderAction: undefined,
  consentCopy: undefined,
  onOpenSettings: undefined,
};

const DiagnoseCustomizationContext = createContext<DiagnoseCustomization>(DEFAULTS);

export function DiagnoseCustomizationProvider({
  value,
  consentCopy,
  onOpenSettings,
  children,
}: {
  value: RenderDiagnoseAction | undefined;
  consentCopy?: DiagnoseConsentCopy;
  /** Where "AI settings" affordances lead. Omit for Radar's own Settings
   *  dialog; pass `null` to hide them. */
  onOpenSettings?: (() => void) | null;
  children: ReactNode;
}) {
  const ctx = useMemo(
    () => ({ renderAction: value, consentCopy, onOpenSettings }),
    [value, consentCopy, onOpenSettings],
  );
  return (
    <DiagnoseCustomizationContext.Provider value={ctx}>
      {children}
    </DiagnoseCustomizationContext.Provider>
  );
}

export function useDiagnoseCustomization(): DiagnoseCustomization {
  return useContext(DiagnoseCustomizationContext);
}
