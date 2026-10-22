import { type IssueRecentChange } from "@skyhook-io/k8s-ui";
import {
  ProjectionBuilder,
  addChanges,
  addNarrowHint,
  changesSubjectFromArgs,
  invalidPayload,
  scopeFromArgs,
  sourceArgsRelevance,
} from "../observations";
import {
  nonEmptyString,
  parseJSON,
  recentChange,
  record,
  stringArray,
} from "../parse";
import { type InvestigationEvidenceSource } from "../types";

export function adaptChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  // The producer serializes an empty result as a nil slice, which is null.
  const changesRaw = value?.changes === null ? [] : value?.changes;
  if (!value || !Array.isArray(changesRaw)) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  const rows = changesRaw
    .map(recentChange)
    .filter((item): item is IssueRecentChange => Boolean(item));
  const created = createdChanges(value.created);
  if (rows.length !== changesRaw.length || !created) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  const changes = mergeCreated(rows, created);
  addNarrowHint(builder, source, value);
  let sourceErrors = 0;
  if (Array.isArray(value.sourcesErrored)) {
    for (const sourceError of value.sourcesErrored) {
      if (nonEmptyString(sourceError)) {
        sourceErrors += 1;
        builder.limit(source, "Recent changes", sourceError, "error");
      }
    }
  } else if (value.sourcesErrored !== undefined) {
    invalidPayload(builder, source, "Recent changes source coverage");
  }
  // Reads of one resource's history are revisions of one card however the
  // window or cap differs; the window is stated in the title instead.
  const args = record(source.args ? parseJSON(source.args) : undefined);
  addChanges(
    builder,
    source,
    changes,
    `changes:${scopeFromArgs(source)}`,
    undefined,
    !nonEmptyString(value.narrowHint) && sourceErrors === 0,
    false,
    sourceArgsRelevance(builder, source),
    changesSubjectFromArgs(source),
    nonEmptyString(args?.since) ? args.since : undefined,
  );
  addClusterScopedChanges(builder, source, value.clusterScopedChanges);
}

const CLUSTER_SCOPED_LABEL = "Cluster-scoped admission changes";

// Admission configuration changes that apply, or may apply, to the queried
// namespace. Each row's summary carries why it is listed, because the
// applicability is the point: the row itself is a cluster-scoped object.
export function addClusterScopedChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  if (payload === undefined) return;
  const value = record(payload);
  const rowsRaw = value?.changes === null ? [] : value?.changes;
  if (!value || !Array.isArray(rowsRaw)) {
    invalidPayload(builder, source, CLUSTER_SCOPED_LABEL);
    return;
  }
  const changes: IssueRecentChange[] = [];
  for (const raw of rowsRaw) {
    const change = recentChange(raw);
    const row = record(raw);
    if (!change || !row || !nonEmptyString(row.applies_because)) {
      invalidPayload(builder, source, CLUSTER_SCOPED_LABEL);
      return;
    }
    const applicability =
      row.applicability === "applies" ? "Applies" : "May apply";
    changes.push({
      ...change,
      summary: [change.summary, `${applicability}: ${row.applies_because}`]
        .filter(Boolean)
        .join(" · "),
    });
  }
  if (nonEmptyString(value.error)) {
    builder.limit(source, CLUSTER_SCOPED_LABEL, value.error, "error");
  }
  if (nonEmptyString(value.incomplete)) {
    builder.limit(source, CLUSTER_SCOPED_LABEL, value.incomplete, "unknown");
  }
  const notListed = stringArray(value.not_listed) ?? [];
  if (notListed.length > 0 || value.truncated === true) {
    builder.limit(
      source,
      CLUSTER_SCOPED_LABEL,
      notListed.length > 0
        ? `Also changed in this window: ${notListed.join(", ")}.`
        : "More cluster-scoped admission changes occurred than were read.",
      "truncated",
    );
  }
  if (changes.length === 0) return;
  addChanges(
    builder,
    source,
    changes,
    `changes:cluster-scoped:${scopeFromArgs(source)}`,
    undefined,
    value.truncated !== true,
  );
}

// The constant fields of a plain create row, which the producer leaves out of
// `created` (issuesapi.CreatedObjects).
const PLAIN_CREATE = {
  changeType: "add",
  summary: "add",
  change_category: "lifecycle",
  rank_reason: "resource create/delete for config or desired state",
} as const;

const CREATED_LINE = /^\+(\d+)s ([^ /]+)\/([^ /]+)(?: \(used by (.+)\))?$/;
const RFC3339_SECONDS =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(Z|[+-]\d{2}:\d{2})$/;

