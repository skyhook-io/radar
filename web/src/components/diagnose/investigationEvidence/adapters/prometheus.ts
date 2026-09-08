import { displayKind } from "@skyhook-io/k8s-ui";
import type { TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import type { DiagnosisResourceRef } from "../../diagnoseEvidenceTypes";
import { metricsUnitForExpression } from "../../investigationMetrics";
import type {
  InvestigationAlertInstance,
  InvestigationAlertRule,
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationEvidenceTarget,
  InvestigationMetricsEvidence,
  InvestigationMetricsSelector,
} from "../types";
import {
  type ProjectionBuilder,
  evidenceTierForRelevance,
  invalidPayload,
  nonEmptyString,
  nonNegativeInteger,
  parseJSON,
  record,
  scopeFromArgs,
  stableHash,
} from "../builder";

function stringRecord(value: unknown): Record<string, string> | undefined {
  const candidate = record(value);
  if (!candidate) return undefined;
  return Object.values(candidate).every((item) => typeof item === "string")
    ? (candidate as Record<string, string>)
    : undefined;
}

/**
 * Pods a producer has already tied to the investigated workload in this
 * projection: the streams diagnose or a workload-logs read resolved for it,
 * its crash candidates, and its Pod startup blockers. A pod name matched
 * against this set is producer-established membership, never inference from
 * a name prefix, which a sibling such as `api-worker` would also satisfy.
 */
function producerEstablishedTargetPods(
  builder: ProjectionBuilder,
): Set<string> {
  const pods = new Set<string>(builder.establishedTargetPods);
  const namespace = builder.target.namespace;
  if (!namespace) return pods;
  for (const group of builder.groups) {
    for (const observation of group.observations) {
      if (observation.relevance === "broader") continue;
      const data = observation.data;
      if (data.type === "logs" && data.namespace === namespace) {
        pods.add(data.pod);
      } else if (data.type === "crash" && data.namespace === namespace) {
        for (const pod of data.crash.pods) pods.add(pod);
      } else if (data.type === "startup" && data.blocker.kind === "Pod") {
        // The blocker names the pods it holds whether the card merged them or
        // not; the subject is only set when there is exactly one, so reading
        // it alone lost the whole set the moment a second pod appeared.
        for (const pod of data.pods ?? []) pods.add(pod);
      }
    }
  }
  return pods;
}

// kube-state-metrics label names for the owning workload.
const WORKLOAD_LABEL_BY_KIND: Readonly<Record<string, string>> = {
  deployment: "deployment",
  statefulset: "statefulset",
  daemonset: "daemonset",
  rollout: "rollout",
  job: "job_name",
  cronjob: "cronjob",
};

/**
 * Whether a Prometheus label set names the investigated resource: the target
 * namespace plus a label identifying it directly, or a `pod` label naming a
 * pod a producer already tied to it.
 */
function labelsNameTarget(
  target: InvestigationEvidenceTarget,
  labels: Record<string, string>,
  targetPods: ReadonlySet<string>,
): boolean {
  if (!target.namespace || labels.namespace !== target.namespace) return false;
  const kind = target.kind.toLowerCase();
  if (kind === "pod") return labels.pod === target.name;
  const workloadLabel = WORKLOAD_LABEL_BY_KIND[kind];
  if (workloadLabel && labels[workloadLabel] === target.name) return true;
  if (
    labels.workload === target.name &&
    (labels.workload_type === undefined ||
      labels.workload_type.toLowerCase() === kind)
  ) {
    return true;
  }
  return labels.pod !== undefined && targetPods.has(labels.pod);
}

/**
 * The label set places the instance in another namespace outright. Only a
 * namespaced target can be excluded this way; for a cluster-scoped target
 * every namespace label is "different" and proves nothing.
 */
function labelsPlaceElsewhere(
  target: InvestigationEvidenceTarget,
  labels: Record<string, string>,
): boolean {
  return (
    Boolean(target.namespace) &&
    nonEmptyString(labels.namespace) &&
    labels.namespace !== target.namespace
  );
}

function alertRule(value: unknown):
  | {
      rule: InvestigationAlertRule;
      instances: Omit<InvestigationAlertInstance, "namesTarget">[];
      annotations: Record<string, string>;
    }
  | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.group) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.type) ||
    typeof candidate.query !== "string"
  ) {
    return undefined;
  }
  for (const field of ["state", "health"] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  const labels =
    candidate.labels === undefined ? {} : stringRecord(candidate.labels);
  const annotations =
    candidate.annotations === undefined
      ? {}
      : stringRecord(candidate.annotations);
  if (!labels || !annotations) return undefined;
  const alertsRaw = candidate.alerts === undefined ? [] : candidate.alerts;
  if (!Array.isArray(alertsRaw)) return undefined;
  const instances: Omit<InvestigationAlertInstance, "namesTarget">[] = [];
  for (const raw of alertsRaw) {
    const instance = record(raw);
    const instanceLabels =
      instance?.labels === undefined ? {} : stringRecord(instance.labels);
    if (
      !instance ||
      !nonEmptyString(instance.state) ||
      !instanceLabels ||
      (instance.activeAt !== undefined &&
        typeof instance.activeAt !== "string") ||
      (instance.value !== undefined && typeof instance.value !== "string")
    ) {
      return undefined;
    }
    instances.push({
      state: instance.state,
      activeAt: instance.activeAt as string | undefined,
      value: instance.value as string | undefined,
      labels: instanceLabels,
    });
  }
  return {
    rule: {
      group: candidate.group,
      name: candidate.name,
      type: candidate.type,
      state: candidate.state as string | undefined,
      health: candidate.health as string | undefined,
      query: candidate.query,
      labels,
    },
    instances,
    annotations,
  };
}

