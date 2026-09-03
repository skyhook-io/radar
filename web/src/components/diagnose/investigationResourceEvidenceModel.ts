const PRIMARY_CONFIG_KEY_MARKERS = ["address", "host", "url", "endpoint"];
const SECONDARY_CONFIG_KEY_MARKERS = ["user", "database", "db", "name"];
export const INVESTIGATION_CONFIG_ROW_LIMIT = 8;
const SENSITIVE_KEY_TOKENS = new Set([
  "password",
  "passwd",
  "passphrase",
  "token",
  "secret",
  "key",
  "credential",
  "credentials",
  "private",
]);

type ResourceRecord = Record<string, unknown>;

export interface InvestigationResourceEvidenceInput {
  kind: string;
  metadata: {
    annotations?: unknown;
    creationTimestamp?: unknown;
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface InvestigationConfigEntry {
  key: string;
  value?: string;
  binary: boolean;
  sensitive: boolean;
}

export interface InvestigationResourceConditionEvidence {
  type: string;
  status: string;
  reason?: string;
  message?: string;
}

export type InvestigationResourceEvidenceModel =
  | {
      kind: "configmap";
      entries: InvestigationConfigEntry[];
      summary: string;
      hasDetails: boolean;
    }
  | {
      kind: "secret";
      keys: string[];
      secretType: string;
      summary: string;
      hasDetails: boolean;
    }
  | {
      kind: "sealedsecret";
      encryptedKeys: string[];
      conditions: InvestigationResourceConditionEvidence[];
      syncLabel: "Synced" | "Not synced" | "Sync unknown";
      scope: "Cluster-wide" | "Namespace-wide" | "Strict";
      observedGeneration?: string;
      created?: { dateTime: string; label: string };
      summary: string;
      hasDetails: boolean;
    };

function valueRecord(value: unknown): ResourceRecord | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as ResourceRecord)
    : undefined;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function keyMarkers(key: string): string[] {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
}

function compactKey(key: string): string {
  return key.toLowerCase().replace(/[^a-z0-9]/g, "");
}

export function isSensitiveInvestigationConfigKey(key: string): boolean {
  const tokens = keyMarkers(key);
  if (tokens.some((token) => SENSITIVE_KEY_TOKENS.has(token))) return true;

  const compact = compactKey(key);
  return [
    "password",
    "passphrase",
    "token",
    "secret",
    "credential",
    "privatekey",
    "apikey",
    "accesskey",
  ].some((marker) => compact.includes(marker));
}

function isSensitiveInvestigationConfigValue(value: string): boolean {
  return (
    /\b[a-z][a-z0-9+.-]*:\/\/[^:/@\s]*:[^/\s?#]+@/i.test(value) ||
    /\bBearer\s+[A-Za-z0-9\-._~+/]{20,}/i.test(value) ||
    /\bsk-[A-Za-z0-9_-]{20,}\b/.test(value) ||
    /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/.test(value) ||
    /\bAKIA[0-9A-Z]{16}\b/.test(value) ||
    /\bgh[oprsu]_[A-Za-z0-9]{20,}\b/.test(value) ||
    /\bgithub_pat_[A-Za-z0-9_]{22,}\b/.test(value) ||
    /password[=:]\s*\S{8,}/i.test(value) ||
    /\$(?:apr1|2[aby]|5|6)\$[./A-Za-z0-9$]{8,}/.test(value) ||
    /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/.test(
      value,
    )
  );
}

function configKeyPriority(key: string): number {
  const compact = compactKey(key);
  if (PRIMARY_CONFIG_KEY_MARKERS.some((marker) => compact.includes(marker))) {
    return 0;
  }
  if (SECONDARY_CONFIG_KEY_MARKERS.some((marker) => compact.includes(marker))) {
    return 1;
  }
  return 2;
}

function configEntries(
  resource: InvestigationResourceEvidenceInput,
): InvestigationConfigEntry[] {
  const plainData = valueRecord(resource.data);
  const binaryData = valueRecord(resource.binaryData);
  const entries: InvestigationConfigEntry[] = [];

  for (const [key, value] of Object.entries(plainData ?? {})) {
    if (typeof value !== "string") continue;
    entries.push({
      key,
      value,
      binary: false,
      sensitive:
        isSensitiveInvestigationConfigKey(key) ||
        isSensitiveInvestigationConfigValue(value),
    });
  }
  for (const key of Object.keys(binaryData ?? {})) {
    entries.push({
      key,
      binary: true,
      sensitive: isSensitiveInvestigationConfigKey(key),
    });
  }

  return entries.sort(
    (left, right) =>
      configKeyPriority(left.key) - configKeyPriority(right.key) ||
      left.key.localeCompare(right.key),
  );
}

function secretKeys(resource: InvestigationResourceEvidenceInput): string[] {
  // Radar's current get_resource producer structurally removes Secret values
  // and emits only this explicit key-name list. Do not inspect data/stringData:
  // this stays fail-closed even if an unexpected object reaches the UI.
  if (!Array.isArray(resource.keys)) return [];
  return resource.keys
    .filter((key): key is string => typeof key === "string")
    .sort((left, right) => left.localeCompare(right));
}

function sealedSecretEncryptedKeys(
  resource: InvestigationResourceEvidenceInput,
): string[] {
  const spec = valueRecord(resource.spec);
  return Object.keys(valueRecord(spec?.encryptedData) ?? {}).sort(
    (left, right) => left.localeCompare(right),
  );
}

function resourceConditions(
  resource: InvestigationResourceEvidenceInput,
): InvestigationResourceConditionEvidence[] {
  const status = valueRecord(resource.status);
  if (!Array.isArray(status?.conditions)) return [];

  return status.conditions.flatMap((value) => {
    const condition = valueRecord(value);
    const type = stringValue(condition?.type);
    const conditionStatus = stringValue(condition?.status);
    if (!type || !conditionStatus) return [];
    return [
      {
        type,
        status: conditionStatus,
        reason: stringValue(condition?.reason),
        message: stringValue(condition?.message),
      },
    ];
  });
}

function sealedSecretScope(
  resource: InvestigationResourceEvidenceInput,
): "Cluster-wide" | "Namespace-wide" | "Strict" {
  const annotations = valueRecord(resource.metadata.annotations);
  if (annotations?.["sealedsecrets.bitnami.com/cluster-wide"] === "true") {
    return "Cluster-wide";
  }
  if (annotations?.["sealedsecrets.bitnami.com/namespace-wide"] === "true") {
    return "Namespace-wide";
  }
  return "Strict";
}

function sealedSecretSyncLabel(
  conditions: InvestigationResourceConditionEvidence[],
): "Synced" | "Not synced" | "Sync unknown" {
  const synced = conditions.find(
    (condition) => condition.type.toLowerCase() === "synced",
  );
  if (synced?.status.toLowerCase() === "true") return "Synced";
  if (synced?.status.toLowerCase() === "false") return "Not synced";
  return "Sync unknown";
}

function timestamp(
  value: unknown,
): { dateTime: string; label: string } | undefined {
  if (typeof value !== "string" || value.length === 0) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return { dateTime: value, label: value };
  const iso = date.toISOString();
  return {
    dateTime: value,
    label: `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`,
  };
}

function clippedSummaryValue(value: string): string {
  const compact = value.replace(/\s+/g, " ").trim();
  return compact.length > 56 ? `${compact.slice(0, 55)}…` : compact;
}

function configEntrySummary(entry: InvestigationConfigEntry): string {
  if (entry.sensitive) return `${entry.key}=[hidden]`;
  if (entry.binary) return `${entry.key}=[binary hidden]`;
  return `${entry.key}=${clippedSummaryValue(entry.value ?? "")}`;
}

function configMapSummary(entries: InvestigationConfigEntry[]): string {
  if (entries.length === 0) return "No data keys in this result";

  // Put legible causal facts ahead of redaction placeholders. If every value is
  // hidden, retain up to two key names so the summary still says what was read.
  const visible = entries
    .slice(0, INVESTIGATION_CONFIG_ROW_LIMIT)
    .filter((entry) => !entry.binary && !entry.sensitive);
  const shown = visible.length > 0 ? visible.slice(0, 2) : entries.slice(0, 2);
  const shownEntries = new Set(shown);
  const remaining = entries.filter((entry) => !shownEntries.has(entry));
  const remainingVisible = remaining.filter(
    (entry) => !entry.binary && !entry.sensitive,
  ).length;
  const remainingHidden = remaining.length - remainingVisible;
  return [
    ...shown.map(configEntrySummary),
    ...(remainingVisible > 0
      ? [
          `${remainingVisible} more ${remainingVisible === 1 ? "value" : "values"}`,
        ]
      : []),
    ...(remainingHidden > 0 ? [`${remainingHidden} hidden`] : []),
  ].join(" · ");
}

export function buildInvestigationResourceEvidenceModel(
  resource: InvestigationResourceEvidenceInput,
): InvestigationResourceEvidenceModel | undefined {
  switch (resource.kind.toLowerCase()) {
    case "configmap": {
      const entries = configEntries(resource);
      return {
        kind: "configmap",
        entries,
        summary: configMapSummary(entries),
        hasDetails: entries.length > 0,
      };
    }
    case "secret": {
      const keys = secretKeys(resource);
      const secretType = stringValue(resource.type) ?? "Opaque";
      return {
        kind: "secret",
        keys,
        secretType,
        summary: `${keys.length} ${keys.length === 1 ? "key" : "keys"}${keys.length === 0 ? ` · ${secretType}` : ""} · values hidden`,
        hasDetails: keys.length > 0,
      };
    }
    case "sealedsecret": {
      const encryptedKeys = sealedSecretEncryptedKeys(resource);
      const conditions = resourceConditions(resource);
      const syncLabel = sealedSecretSyncLabel(conditions);
      const status = valueRecord(resource.status);
      const observedGeneration =
        typeof status?.observedGeneration === "number" ||
        typeof status?.observedGeneration === "string"
          ? String(status.observedGeneration)
          : undefined;
      const created = timestamp(resource.metadata.creationTimestamp);
      const scope = sealedSecretScope(resource);
      return {
        kind: "sealedsecret",
        encryptedKeys,
        conditions,
        syncLabel,
        scope,
        observedGeneration,
        created,
        summary: `${encryptedKeys.length} encrypted ${encryptedKeys.length === 1 ? "key" : "keys"} · ${syncLabel}`,
        hasDetails: Boolean(
          encryptedKeys.length > 0 ||
          conditions.length > 0 ||
          observedGeneration ||
          created ||
          scope !== "Strict",
        ),
      };
    }
    default:
      return undefined;
  }
}

/** A compact, redacted summary suitable for the collapsed evidence-card row. */
export function investigationResourceEvidenceSummary(
  resource: InvestigationResourceEvidenceInput,
): string | undefined {
  return buildInvestigationResourceEvidenceModel(resource)?.summary;
}

/** Whether the specialized resource body adds facts beyond its summary. */
export function investigationResourceEvidenceHasDetails(
  resource: InvestigationResourceEvidenceInput,
): boolean {
  return buildInvestigationResourceEvidenceModel(resource)?.hasDetails ?? false;
}
