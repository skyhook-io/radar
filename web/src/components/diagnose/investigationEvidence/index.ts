// The module's public surface. Importers name the directory, never a file
// inside it, so what this file re-exports is the whole contract. Anything
// absent here is internal: exported from its own file only so a sibling can
// reach it, and free to move between files without affecting a caller.

export { metricsScope } from "./adapters/prometheus";
export { projectInvestigationEvidence } from "./builder";
export {
  investigationActivitySourceDomId,
  investigationEvidenceSourceDomId,
  investigationEvidenceSourceId,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSubjectRef,
  investigationSourceArgs,
  isInvestigationEvidenceRef,
  resolveInvestigationRootCauseEvidence,
} from "./identity";
export { evidenceSemanticSnapshot } from "./observations";
export type {
  InvestigationAccessCheck,
  InvestigationAlertInstance,
  InvestigationAlertRule,
  InvestigationEventEvidence,
  InvestigationEvidenceCoverage,
  InvestigationEvidenceData,
  InvestigationEvidenceGroup,
  InvestigationEvidenceKind,
  InvestigationEvidenceLimitation,
  InvestigationEvidenceObservation,
  InvestigationEvidencePhase,
  InvestigationEvidenceProjection,
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationEvidenceTarget,
  InvestigationEvidenceTier,
  InvestigationEvidenceTimelineItem,
  InvestigationEvidenceTurn,
  InvestigationGitOpsDiagnosis,
  InvestigationHelmOperation,
  InvestigationHelmOwnedResource,
  InvestigationHelmRelease,
  InvestigationKubernetesResource,
  InvestigationMetricsEvidence,
  InvestigationMetricsSelector,
  InvestigationNetworkEvidence,
  InvestigationNetworkRoute,
  InvestigationPermissionBinding,
  InvestigationPermissionSubject,
  InvestigationResourceContext,
  InvestigationResourceSummary,
  InvestigationRootCauseEvidenceLink,
  InvestigationRootCauseEvidenceResolution,
  InvestigationTopologyEdge,
  InvestigationTopologyNode,
} from "./types";