function targetIdentity(target: InvestigationEvidenceTarget): string {
  return `${displayKind(target.kind)} ${target.namespace ? `${target.namespace}/` : ""}${target.name}`;
}

export function adaptPrometheusRules(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (
    !value ||
    !Array.isArray(value.rules) ||
    typeof value.count !== "number" ||
    value.count !== value.rules.length ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean") ||
    (value.note !== undefined && typeof value.note !== "string")
  ) {
    invalidPayload(builder, source);
    return;
  }
  const rules = value.rules
    .map(alertRule)
    .filter((item): item is NonNullable<typeof item> => Boolean(item));
  if (rules.length !== value.rules.length) {
    invalidPayload(builder, source);
    return;
  }
  const args = record(source.args ? parseJSON(source.args) : undefined);
  const stateFilter = nonEmptyString(args?.state)
    ? args.state.toLowerCase()
    : undefined;
  const typeFilter = nonEmptyString(args?.type)
    ? args.type.toLowerCase()
    : undefined;
  const truncated = value.truncated === true;
  if (truncated) {
    builder.limit(
      source,
      "Alert rules",
      `The rule list was capped, so additional matching rules may exist${nonEmptyString(value.note) ? `; ${value.note}` : ""}.`,
      "truncated",
    );
  }
  const identity = targetIdentity(builder.target);
  const targetPods = producerEstablishedTargetPods(builder);
  // Rule names are not unique within a group, and group names are unique
  // only per rule file, which the producer does not return. The expression
  // and static labels distinguish rules; identical rows within one response
  // cannot be told apart at all, so they stay scoped to this read rather
  // than inherit each other's history in whatever order they arrive.
  const ruleDefinition = (rule: InvestigationAlertRule) =>
    `alerts:${rule.group}:${rule.name}:${stableHash(
      JSON.stringify([rule.query, Object.entries(rule.labels).sort()]),
    )}`;
  const definitionCounts = new Map<string, number>();
  for (const { rule } of rules) {
    const definition = ruleDefinition(rule);
    definitionCounts.set(
      definition,
      (definitionCounts.get(definition) ?? 0) + 1,
    );
  }
  const seenRuleIdentities = new Map<string, number>();
  let namesTarget = false;
  let healthGap = false;
  let alertingCount = 0;
  let everyInstanceElsewhere = true;
  for (const { rule, instances: rawInstances, annotations } of rules) {
    // Recording rules carry no state and never describe a resource.
    if (rule.type.toLowerCase() !== "alerting") continue;
    alertingCount += 1;
    const instances = rawInstances.map((instance) => ({
      ...instance,
      namesTarget: labelsNameTarget(
        builder.target,
        instance.labels,
        targetPods,
      ),
    }));
    if (
      instances.length === 0 ||
      instances.some(
        (instance) => !labelsPlaceElsewhere(builder.target, instance.labels),
      )
    ) {
      everyInstanceElsewhere = false;
    }
    const definition = ruleDefinition(rule);
    const ambiguous = (definitionCounts.get(definition) ?? 0) > 1;
    const ordinal = seenRuleIdentities.get(definition) ?? 0;
    seenRuleIdentities.set(definition, ordinal + 1);
    const ruleIdentity = ambiguous
      ? `${definition}:${source.id}:${ordinal}`
      : definition;
    const previousRelevance = ambiguous
      ? undefined
      : builder.latestRelevance("alerts", ruleIdentity);
    const relevance: InvestigationEvidenceRelevance = instances.some(
      (instance) => instance.namesTarget,
    )
      ? "target"
      : labelsNameTarget(builder.target, rule.labels, targetPods)
        ? "producer-related"
        : // The same rule read again with no instances left has resolved for
          // the target it named before; keep that provenance so the
          // resolution stays visible instead of the stale firing card.
          instances.length === 0 &&
            previousRelevance !== undefined &&
            previousRelevance !== "broader"
          ? previousRelevance
          : "broader";
    if (relevance !== "broader") namesTarget = true;
    const state = (rule.state ?? "").toLowerCase();
    const active = state === "firing" || state === "pending";
    const severity = (rule.labels.severity ?? "").toLowerCase();
    const targetInstances = instances.filter(
      (instance) => instance.namesTarget,
    ).length;
    builder.observe(ruleIdentity, "alerts", source, {
      tier: evidenceTierForRelevance(
        active ? "supporting" : "context",
        relevance,
      ),
      relevance,
      tone:
        state === "firing"
          ? severity === "critical"
            ? "error"
            : "alert"
          : state === "pending"
            ? "warning"
            : "neutral",
      title: `${rule.name}${state ? ` ${state}` : ""}`,
      summary:
        instances.length === 0
          ? `No active instances · ${rule.group}`
          : `${instances.length} active instance${instances.length === 1 ? "" : "s"}${
              targetInstances > 0
                ? `, ${targetInstances} naming ${identity}`
                : ""
            } · ${rule.group}`,
      data: { type: "alerts", rule, instances, annotations },
    });
    const health = (rule.health ?? "").toLowerCase();
    if (health === "err") {
      healthGap = true;
      builder.limit(
        source,
        `Alert rule ${rule.name}`,
        "Prometheus reported an evaluation error for this rule, so its state may be stale or missing.",
        "error",
      );
    } else if (health !== "ok") {
      // Anything but a reported "ok" evaluation, including an unrecognized
      // value, is an unevaluated rule for the purpose of a negative.
      healthGap = true;
      builder.limit(
        source,
        `Alert rule ${rule.name}`,
        "Prometheus has not reported a successful evaluation for this rule, so its state is unknown.",
        "unknown",
      );
    }
  }
  // A negative is only as strong as the query: an active-state filter over
  // every alerting rule (a name or group filter never looked at the rest), a
  // complete list, every rule evaluated, and every returned instance placed
  // in another namespace outright. An instance this projection merely fails
  // to recognize is not evidence of absence.
  if (
    source.confirmedSuccess &&
    !truncated &&
    !namesTarget &&
    !healthGap &&
    typeFilter !== "record" &&
    !nonEmptyString(args?.name) &&
    !nonEmptyString(args?.group) &&
    (stateFilter === "firing" || stateFilter === "pending") &&
    (alertingCount === 0 || everyInstanceElsewhere)
  ) {
    const filter = `state=${stateFilter}`;
    builder.observe(`alerts:receipt:${filter}`, "receipt", source, {
      tier: "checked",
      relevance: "target",
      tone: "neutral",
      title: `No ${stateFilter} alerts matched this ${displayKind(builder.target.kind)}`,
      summary: identity,
      data: {
        type: "receipt",
        checked: "alerts",
        scope: identity,
        message:
          alertingCount === 0
            ? `0 alerting rules returned (filter: ${filter}).`
            : `${alertingCount} alerting rule${alertingCount === 1 ? "" : "s"} returned (filter: ${filter}), every instance in another namespace; none names ${identity}.`,
      },
    });
  }
}

