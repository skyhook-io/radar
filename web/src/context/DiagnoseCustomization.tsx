// Slot-based injection of a resource-level "Diagnose" action.
//
// Lets an embedding host (e.g. Radar Hub) inject a "Diagnose with AI" button
// into every resource detail action bar — without forking WorkloadView or the
// shared ResourceActionsBar. The host returns whatever node should render in
// the action bar's right-aligned universal-actions area, given the resource
// context.
//
// Default (no provider): Radar renders no Diagnose button — OSS stays
// agent-free.
import { createContext, useContext } from 'react';
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

const DiagnoseCustomizationContext = createContext<RenderDiagnoseAction | undefined>(undefined);

export function DiagnoseCustomizationProvider({
  value,
  children,
}: {
  value: RenderDiagnoseAction | undefined;
  children: ReactNode;
}) {
  return (
    <DiagnoseCustomizationContext.Provider value={value}>
      {children}
    </DiagnoseCustomizationContext.Provider>
  );
}

export function useDiagnoseCustomization(): RenderDiagnoseAction | undefined {
  return useContext(DiagnoseCustomizationContext);
}
