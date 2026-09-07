/**
 * Structural contracts emitted by Radar evidence tools and consumed by the
 * investigation projection. These deliberately exclude any presentation or
 * transport envelope so the live Diagnose UI is not coupled to another route.
 */
export interface DiagnosisFilteredLogs {
  lines: string[] | null;
  totalLines: number;
  matchedLines: number;
  fallback: boolean;
}

export interface DiagnosisPodLogEntry {
  pod: string;
  container: string;
  logs?: DiagnosisFilteredLogs;
  error?: string;
}

export interface DiagnosisPodContainerRef {
  pod: string;
  container: string;
}

export interface DiagnosisCrashCause {
  pods: string[];
  container: string;
  state: string;
  reason?: string;
  exitCode: number;
  logLine: string;
  logSource: string;
  logLineSelection:
    | "fatal_pattern"
    | "traceback_header_only"
    | "last_matched_line"
    | "log_tail"
    | string;
}

export interface DiagnosisStartupBlocker {
  kind: string;
  name: string;
  reason: string;
  severity: string;
  message: string;
}

export interface DiagnosisResourceRef {
  kind: string;
  group?: string;
  namespace?: string;
  name: string;
}

export interface DiagnosisResourceContext {
  tier: "basic" | "diagnostic";
  owner?: DiagnosisResourceRef;
  managedBy?: DiagnosisResourceRef[];
  exposes?: DiagnosisResourceRef[];
  selectedBy?: DiagnosisResourceRef[];
  referencedBy?: {
    total: number;
    items?: Array<DiagnosisResourceRef & { paths?: string[] }>;
    truncated?: boolean;
  };
  uses?: {
    configMaps?: DiagnosisResourceRef[];
    secrets?: DiagnosisResourceRef[];
    serviceAccount?: DiagnosisResourceRef;
    pvcs?: DiagnosisResourceRef[];
  };
  runsOn?: DiagnosisResourceRef;
  scaledBy?: DiagnosisResourceRef[];
  statusSummary?: {
    phase?: string;
    conditions?: Array<{
      type: string;
      status: string;
      reason?: string;
      message?: string;
      lastTransitionTime?: string;
    }>;
  };
  workloadSummary?: {
    replicas?: {
      desired?: number;
      ready?: number;
      available?: number;
      updated?: number;
      unavailable?: number;
    };
    rolloutRisk?: {
      reason: string;
      replicas: number;
      maxSurge: string;
      maxUnavailable: string;
      resolvedMaxSurge: number;
      resolvedMaxUnavailable: number;
      message: string;
      action: string;
    };
  };
  appReferences?: {
    staleSecretEnvTruncated?: boolean;
  };
  omitted?: Array<{
    field: string;
    reason: string;
  }>;
}

export interface DiagnosisChangeContext {
  changed: boolean;
  what?: string;
  when?: string;
  evidence?: string;
}

export interface DiagnosisDNSContext {
  signals?: string[];
  coreDNSFindings?: Array<{
    kind: string;
    namespace: string;
    name: string;
    severity: string;
    reason: string;
    message?: string;
  }>;
}

export type DiagnosisEvidenceTone =
  "error" | "alert" | "warning" | "info" | "neutral";

export interface DiagnosisEvidenceLimitationBase {
  source: string;
  message: string;
  kind: "error" | "truncated" | "unknown";
}

export function diagnosisSeverityTone(severity: string): DiagnosisEvidenceTone {
  const value = severity.toLowerCase();
  if (value === "critical" || value === "error" || value === "failed")
    return "error";
  if (value === "high" || value === "alert") return "alert";
  if (value === "medium" || value === "warning" || value === "warn")
    return "warning";
  if (value === "low" || value === "info" || value === "informational")
    return "info";
  return "neutral";
}