function timeSeries(value: unknown): TimeSeries | undefined {
  const item = record(value);
  if (!item || !Array.isArray(item.dataPoints)) return undefined;
  const labels = record(item.labels) ?? {};
  if (Object.values(labels).some((label) => typeof label !== "string")) {
    return undefined;
  }
  const dataPoints: TimeSeries["dataPoints"] = [];
  for (const raw of item.dataPoints) {
    const point = record(raw);
    if (!point || typeof point.timestamp !== "number") return undefined;
    if (
      point.value !== undefined &&
      point.value !== null &&
      typeof point.value !== "number"
    ) {
      return undefined;
    }
    dataPoints.push({
      timestamp: point.timestamp,
      value: typeof point.value === "number" ? point.value : null,
    });
  }
  return { labels: labels as Record<string, string>, dataPoints };
}

function metricsSelector(
  value: unknown,
): InvestigationMetricsSelector | undefined {
  const item = record(value);
  if (!item || typeof item.metric !== "string" || !Array.isArray(item.matchers))
    return undefined;
  const matchers: InvestigationMetricsSelector["matchers"] = [];
  for (const raw of item.matchers) {
    const matcher = record(raw);
    if (
      !matcher ||
      typeof matcher.label !== "string" ||
      typeof matcher.op !== "string" ||
      typeof matcher.value !== "string"
    ) {
      return undefined;
    }
    matchers.push({
      label: matcher.label,
      op: matcher.op,
      value: matcher.value,
    });
  }
  return { metric: item.metric, matchers };
}

