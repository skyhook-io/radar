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
  const base = metric.endsWith("_total")
    ? metric.slice(0, -"_total".length)
    : metric;
  if (base.endsWith("_bytes")) return "bytes";
  if (base.endsWith("_seconds")) return "seconds";
  return undefined;
}

// Functions whose result is a count, a flag or a time, whatever the input
// metric measured. Aggregations may put their grouping clause before the
// parenthesis: `count by (pod) (...)`.
const QUANTITY_DISCARDING_FUNCTIONS =
  /\b(?:count|count_values|count_over_time|absent|absent_over_time|present_over_time|changes|resets|timestamp)\s*(?:\(|by\b|without\b)/;

/**
 * The unit is what every metric in the expression states through its name,
 * as the producer's selector inventory lists them. Aggregations keep a unit;
 * a rate turns bytes into bytes per second and seconds into a ratio; a
 * division, or a function that counts or flags rather than measures, is
 * unitless; metrics that disagree, or say nothing, leave the axis unitless.
 * Matcher values and string literals are ignored when looking for operators,
 * so a label value containing "/" cannot demote the unit.
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
  const operators = query
    .replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g, '""')
    .replace(/\{[^{}]*\}/g, "")
    .replace(/\[[^\]]*\]/g, "");
  if (operators.includes("/") || QUANTITY_DISCARDING_FUNCTIONS.test(operators))
    return "";
  if (/\b(?:rate|irate|deriv)\s*\(/.test(operators)) {
    return unit === "bytes" ? "bytes/s" : "";
  }
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
 * taken only from the same turn's change observations that producers related
 * to the target. Each marker says a change was recorded at that instant and
 * nothing more.
 */
export function metricsChangeMarkers(
  groups: readonly InvestigationEvidenceGroup[],
  observation: InvestigationEvidenceObservation,
): ChartAnnotation[] {
  const { data } = observation;
  if (data.type !== "metrics" || !data.subject) return [];
  const domain = metricsDomain(data);
  if (!domain) return [];
  const turnIndex = observation.source.turnIndex;
  const relatives = producerRelatives(groups, data.subject, turnIndex);
  const strictKeys = new Set(relatives.map(refKey));
  const looseKeys = new Set(relatives.map(looseKey));
  const seen = new Set<string>();
  const markers: ChartAnnotation[] = [];
  for (const group of groups) {
    for (const candidate of group.observations) {
      if (
        candidate.data.type !== "changes" ||
        candidate.source.turnIndex !== turnIndex ||
        candidate.relevance === "broader"
      ) {
        continue;
      }
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
  return markers.sort((left, right) => left.timestamp - right.timestamp);
}