/**
 * Expands get_changes `created` runs back into the rows they replaced. Each
 * line is "+<seconds after first>s Kind/name", with " (used by ...)" for a
 * consumed ConfigMap; a member's timestamp is `first` plus the offset, written
 * in `first`'s zone so it matches the row the producer would have sent.
 * Returns [] when the field is absent and undefined when it is malformed.
 */
export function createdChanges(
  payload: unknown,
): IssueRecentChange[] | undefined {
  if (payload === undefined || payload === null) return [];
  if (!Array.isArray(payload)) return undefined;
  const out: IssueRecentChange[] = [];
  for (const raw of payload) {
    const entry = record(raw);
    const objects = stringArray(entry?.objects);
    const first = typeof entry?.first === "string" ? entry.first : "";
    const versions = record(entry?.apiVersions);
    if (
      !entry ||
      !nonEmptyString(entry.namespace) ||
      !objects ||
      entry.count !== objects.length ||
      addSeconds(first, 0) !== first ||
      (entry.apiVersions !== undefined && !versions)
    ) {
      return undefined;
    }
    for (const line of objects) {
      const match = CREATED_LINE.exec(line);
      if (!match) return undefined;
      const [, offset, kind, name, consumers] = match;
      const apiVersion = versions?.[kind];
      const timestamp = addSeconds(first, Number(offset));
      if (
        (apiVersion !== undefined && !nonEmptyString(apiVersion)) ||
        timestamp === undefined
      )
        return undefined;
      out.push({
        ...PLAIN_CREATE,
        kind,
        ...(apiVersion ? { apiVersion } : {}),
        namespace: entry.namespace,
        name,
        timestamp,
        ...(consumers ? { consumed_by: consumers.split(", ") } : {}),
      });
    }
  }
  return out;
}

// addSeconds adds whole seconds to an RFC3339 timestamp and keeps its zone
// designator, so "2026-10-16T04:43:24+03:00" + 41 is "2026-10-16T04:44:05+03:00".
// Returns undefined for a malformed timestamp or an out-of-range result; a
// calendar-invalid date (February 30) is caught by callers comparing
// addSeconds(t, 0) with t.
function addSeconds(timestamp: string, seconds: number): string | undefined {
  const m = RFC3339_SECONDS.exec(timestamp);
  if (!m || !Number.isSafeInteger(seconds)) return undefined;
  const [, y, mo, d, h, mi, s, zone] = m;
  const wall = new Date(
    Date.UTC(+y, +mo - 1, +d, +h, +mi, +s) + seconds * 1000,
  );
  if (Number.isNaN(wall.getTime())) return undefined;
  const pad = (n: number, width = 2) => String(n).padStart(width, "0");
  return (
    `${pad(wall.getUTCFullYear(), 4)}-${pad(wall.getUTCMonth() + 1)}-${pad(wall.getUTCDate())}` +
    `T${pad(wall.getUTCHours())}:${pad(wall.getUTCMinutes())}:${pad(wall.getUTCSeconds())}${zone}`
  );
}

/**
 * Puts expanded creates back where get_changes ranked them: plain creates
 * share the lifecycle rank, so they join the rows of that rank ordered newest
 * first (ties by kind/namespace/name, as the producer breaks them), after
 * every higher-ranked row and before status rows.
 */
export function mergeCreated(
  rows: IssueRecentChange[],
  created: IssueRecentChange[],
): IssueRecentChange[] {
  if (created.length === 0) return rows;
  const lifecycleBand = (c: IssueRecentChange) =>
    c.change_category === "lifecycle" &&
    !c.application_configuration_change &&
    !c.not_linked_to_returned_issues;
  const start = rows.findIndex(
    (c) => lifecycleBand(c) || c.change_category === "runtime_status",
  );
  const bandStart = start === -1 ? rows.length : start;
  let bandEnd = bandStart;
  while (bandEnd < rows.length && lifecycleBand(rows[bandEnd])) bandEnd++;
  const key = (c: IssueRecentChange) =>
    `${c.kind}/${c.namespace ?? ""}/${c.name}/${c.changeType}`;
  const band = [...rows.slice(bandStart, bandEnd), ...created].sort((a, b) => {
    const at = Date.parse(a.timestamp);
    const bt = Date.parse(b.timestamp);
    if (at !== bt) return bt - at;
    return key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0;
  });
  return [...rows.slice(0, bandStart), ...band, ...rows.slice(bandEnd)];
}