/**
 * How one selector relates to the investigated resource. "target": it names
 * the target itself (an identity label, or pods a producer established as
 * the target's own). "related": it mentions the target's namespace and
 * something in it the target cannot be proved to own (a pod named by prefix,
 * a sibling workload, an owner series). "namespace": only the namespace.
 * "foreign": another namespace or none.
 */
type SelectorVerdict = "target" | "related" | "namespace" | "foreign";

const VERDICT_RANK: Record<SelectorVerdict, number> = {
  foreign: 3,
  target: 2,
  related: 1,
  namespace: 0,
};

/**
 * A regex matcher names the target only in the exact forms the plan admits,
 * with the name's regex metacharacters (a dot in a Kubernetes name) escaped
 * or absent. "api-?.*" also selects "apiworker-…" and an unescaped "api.v2"
 * also selects "api-v2", so neither may claim the target.
 */
function regexNamesExactly(
  value: string,
  name: string,
  suffix: string,
): boolean {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return value === `${escaped}${suffix}`;
}

/**
 * The pod names an exact-set regex `^(a|b)$` (or the unanchored `a|b`, which
 * Prometheus anchors) lists. A dot must be escaped to count: `^(api.v2-0)$`
 * also selects `api-v2-0`, so it names more than the pod it appears to and
 * cannot prove membership. Undefined for any other shape.
 */
