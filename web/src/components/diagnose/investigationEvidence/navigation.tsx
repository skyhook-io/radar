import { createContext } from "react";
import type { MetricsChangeCoverage } from "../investigationMetrics";
import type { DiagnosisResourceRef } from "../diagnoseEvidenceTypes";
import type { InvestigationCaseItem } from "../investigationCase";

/** The Timeline scope a changes card can open: one resource name in one namespace. */
export interface InvestigationTimelineScope {
  namespace?: string;
  name: string;
}

export const EvidenceNavigationContext = createContext<{
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
  revealSourceId?: string;
  revealRequestId?: number;
  expandedGroupIds?: ReadonlySet<string>;
  onGroupOpenChange?: (id: string, open: boolean) => void;
  citedOrderByGroup?: ReadonlyMap<string, number>;
  /** Change markers for each metrics observation, keyed by its source id. */
  metricsMarkersBySource?: ReadonlyMap<string, MetricsChangeCoverage>;
  /** Card- and revision-placed agent items, keyed by group id. */
  caseByGroup?: ReadonlyMap<string, InvestigationCaseItem[]>;
  /** The hypothesis each `rules_out` item excludes, keyed by item. */
  excludedByItem?: ReadonlyMap<string, string>;
}>({});
