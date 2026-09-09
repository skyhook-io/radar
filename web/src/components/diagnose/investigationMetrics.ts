import { displayKind, type IssueRecentChange } from "@skyhook-io/k8s-ui";
import type { ChartAnnotation } from "@skyhook-io/k8s-ui/components/charts";
import { apiVersionToGroup } from "../../utils/navigation";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import type {
  InvestigationEvidenceGroup,
  InvestigationEvidenceObservation,
  InvestigationMetricsEvidence,
  InvestigationMetricsSelector,
} from "./investigationEvidence";

function metricUnitSuffix(metric: string): "bytes" | "seconds" | undefined {
  // _sum is the sum of the observed values, so it measures what the histogram
  // measures. _bucket and _count are counts of observations: only the `le`
  // boundary carries the unit, and nothing here reads it, so a bucket series
  // has no unit this can print.
  if (metric.endsWith("_bucket") || metric.endsWith("_count")) return undefined;
  let base = metric;
  for (const suffix of ["_total", "_sum"]) {
    if (base.endsWith(suffix)) base = base.slice(0, -suffix.length);
  }
  if (base.endsWith("_bytes")) return "bytes";
  if (base.endsWith("_seconds")) return "seconds";
  return undefined;
}

/**
 * PromQL an axis label can survive. Anything outside this list leaves the axis
 * unitless, because a unit is a claim about what the numbers mean and only the
 * expression can support it.
 *
 * Aggregation operators and the `_over_time` functions that return one of the
 * samples keep the metric's unit; so does the change in a counter. Everything
 * else either produces a different quantity (a count, a ratio, a variance, a
 * boolean) or is something this has not reasoned about, and both are better
 * unlabelled than labelled wrong.
 */
const UNIT_PRESERVING_FUNCTIONS = new Set([
  "sum",
  "min",
  "max",
  "avg",
  "by",
  "without",
  "abs",
  "ceil",
  "floor",
  "round",
  "clamp",
  "clamp_max",
  "clamp_min",
  "label_replace",
  "label_join",
  "topk",
  "bottomk",
  "sort",
  "sort_desc",
  "sort_by_label",
  "sort_by_label_desc",
  "quantile",
  "min_over_time",
  "max_over_time",
  "avg_over_time",
  "sum_over_time",
  "last_over_time",
  "quantile_over_time",
  "increase",
  "delta",
  "idelta",
]);

/** The functions that divide a quantity by time. */
const RATE_FUNCTIONS = new Set(["rate", "irate", "deriv"]);

/**
 * Blanks string literals and comments in one left-to-right pass, so neither
 * can be read as the other. Scanning for comments first lets a `#` inside a
 * label value swallow the rest of the query, which once left `a_bytes{note="#"}
 * > bool 0` looking like a plain metric and gave a dimensionless 0/1 a bytes
 * axis. PromQL strings come in three quotes; only the first two take
 * backslash escapes.
 */
function withoutStringsAndComments(query: string): string {
  let out = "";
  let quote: string | undefined;
  for (let index = 0; index < query.length; index += 1) {
    const char = query[index];
    if (quote) {
      if (char === "\\" && quote !== "`") {
        index += 1;
        continue;
      }
      if (char === quote) quote = undefined;
      continue;
    }
    if (char === '"' || char === "'" || char === "`") {
      quote = char;
      out += '""';
      continue;
    }
    if (char === "#") {
      while (index < query.length && query[index] !== "\n") index += 1;
      out += " ";
      continue;
    }
    out += char;
  }
  return out;
}

/**
 * The unit is what every metric in the expression states through its name, as
 * the producer's selector inventory lists them, and it only survives the
 * operations that leave that meaning intact.
 *
 * `sum(x_bytes)` is bytes. `rate(x_bytes[5m])` is bytes per second, and a rate
 * of seconds is a ratio with no unit worth printing. Everything else is barred
 * rather than guessed: arithmetic and comparisons produce a quantity the
 * metric's name no longer describes (`x_bytes > bool 0` is a 0 or a 1,
 * `stdvar(x_bytes)` is bytes squared), a set operator mixes two expressions
 * whose units may differ, `atan2` returns an angle, a second rate divides by
 * time twice, and an unlisted function is simply unknown. Strings, comments, label matchers and
 * range selectors are blanked before the expression is read, so nothing
 * inside them can be mistaken for an operator or a call.
 */