function exactPodSet(value: string): string[] | undefined {
  const body =
    value.startsWith("^(") && value.endsWith(")$") ? value.slice(2, -2) : value;
  if (body === "" || /[^A-Za-z0-9\-|\\.]/.test(body)) return undefined;
  // Every dot has to arrive escaped; an unescaped one is a wildcard.
  if (/(^|[^\\])\./.test(body.replace(/\\\\/g, ""))) return undefined;
  const names = body.split("|").map((name) => name.replace(/\\\./g, "."));
  return names.every((name) => /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(name))
    ? names
    : undefined;
}

/**
 * Whether a workload identity label names the target: the label
 * kube-state-metrics uses for the kind (`deployment`, `job_name`, …) or a
 * generic `workload` label, with the exact name.
 */
function identityLabelNamesTarget(
  target: InvestigationEvidenceTarget,
  label: string,
  value: string,
  exact: boolean,
): boolean {
  const kind = target.kind.toLowerCase();
  const workloadLabel = WORKLOAD_LABEL_BY_KIND[kind];
  // `deployment=api` states the kind in the label itself; `workload=api` does
  // not, so a `workload_type` naming another kind vetoes it in
  // classifySelector, which is the only place that sees the whole selector.
  if (label !== workloadLabel && label !== "workload") return false;
  return exact
    ? value === target.name
    : regexNamesExactly(value, target.name, "");
}

function classifyMatcher(
  target: InvestigationEvidenceTarget,
  matcher: InvestigationMetricsSelector["matchers"][number],
  establishedPods: ReadonlySet<string>,
): SelectorVerdict {
  const { label, op, value } = matcher;
  const kind = target.kind.toLowerCase();
  if (label === "pod") {
    if (kind === "pod") {
      if (op === "=") return value === target.name ? "target" : "related";
      if (op === "=~")
        return regexNamesExactly(value, target.name, "") ? "target" : "related";
      return "related";
    }
    if (op === "=") return establishedPods.has(value) ? "target" : "related";
    if (op === "=~") {
      const names = exactPodSet(value);
      if (names && names.every((name) => establishedPods.has(name))) {
        return "target";
      }
      return "related";
    }
    return "related";
  }
  if (label === "namespace" || label === "container") return "namespace";
  if (op === "=" && identityLabelNamesTarget(target, label, value, true)) {
    return "target";
  }
  if (op === "=~" && identityLabelNamesTarget(target, label, value, false)) {
    return "target";
  }
  // Owner series (kube_pod_owner, kube_replicaset_owner) and every other
  // label in the namespace: about the namespace's things, not proved to be
  // the target's.
  return "related";
}

function classifySelector(
  target: InvestigationEvidenceTarget,
  selector: InvestigationMetricsSelector,
  establishedPods: ReadonlySet<string>,
): SelectorVerdict {
  // `workload` is generic, so a `workload_type` naming another kind means
  // the series is about a different workload that happens to share a name.
  // A regex counts when it names an exact set, the same way the namespace
  // matcher below does: `workload_type=~"statefulset"` excludes a Deployment
  // as plainly as `=` does, and reading only `=` let a sibling's chart take
  // the target's identity. A set that includes the target kind is no conflict,
  // and an inexact regex says nothing either way.
  const workloadType = selector.matchers.find(
    (matcher) =>
      matcher.label === "workload_type" &&
      (matcher.op === "=" || matcher.op === "=~"),
  );
  const workloadTypeNames =
    workloadType === undefined
      ? undefined
      : workloadType.op === "="
        ? [workloadType.value]
        : exactPodSet(workloadType.value);
  const workloadTypeConflicts =
    workloadTypeNames !== undefined &&
    workloadTypeNames.length > 0 &&
    !workloadTypeNames.some(
      (name) => name.toLowerCase() === target.kind.toLowerCase(),
    );
  // Prometheus anchors regex matchers, so namespace=~"shop" is exact too.
  const inNamespace = selector.matchers.some(
    (matcher) =>
      matcher.label === "namespace" &&
      ((matcher.op === "=" && matcher.value === target.namespace) ||
        (matcher.op === "=~" &&
          regexNamesExactly(matcher.value, target.namespace ?? "", ""))),
  );
  if (!inNamespace) return "foreign";
  let verdict: SelectorVerdict = "namespace";
  for (const matcher of selector.matchers) {
    if (workloadTypeConflicts && matcher.label === "workload") continue;
    const candidate = classifyMatcher(target, matcher, establishedPods);
    if (candidate === "foreign") return "foreign";
    if (VERDICT_RANK[candidate] > VERDICT_RANK[verdict]) verdict = candidate;
  }
  return verdict;
}

