// The module's public surface — the same 46 names the single file exported.
// Call sites import "./investigationEvidence", which resolves here, so the
// split is invisible outside this directory. Anything not listed is internal
// to the module: exported only so a sibling file can reach it.

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