export function metricsUnitForExpression(
  query: string,
  selectors: readonly InvestigationMetricsSelector[],
): string {
  if (selectors.length === 0) return "";
  const units = new Set(
    selectors.map((selector) => metricUnitSuffix(selector.metric)),
  );
  if (units.size !== 1) return "";
  const [unit] = units;
  if (!unit) return "";
  const expression = withoutStringsAndComments(query)
    .replace(/\{[^{}]*\}/g, "")
    .replace(/\[[^\]]*\]/g, "");
  // Arithmetic, comparison and the set operators all produce something the
  // metric's name no longer describes, or mix two things that disagree.
  if (/[-+/*%^]|[<>!=]=|[<>]|\b(?:bool|and|or|unless|atan2)\b/.test(expression)) {
    return "";
  }
  // An aggregation may put its grouping clause before the parenthesis, as in
  // `count by (pod) (...)`, so the name is not always adjacent to it.
  const functions = [
    ...expression.matchAll(
      /([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:(?:by|without)\s*\([^()]*\)\s*)?\(/g,
    ),
  ]
    .map((match) => match[1])
    .filter((name) => !selectors.some((selector) => selector.metric === name));
  const rates = functions.filter((name) => RATE_FUNCTIONS.has(name)).length;
  if (rates > 1) return "";
  for (const name of functions) {
    if (RATE_FUNCTIONS.has(name)) continue;
    if (!UNIT_PRESERVING_FUNCTIONS.has(name)) return "";
  }
  if (rates === 1) return unit === "bytes" ? "bytes/s" : "";
  return unit;
}

export function metricsDomain(
  data: Pick<InvestigationMetricsEvidence, "mode" | "start" | "end">,
): { start: number; end: number } | undefined {
  if (data.mode !== "range" || !data.start || !data.end) return undefined;
  const start = Date.parse(data.start) / 1000;
  const end = Date.parse(data.end) / 1000;
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start)
    return undefined;
  return { start, end };
}

function refKey(ref: {
  kind: string;
  group?: string;
  namespace?: string;
  name: string;
}): string {
  return [
    ref.kind.toLowerCase(),
    (ref.group ?? "").toLowerCase(),
    ref.namespace ?? "",
    ref.name,
  ].join("/");
}

function looseKey(ref: {
  kind: string;
  namespace?: string;
  name: string;
}): string {
  return [ref.kind.toLowerCase(), ref.namespace ?? "", ref.name].join("/");
}

/**
 * A change names its resource exactly when it carries an apiVersion. Helm
 * history rows and older stores omit it, so those match on kind, namespace
 * and name alone, which is unambiguous only against the subject and its
 * producer-established relatives.
 */
function changeMatchesRelative(
  change: IssueRecentChange,
  strictKeys: ReadonlySet<string>,
  looseKeys: ReadonlySet<string>,
): boolean {
  if (change.apiVersion) {
    return strictKeys.has(
      refKey({
        kind: change.kind,
        group: apiVersionToGroup(change.apiVersion),
        namespace: change.namespace,
        name: change.name,
      }),
    );
  }
  return looseKeys.has(looseKey(change));
}

function sameResource(
  left: DiagnosisResourceRef,
  right: DiagnosisResourceRef,
): boolean {
  return refKey(left) === refKey(right);
}

/**
 * The resources a change may be recorded against and still count as a change
 * to the chart's subject: the subject itself and the relatives producers named
 * in the same turn (the configuration it consumes, and the pods and ReplicaSets
 * diagnosis rows identified). Name prefixes never qualify.
 */
function producerRelatives(
  groups: readonly InvestigationEvidenceGroup[],
  subject: DiagnosisResourceRef,
  turnIndex: number,
): DiagnosisResourceRef[] {
  const relatives: DiagnosisResourceRef[] = [subject];
  for (const group of groups) {
    for (const observation of group.observations) {
      if (
        observation.source.turnIndex !== turnIndex ||
        observation.relevance === "broader"
      ) {
        continue;
      }
      const { data } = observation;
      switch (data.type) {
        case "resource": {
          const ref: DiagnosisResourceRef = {
            kind: data.resource.kind,
            group: apiVersionToGroup(data.resource.apiVersion) || undefined,
            namespace: data.resource.metadata.namespace,
            name: data.resource.metadata.name,
          };
          if (!sameResource(ref, subject)) break;
          const uses = data.resourceContext?.uses;
          relatives.push(...(uses?.configMaps ?? []), ...(uses?.secrets ?? []));
          if (data.resourceContext?.owner) {
            relatives.push(data.resourceContext.owner);
          }
          break;
        }
        case "startup":
          if (data.subject) relatives.push(data.subject);
          break;
        case "logs":
          if (data.namespace) {
            relatives.push({
              kind: "Pod",
              namespace: data.namespace,
              name: data.pod,
            });
          }
          break;
        case "crash":
          if (data.namespace) {
            for (const pod of data.crash.pods) {
              relatives.push({
                kind: "Pod",
                namespace: data.namespace,
                name: pod,
              });
            }
          }
          break;
        default:
          break;
      }
    }
  }
  return relatives;
}

/**
 * Changes Radar recorded for the chart's subject inside the chart window,
 * paired with whether that lookup was possible at all. An empty marker list
 * means "none were recorded" only when `checked` is true; otherwise the chart
 * had no subject to look changes up for, or the turn captured no changes to
 * look through, and the chart must not claim a clean window.
 */
export type MetricsChangeCoverage = {
  markers: ChartAnnotation[];
  /** False when no change lookup was possible, so an empty list proves nothing. */
  checked: boolean;
};

export function metricsChangeCoverage(
  groups: readonly InvestigationEvidenceGroup[],
  observation: InvestigationEvidenceObservation,
): MetricsChangeCoverage {
  const { data } = observation;
  // A broader chart's subject is a neighbour; the turn's non-broader relatives
  // and changes all belong to the target, so marking them there would
  // attribute the target's changes to the neighbour.
  if (
    data.type !== "metrics" ||
    !data.subject ||
    observation.relevance === "broader"
  ) {
    return { markers: [], checked: false };
  }
  const domain = metricsDomain(data);
  if (!domain) return { markers: [], checked: false };
  const turnIndex = observation.source.turnIndex;
  const relatives = producerRelatives(groups, data.subject, turnIndex);
  const strictKeys = new Set(relatives.map(refKey));
  const looseKeys = new Set(relatives.map(looseKey));
  const seen = new Set<string>();
  const markers: ChartAnnotation[] = [];
  let checked = false;
  for (const group of groups) {
    for (const candidate of group.observations) {
      if (
        candidate.source.turnIndex !== turnIndex ||
        candidate.relevance === "broader"
      ) {
        continue;
      }
      // An authoritative empty change window is filed as a receipt, and counts
      // as a check just as a change list does. A window the producer could not
      // vouch for becomes a limitation instead, never a receipt, so it leaves
      // `checked` false.
      if (
        candidate.data.type === "receipt" &&
        candidate.data.checked === "changes"
      ) {
        checked = true;
        continue;
      }
      if (candidate.data.type !== "changes") continue;
      checked = true;
      for (const change of candidate.data.changes) {
        const timestamp = Date.parse(change.timestamp) / 1000;
        if (
          !Number.isFinite(timestamp) ||
          timestamp < domain.start ||
          timestamp > domain.end ||
          !changeMatchesRelative(change, strictKeys, looseKeys)
        ) {
          continue;
        }
        const identity = `${change.apiVersion ?? ""}|${change.kind}|${change.namespace ?? ""}|${change.name}|${change.timestamp}`;
        if (seen.has(identity)) continue;
        seen.add(identity);
        markers.push({
          timestamp,
          label: `${displayKind(change.kind)} ${change.name}`,
          kind: "change",
        });
      }
    }
  }
  return {
    markers: markers.sort((left, right) => left.timestamp - right.timestamp),
    checked,
  };
}

/**
 * Changes Radar recorded for the chart's subject inside the chart window.
 * Callers that need to tell "none recorded" from "not looked up" want
 * {@link metricsChangeCoverage} instead.
 */
export function metricsChangeMarkers(
  groups: readonly InvestigationEvidenceGroup[],
  observation: InvestigationEvidenceObservation,
): ChartAnnotation[] {
  return metricsChangeCoverage(groups, observation).markers;
}