/**
 * A metrics result is about the target only when every selector names it:
 * the target namespace plus pods a producer established as the target's own
 * or an identity label with its exact name. A selector that only mentions
 * something in the namespace the target cannot be proved to own (a pod
 * prefix, a sibling, an owner join) makes the result producer-related: kept,
 * not promoted, no change markers. A namespace-only query, a bare or foreign
 * selector, or an inventory the producer could not extract is broader.
 */
export function metricsScope(
  target: InvestigationEvidenceTarget,
  selectors: readonly InvestigationMetricsSelector[],
  selectorsUnknown: boolean,
  establishedPods: ReadonlySet<string> = new Set(),
): InvestigationEvidenceRelevance {
  if (selectorsUnknown || selectors.length === 0 || !target.namespace) {
    return "broader";
  }
  let allTarget = true;
  let anyRelated = false;
  for (const selector of selectors) {
    const verdict = classifySelector(target, selector, establishedPods);
    if (verdict === "foreign") return "broader";
    if (verdict !== "target") allTarget = false;
    if (verdict === "related") anyRelated = true;
  }
  if (allTarget) return "target";
  return anyRelated ? "producer-related" : "broader";
}

function metricsWindowLabel(data: {
  mode: "range" | "instant";
  start?: string;
  end?: string;
  step?: string;
}): string | undefined {
  if (data.mode !== "range" || !data.start || !data.end) return undefined;
  const startMs = Date.parse(data.start);
  const endMs = Date.parse(data.end);
  if (!Number.isFinite(startMs) || !Number.isFinite(endMs) || endMs <= startMs)
    return undefined;
  const minutes = Math.round((endMs - startMs) / 60_000);
  const window =
    minutes >= 60 * 24 * 2
      ? `${Math.round(minutes / (60 * 24))}d`
      : minutes >= 120
        ? `${Math.round(minutes / 60)}h`
        : `${minutes}m`;
  return data.step
    ? `${window} window · ${data.step} step`
    : `${window} window`;
}

/**
 * One half of a Go↔TS contract: `diagnoseMetricsCategories` in
 * internal/mcp/tools_diagnose_metrics.go decides which categories the producer
 * captures, and a series whose category is missing here is discarded. Change
 * both together.
 */
const DIAGNOSE_METRICS_LABELS: Record<string, string> = {
  cpu: "CPU usage",
  memory: "Memory working set",
  restarts: "Restarts",
};

/**
 * The vitals `diagnose` captured inside the same call: one chart per category
 * over the exact pods the bundle covers. They take the bundle's relevance, so a
 * neighbour's diagnose never charts as evidence for the target, and they carry
 * the diagnosed resource as their subject so recorded changes can be marked
 * on them. An absent field means Prometheus was not available or the read was
 * not permitted, which is not a collection failure the producer reported.
 */
