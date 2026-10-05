import type { HealthLevel } from "./resource-utils";
import { isApiGroup } from "./resource-utils-cnpg";

export const DATUM_GROUPS: Record<string, string[]> = {
  "dns.networking.miloapis.com": ["DNSZone", "DNSRecordSet", "DNSZoneClass"],
  "networking.datumapis.com": [
    "Domain",
    "HTTPProxy",
    "Connector",
    "ConnectorClass",
    "ConnectorAdvertisement",
    "Network",
    "NetworkContext",
    "NetworkBinding",
    "Subnet",
    "SubnetClaim",
    "NetworkService",
  ],
  "compute.datumapis.com": ["Instance", "Workload"],
  "resourcemanager.miloapis.com": ["Project", "Organization"],
};
export function isDatumResource(data: any): boolean {
  return Object.entries(DATUM_GROUPS).some(
    ([group, kinds]) =>
      isApiGroup(data?.apiVersion, group) && kinds.includes(data?.kind),
  );
}
export function datumObservations(data: any): any[] {
  const status = data.status || {};
  return [
    ...(status.conditions || []).map((c: any) => ({ ...c, scope: "" })),
    ...["hostnameStatuses", "recordSets", "capabilities"].flatMap((field) =>
      (status[field] || []).flatMap((item: any) =>
        (item.conditions || []).map((c: any) => ({
          ...c,
          scope: item.hostname || item.name || item.type,
        })),
      ),
    ),
  ].map((c: any) => ({
    ...c,
    observedGeneration:
      c.observedGeneration ??
      (data.kind === "Workload" ? status.observedGeneration : undefined),
  }));
}
const positive = new Set([
  "Accepted",
  "Programmed",
  "Ready",
  "Available",
  "CertificatesReady",
  "CertificateReady",
  "RecordProgrammed",
  "DNSRecordProgrammed",
  "DNSRecordsProgrammed",
  "HostnamesVerified",
  "Verified",
  "MembersResolved",
  "QuotaGranted",
]);
const transient = new Set([
  "Pending",
  "Progressing",
  "Reconciling",
  "PendingVerification",
  "Initializing",
  "Creating",
  "Updating",
  "WaitingForController",
  "DependencyNotReady",
  "ReconciliationInProgress",
  "Issuing",
  "InProgress",
  "Waiting",
  "ProgrammingInProgress",
  "Provisioning",
  "Starting",
  "Stopping",
  "InstancesProvisioning",
  "PendingProgramming",
  "PendingQuota",
  "ChallengeInProgress",
  "CertificatesPending",
  "RetryPending",
  "PendingEvaluation",
]);
export function datumConditionTone(c: any, generation = 0): HealthLevel {
  if (c.observedGeneration !== undefined && c.observedGeneration < generation)
    return "unknown";
  if (transient.has(c.reason)) return "neutral";
  return c.status === "True"
    ? "healthy"
    : c.status === "False"
      ? "degraded"
      : "unknown";
}
const required: Record<string, string[]> = {
  DNSZone: ["Accepted", "Programmed"],
  DNSRecordSet: ["Accepted", "Programmed"],
  HTTPProxy: ["Accepted", "Programmed"],
  Domain: ["ValidDomain", "Verified"],
  Connector: ["Accepted", "Ready"],
  Workload: ["Available"],
  Instance: ["Available"],
  DNSZoneClass: ["Accepted", "Programmed"],
  ConnectorAdvertisement: ["Accepted"],
};
export function getDatumStatus(data: any): {
  label: string;
  color: HealthLevel;
} {
  if (data.metadata?.deletionTimestamp)
    return { label: "Terminating", color: "neutral" };
  const observations = datumObservations(data).filter((c) =>
    data.kind === "Domain"
      ? ["Verified", "ValidDomain"].includes(c.type)
      : positive.has(c.type),
  );
  const stale = (c: any) =>
    c.observedGeneration !== undefined &&
    c.observedGeneration < (data.metadata?.generation || 0);
  if (
    data.kind === "HTTPProxy" &&
    datumObservations(data).some(
      (c) => c.type === "HostnamesInUse" && c.status === "True" && !stale(c),
    )
  )
    return { label: "Attention needed", color: "degraded" };
  if (
    observations.some(
      (c) => c.status === "False" && !stale(c) && !transient.has(c.reason),
    )
  )
    return { label: "Attention needed", color: "degraded" };
  if (observations.some((c) => stale(c) || transient.has(c.reason)))
    return { label: "Reconciling", color: "neutral" };
  if (!observations.length || observations.some((c) => c.status !== "True"))
    return { label: "Unknown", color: "unknown" };
  if (
    (required[data.kind] || ["Ready"]).some(
      (type) =>
        !observations.some(
          (c) => !c.scope && c.type === type && c.status === "True",
        ),
    )
  )
    return { label: "Unknown", color: "unknown" };
  return { label: "Ready", color: "healthy" };
}
