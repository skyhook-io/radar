import type { InvestigationRefusal } from "../../api/diagnose";
import { useDiagnoseCustomization } from "../../context/DiagnoseCustomization";

/** The host's action for a refused start or follow-up, or nothing when the
 * refusal carries no code, reason or action, or the host offers none. */
export function InvestigationRefusalAction({
  refusal,
}: {
  refusal: InvestigationRefusal | null;
}) {
  const { renderRefusalAction } = useDiagnoseCustomization();
  if (!refusal || !renderRefusalAction) return null;
  return <>{renderRefusalAction(refusal)}</>;
}