export function addDiagnoseMetrics(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  raw: unknown,
  subject: DiagnosisResourceRef,
  relevance: InvestigationEvidenceRelevance,
): void {
  if (raw === undefined) return;
  const value = record(raw);
  const window = record(value?.window);
  if (
    !value ||
    !window ||
    !nonEmptyString(window.start) ||
    !nonEmptyString(window.end) ||
    !nonEmptyString(window.step) ||
    !(Date.parse(window.end) > Date.parse(window.start)) ||
    !nonNegativeInteger(value.pods) ||
    (value.partial !== undefined && typeof value.partial !== "boolean") ||
    (value.omittedPods !== undefined &&
      !nonNegativeInteger(value.omittedPods)) ||
    (value.error !== undefined && typeof value.error !== "string") ||
    (value.coverage !== undefined &&
      value.coverage !== "ksm_history" &&
      value.coverage !== "current_pods" &&
      value.coverage !== "none") ||
    (value.observedPods !== undefined &&
      !nonNegativeInteger(value.observedPods)) ||
    (value.scopeError !== undefined && typeof value.scopeError !== "string") ||
    !Array.isArray(value.series)
  ) {
    invalidPayload(builder, source, "Workload metrics");
    return;
  }
  const scope = scopeFromArgs(source);
  const partial = value.partial === true;
  const coverage = value.coverage as
    "ksm_history" | "current_pods" | "none" | undefined;
  const observedPods =
    typeof value.observedPods === "number" ? value.observedPods : undefined;
  // The pods the chart covers: with ownership history every pod
  // kube-state-metrics attributed in the window, otherwise the pods running
  // at collection time, which a rollout during the window can miss.
  const podsLabel =
    coverage === "ksm_history" && observedPods !== undefined && observedPods > 0
      ? `${observedPods} pod${observedPods === 1 ? "" : "s"} in window`
      : partial
        ? `first ${value.pods} of ${typeof value.omittedPods === "number" ? value.pods + value.omittedPods : "the"} current pods`
        : `${value.pods} current pod${value.pods === 1 ? "" : "s"}`;
  const windowLabel = metricsWindowLabel({
    mode: "range",
    start: window.start,
    end: window.end,
    step: window.step,
  });
  for (const rawEntry of value.series) {
    const entry = record(rawEntry);
    const rawSeries = entry?.series;
    const series = Array.isArray(rawSeries)
      ? rawSeries
          .map(timeSeries)
          .filter((item): item is TimeSeries => Boolean(item))
      : undefined;
    if (
      !entry ||
      !nonEmptyString(entry.category) ||
      !Object.hasOwn(DIAGNOSE_METRICS_LABELS, entry.category) ||
      !nonEmptyString(entry.query) ||
      !nonEmptyString(entry.unit) ||
      !Array.isArray(rawSeries) ||
      !series ||
      series.length !== rawSeries.length
    ) {
      invalidPayload(builder, source, "Workload metrics");
      continue;
    }
    const label = DIAGNOSE_METRICS_LABELS[entry.category];
    const data: InvestigationMetricsEvidence = {
      type: "metrics",
      origin: "diagnose",
      query: entry.query,
      mode: "range",
      start: window.start,
      end: window.end,
      step: window.step,
      unit: entry.unit,
      label,
      series,
      truncated: false,
      subject,
      pods: value.pods,
      partial,
      ...(coverage ? { coverage } : {}),
      ...(observedPods !== undefined ? { observedPods } : {}),
    };
    builder.observe(
      `metrics:diagnose:${subject.group ?? ""}:${subject.kind}:${subject.namespace ?? ""}:${subject.name}:${entry.category}`,
      "metrics",
      source,
      {
        tier: evidenceTierForRelevance("supporting", relevance),
        relevance,
        tone: "neutral",
        title: `${label} · ${scope}`,
        summary: [
          series.length === 0 ? "No samples in the window" : podsLabel,
          windowLabel,
          partial ? "partial pod set" : undefined,
        ]
          .filter((part): part is string => Boolean(part))
          .join(" · "),
        data,
      },
    );
  }
  if (nonEmptyString(value.error)) {
    builder.limit(source, "Workload metrics", value.error, "error");
  }
  if (nonEmptyString(value.scopeError)) {
    builder.limit(
      source,
      "Workload metrics",
      `Pod ownership history could not be read (${value.scopeError}); the charts cover the pods running at collection time.`,
      "unknown",
    );
  }
}

