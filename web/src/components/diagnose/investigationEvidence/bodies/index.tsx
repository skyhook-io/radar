import type { MetricsChangeCoverage } from "../../investigationMetrics";
import type {
  InvestigationEvidenceData,
  InvestigationResourceSummary,
} from "..";
import { investigationResourceEvidenceHasDetails } from "../../investigationResourceEvidenceModel";
import {
  CrashBody,
  IssueBody,
  ResourceBody,
  StartupBody,
  diagnosedScalers,
} from "./workload";
import { ChangesBody, EventsBody, LogsBody } from "./streams";
import {
  DNSBody,
  NetworkBody,
  RelationshipsBody,
  TopologyBody,
} from "./network";
import { InventoryBody } from "./inventory";
import { AlertsBody, MetricsBody } from "./metrics";
import { HelmBody, PermissionsBody } from "./platform";
import { RankingBody } from "./ranking";
import { PostureBody } from "./posture";

export function EvidenceBody({
  data,
  cardSummary,
  changeCoverage,
  condensed = false,
  citedRows,
}: {
  data: InvestigationEvidenceData;
  cardSummary?: string;
  /** Change markers for a metrics chart; derived by the pane, never by data. */
  changeCoverage?: MetricsChangeCoverage;
  /** The card's title already names the stream: skip the repeated badges. */
  condensed?: boolean;
  /** Listing rows the card's citations name; they lead the inventory. */
  citedRows?: readonly InvestigationResourceSummary[];
}) {
  switch (data.type) {
    case "issue":
      return <IssueBody data={data} cardSummary={cardSummary} />;
    case "startup":
      return <StartupBody data={data} />;
    case "crash":
      return <CrashBody data={data} />;
    case "resource":
      return <ResourceBody data={data} />;
    case "logs":
      return <LogsBody data={data} condensed={condensed} />;
    case "events":
      return <EventsBody data={data} />;
    case "changes":
      return <ChangesBody data={data} />;
    case "dns":
      return <DNSBody data={data} />;
    case "network":
      return <NetworkBody data={data} />;
    case "relationships":
      return <RelationshipsBody data={data} />;
    case "topology":
      return <TopologyBody data={data} />;
    case "inventory":
      return <InventoryBody data={data} cited={citedRows} />;
    case "receipt":
      // The title and the scope above it are the answer. A body appears only
      // when it adds the reason or the limit, so an absent one renders nothing
      // rather than an empty line.
      return data.message ? (
        <p className="text-xs text-theme-text-secondary">{data.message}</p>
      ) : null;
    case "alerts":
      return <AlertsBody data={data} />;
    case "helm":
      return <HelmBody data={data} />;
    case "permissions":
      return <PermissionsBody data={data} />;
    case "metrics":
      return <MetricsBody data={data} changeCoverage={changeCoverage} />;
    case "ranking":
      return <RankingBody data={data} />;
    case "posture":
      return <PostureBody data={data} />;
  }
}

export function evidenceHasDetails(
  data: InvestigationEvidenceData,
  cardSummary?: string,
): boolean {
  switch (data.type) {
    case "issue": {
      const summary = cardSummary?.trim();
      const cause = data.issue.cause?.trim();
      const message = data.issue.message?.trim();
      return Boolean(
        (cause && cause !== summary) ||
        (message && message !== summary && message !== cause) ||
        data.pods?.length,
      );
    }
    case "startup":
      return (data.pods?.length ?? 0) > 1;
    case "receipt":
      return false;
    case "resource": {
      const replicas = data.resourceContext?.workloadSummary?.replicas;
      return Boolean(
        investigationResourceEvidenceHasDetails(data.resource) ||
        replicas?.desired !== undefined ||
        data.resourceContext?.statusSummary?.conditions?.length ||
        diagnosedScalers(data.resourceContext).length ||
        data.gitOpsDiagnosis ||
        data.warnings.length,
      );
    }
    case "logs":
      return (data.logs?.lines?.length ?? 0) > 0 || Boolean(data.error);
    case "changes":
      return data.changes.length > 0 || Boolean(data.changeContext?.evidence);
    default:
      return true;
  }
}