export function adaptQueryPrometheus(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const mode = value?.type;
  if (
    !value ||
    !nonEmptyString(value.query) ||
    (mode !== "range" && mode !== "instant") ||
    !Array.isArray(value.series) ||
    !Array.isArray(value.selectors) ||
    (value.selectorsUnknown !== undefined &&
      typeof value.selectorsUnknown !== "boolean") ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean")
  ) {
    invalidPayload(builder, source, "Prometheus query");
    return;
  }
  const series = value.series
    .map(timeSeries)
    .filter((item): item is TimeSeries => Boolean(item));
  const selectors = value.selectors
    .map(metricsSelector)
    .filter((item): item is InvestigationMetricsSelector => Boolean(item));
  if (
    series.length !== value.series.length ||
    selectors.length !== value.selectors.length
  ) {
    invalidPayload(builder, source, "Prometheus query");
    return;
  }
  const selectorsUnknown = value.selectorsUnknown === true;
  const relevance = metricsScope(
    builder.target,
    selectors,
    selectorsUnknown,
    producerEstablishedTargetPods(builder),
  );
  const start = nonEmptyString(value.start) ? value.start : undefined;
  const end = nonEmptyString(value.end) ? value.end : undefined;
  const step = nonEmptyString(value.step) ? value.step : undefined;
  const note = nonEmptyString(value.note) ? value.note : undefined;
  if (value.truncated === true) {
    const summary = record(value.summary);
    const cardinality = record(summary?.labelCardinality);
    const widest = cardinality
      ? Object.entries(cardinality).find(
          ([, count]) => typeof count === "number",
        )
      : undefined;
    const parts = [
      typeof summary?.seriesCount === "number"
        ? `${summary.seriesCount} series`
        : undefined,
      typeof summary?.totalDataPoints === "number"
        ? `${summary.totalDataPoints} samples`
        : undefined,
      widest ? `${widest[1]} distinct ${widest[0]} values` : undefined,
    ].filter((part): part is string => Boolean(part));
    builder.limit(
      source,
      "Prometheus query",
      `The result of \`${value.query}\` was too large to keep${parts.length ? ` (${parts.join(", ")})` : ""}, so Radar cannot chart it.${note ? ` ${note}` : ""}`,
      "truncated",
    );
    return;
  }
  const windowLabel = metricsWindowLabel({ mode, start, end, step });
  // One metric names the card; the title is Radar's reading of the query,
  // never the agent's, so a wrong claim cannot become the card's identity.
  // A bare matcher block such as `{namespace="shop"}` names no metric.
  const metricNames = [
    ...new Set(
      selectors.flatMap((selector) =>
        selector.metric.trim() ? [selector.metric.trim()] : [],
      ),
    ),
  ];
  const title =
    metricNames.length === 1
      ? metricNames[0]
      : mode === "range"
        ? "Prometheus metrics"
        : "Prometheus values";
  const data: InvestigationMetricsEvidence = {
    type: "metrics",
    origin: "query",
    query: value.query,
    mode,
    start,
    end,
    step,
    unit: metricsUnitForExpression(value.query, selectors),
    series,
    truncated: false,
    note,
    selectors,
    selectorsUnknown,
    subject:
      relevance === "target"
        ? {
            kind: builder.target.kind,
            ...(builder.target.group ? { group: builder.target.group } : {}),
            namespace: builder.target.namespace,
            name: builder.target.name,
          }
        : undefined,
  };
  builder.observe(
    `metrics:${mode}:${value.query}:${start ?? ""}:${end ?? ""}:${step ?? ""}`,
    "metrics",
    source,
    {
      tier: evidenceTierForRelevance("supporting", relevance),
      relevance,
      tone: "neutral",
      title,
      summary: [
        metricNames.length === 1 ? "Prometheus" : undefined,
        series.length === 0 ? "No series matched" : `${series.length} series`,
        windowLabel,
      ]
        .filter((part): part is string => Boolean(part))
        .join(" · "),
      data,
    },
  );
}
