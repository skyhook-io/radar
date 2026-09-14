import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { resourcePath } from "../../utils/navigation";
import {
  AreaChart,
  SeriesLegend,
  formatMetricValue,
  type ReferenceLine,
} from "@skyhook-io/k8s-ui/components/charts";
import { type PrometheusTimeRange } from "../../api/client";
import {
  useWorkloadMetrics,
  type WorkloadMetricPanel,
  type WorkloadRequestSource,
  type WorkloadMetrics,
} from "../../api/workloadMetrics";
import { latestWorkloadValue, workloadPodValues } from "./workloadMetricValues";

interface Props {
  kind: string;
  namespace: string;
  name: string;
  range: PrometheusTimeRange;
  cpuReferenceLines?: ReferenceLine[];
  memoryReferenceLines?: ReferenceLine[];
  restartLane?: ReactNode;
  nameMatchedCharts?: Partial<Record<'cpu' | 'memory', ReactNode>>;
}

export function WorkloadMetricsSection(props: Props) {
  const [source, setSource] = useState<WorkloadRequestSource | "">("");
  const { data, isLoading, isPlaceholderData, error } = useWorkloadMetrics(
    props.kind,
    props.namespace,
    props.name,
    props.range,
    source,
    true,
  );
  const sameResourceScope = data && ['memory', 'throttling'].every((key) => {
    const scope = data.history[key as 'memory' | 'throttling'];
    return !scope || (scope.mode === data.history.cpu?.mode && scope.reason === data.history.cpu?.reason);
  });
  const nameMatchedFallbacks = (['cpu', 'memory'] as const).filter((key) => {
    const panel = data?.panels[key];
    return (data || error) && props.nameMatchedCharts?.[key] && (!panel || ['detecting', 'error', 'unavailable'].includes(panel.state));
  });
  return (
    <div className="metrics-layout min-w-0 px-4 pt-3 space-y-4">
      {data?.scopeNotice && <p role="status" className="text-xs text-theme-text-secondary">{data.scopeNotice}</p>}
      {isPlaceholderData && <p role="status" className="text-xs text-theme-text-tertiary">Loading selected request source… Resource charts show the previous sample.</p>}
      <WorkloadRequests data={data} isLoading={isLoading || isPlaceholderData} error={error} setSource={setSource} />
      <section aria-label="Workload resource pressure">
        {(data?.panels.cpu || data?.panels.memory || data?.panels.throttling) && <h3 className="mb-2 text-sm font-semibold text-theme-text-primary">Resources</h3>}
        {props.restartLane}
        {sameResourceScope && <HistoryNotice scope={data?.history.cpu} />}
        <div className="metrics-chart-grid">
          {data?.panels.cpu && <WorkloadChart label="CPU usage" population={data.history.cpu?.mode === 'workload-history' ? 'workload' : 'per Pod'} panel={data.panels.cpu} window={data} scope={sameResourceScope ? undefined : data.history.cpu} referenceLines={data.history.cpu?.mode === 'current-pods' ? props.cpuReferenceLines : undefined} />}
          {data?.panels.memory && <WorkloadChart label="Memory working set" population={data.history.memory?.mode === 'workload-history' ? 'workload' : 'per Pod'} panel={data.panels.memory} window={data} scope={sameResourceScope ? undefined : data.history.memory} referenceLines={data.history.memory?.mode === 'current-pods' ? props.memoryReferenceLines : undefined} />}
          {data?.panels.throttling && <WorkloadChart
            className="metrics-chart-wide"
            label="CPU throttled periods"
            panel={data.panels.throttling}
            window={data}
            scope={sameResourceScope ? undefined : data.history.throttling}
          />}
        </div>
        {(data?.panels.cpu || data?.panels.memory) && <p className="mt-2 text-xs text-theme-text-tertiary">Resource totals include reporting containers and sidecars.</p>}
        {data?.panels.throttling && <p className="mt-2 text-xs text-theme-text-tertiary">
          Throttling is the share of CFS periods throttled, not CPU time lost.
          {data.history.throttling?.mode === 'workload-history'
            ? ' Workload is the total across reporting Pods; Maximum Pod exposes skew. Throttling is weighted by total periods, not an average of Pod percentages.'
            : ` Examines ${data.pods} of ${data.podsTotal} current Pods; previous replicas are not reconstructed.`} {data.reason}
        </p>}
      </section>
      {nameMatchedFallbacks.length > 0 && <details aria-label="Name-matched resource metrics">
        <summary className="mb-2 cursor-pointer text-sm font-medium text-theme-text-secondary">Basic metrics — identity not verified</summary>
        <p className="mb-2 text-xs text-theme-text-secondary">The identity-checked charts above are not available for these metrics. These existing charts match current Pod names, not Pod UIDs or historical workload ownership; matching names in a shared backend may include another cluster.</p>
        <div className="metrics-chart-grid">{nameMatchedFallbacks.map((key) => <div key={key}>{props.nameMatchedCharts?.[key]}</div>)}</div>
      </details>}
      {data && (data.comparison.cpu || data.comparison.memory || data.comparison.throttling) && <PodComparison
        namespace={props.namespace}
        cpu={data.comparison.cpu}
        memory={data.comparison.memory}
        throttle={data.comparison.throttling}
        window={data}
      >
        {(!!props.cpuReferenceLines?.length || !!props.memoryReferenceLines?.length) && <p className="mb-2 text-xs text-theme-text-tertiary">
          Template per Pod
          {!!props.cpuReferenceLines?.length && <> · CPU: {props.cpuReferenceLines.map((line) => line.label).join(', ')}</>}
          {!!props.memoryReferenceLines?.length && <> · Memory: {props.memoryReferenceLines.map((line) => line.label).join(', ')}</>}
          . Actual Pods can differ after injection or rollout.
        </p>}
      </PodComparison>}
    </div>
  );
}

function HistoryNotice({ scope }: { scope?: WorkloadMetrics['history']['cpu'] }) {
  if (!scope) return null;
  if (scope.mode === 'workload-history') return <p className="mb-2 text-xs text-theme-text-tertiary">Workload history · includes previous replicas where ownership and metrics were retained.</p>;
  return <p role="status" className="mb-2 text-xs text-theme-text-secondary">
    {scope.mode === 'current-pods' ? 'Workload history unavailable — showing current Pods only. ' : 'Workload history could not be checked. '}{scope.reason}
  </p>;
}

function WorkloadRequests({ data, isLoading, error, setSource }: {
  data?: WorkloadMetrics;
  isLoading: boolean;
  error: Error | null;
  setSource: (source: WorkloadRequestSource) => void;
}) {
  if (isLoading)
    return (
      <p className="text-xs text-theme-text-tertiary">
        Checking request metrics and resource pressure…
      </p>
    );
  if (error)
    return (
      <p role="status" className="text-sm text-theme-text-secondary">
        Workload metrics could not be loaded: {error.message}
      </p>
    );
  if (!data) return null;
  if (data.state === "detecting" || (data.state === "error" && Object.keys(data.panels).length === 0))
    return <p role="status" className="text-xs text-theme-text-secondary">{data.reason}</p>;
  if (data.state === "unavailable")
    return (
      <div className="text-xs text-theme-text-secondary">
        <p>{data.reason}</p>
      </div>
    );
  const sources = data.sources.filter(
    (s) =>
      s.state === "available" || s.state === "stale" || s.id === data.source,
  );
  const requestPanel = data.panels.requests;
  const hasRequests =
    requestPanel &&
    requestPanel.series.some((s) => s.dataPoints.some((p) => p.value != null));
  const hasResources = [data.panels.cpu, data.panels.memory, data.panels.throttling].some(
    (panel) => panel?.series.some((series) => series.dataPoints.some((point) => point.value != null && Number.isFinite(point.value))),
  );
  const observed = data.panels.observedPods?.series[0];
  const reportingPods = observed && latestWorkloadValue(observed, data.end, data.stepSeconds);
  return (
    <div className="space-y-4 min-w-0 break-words">
      {data.attribution && <details className="text-xs text-theme-text-tertiary">
        <summary className="cursor-pointer">{data.attribution.scope ? "Operator-asserted metrics scope" : "Automatic metrics attribution"}</summary>
        <ul className="mt-2 space-y-1">{Object.entries(data.attribution).map(([source, evidence]) => <li key={source}>{source}: {evidence}</li>)}</ul>
        <p className="mt-2 max-w-4xl leading-relaxed">
          Sources are checked independently. Unmatched Pods are excluded, not treated as zero.
          If automatic matching cannot establish identity, an operator can optionally use <code>--prometheus-single-cluster</code> for a backend dedicated to this cluster,
          or <code>--prometheus-cluster-label cluster=your-cluster</code> for a shared backend with that exact label.
          These overrides must be reasserted after changing the connection.
        </p>
      </details>}
      <section aria-label="Workload requests">
        <div className="flex flex-wrap items-center justify-between gap-2 mb-2">
          <h3 className="text-sm font-semibold text-theme-text-primary">
            Requests
          </h3>
          {sources.length > 1 ? (
            <select
              aria-label="Request metrics source"
              className="rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-xs text-theme-text-secondary"
              value={data.source}
              onChange={(e) =>
                setSource(e.target.value as WorkloadRequestSource)
              }
            >
              {sources.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label}
                  {s.state === "unavailable" || s.state === "error"
                    ? " (unavailable)"
                    : ""}
                </option>
              ))}
            </select>
          ) : (
            <span className="text-xs text-theme-text-tertiary">
              {data.sources.find((s) => s.id === data.source)?.label}
            </span>
          )}
        </div>
        <HistoryNotice scope={data.history.requests} />
        {hasRequests ? (
          <>
            <div className="metrics-chart-grid">
              <WorkloadChart
                label="Requests / sec"
                panel={requestPanel}
                window={data}
              />
              <WorkloadChart
                label="HTTP 5xx"
                panel={data.panels.errors}
                window={data}
              />
              <WorkloadChart
                className="metrics-chart-wide"
                label="Latency · p50 / p95"
                panel={data.panels.p95}
                secondary={data.panels.p50}
                window={data}
              />
            </div>
            <div className="mt-2 text-xs text-theme-text-tertiary space-y-2">
              <p>{data.sources.find((s) => s.id === data.source)?.label} · {Math.round(data.rateWindowSeconds / 60)}-minute rates · {reportingPods == null ? "Reporting Pod count unavailable" : data.history.requests?.mode === 'workload-history' ? `${reportingPods} Pods reporting` : `${reportingPods} of ${data.podsTotal} current Pods reporting`}</p>
              <details>
                <summary className="cursor-pointer">Coverage and interpretation</summary>
                <p className="mt-2 max-w-4xl leading-relaxed">
                  {data.history.requests?.mode === 'workload-history'
                    ? 'Queries follow retained ownership at each timestamp, not today’s replica list. Workload identity is cluster, namespace, kind and name, including recreation under that name. Missing ownership produces gaps; when ownership disappears after Pod deletion, that Pod leaves the aggregate even if its last rate window still has samples.'
                    : `Queries select ${data.pods} of ${data.podsTotal} current Pods; previous replicas are not reconstructed.`}
                  Other Pods may be idle, new, or not instrumented. HTTP 5xx excludes failures without an HTTP response
                  and is not a gRPC error rate. Latency is a histogram approximation at the selected observer, not end-to-end user latency.
                  Ports and processes are combined; health checks and admin traffic may count.
                  {data.source === 'istio' && ' Istio histograms update separately and can briefly trail the request counter; latency uses the histogram observations.'}
                  Rates use a {Math.round(data.rateWindowSeconds / 60)}-minute rolling window, evaluated every {Math.round(data.stepSeconds)} seconds. Longer windows smooth short spikes.
                </p>
              </details>
            </div>
          </>
        ) : (
          <div className="text-xs text-theme-text-tertiary">
          <details>
            <summary className="cursor-pointer">{requestPanel?.state === "error" ? "Request metrics query failed" : requestPanel?.state === "partial" ? "Request metrics withheld" : requestPanel?.state === "detecting" ? "Checking request metrics…" : "No HTTP request observations in this window"}</summary>
            <p className="mt-2 max-w-4xl leading-relaxed">{requestPanel?.reason}{requestPanel?.state === "unavailable" && <> If Beyla is scraped under a different job, configure <code>--beyla-job-selector</code> with its actual job matcher.</>}</p>
          </details>
          {hasResources && <p className="mt-2">Resource charts below remain available.</p>}
          </div>
        )}
      </section>
    </div>
  );
}

function WorkloadChart({
  className = "",
  label,
  population,
  panel,
  secondary,
  window,
  referenceLines,
  scope,
}: {
  className?: string;
  label: string;
  population?: 'workload' | 'per Pod';
  panel?: WorkloadMetricPanel;
  secondary?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "start" | "end" | "stepSeconds">;
  referenceLines?: ReferenceLine[];
  scope?: WorkloadMetrics['history']['cpu'];
}) {
  const series = secondary
    ? [
        ...secondary.series.map((s) => ({ ...s, labels: { quantile: "p50" } })),
        ...(panel?.series ?? []).map((s) => ({
          ...s,
          labels: { quantile: "p95" },
        })),
      ]
    : (panel?.series ?? []);
  const hasSamples = series.some((s) =>
    s.dataPoints.some((p) => p.value != null && Number.isFinite(p.value)),
  );
  const headline = panel?.series.find((s) => s.labels.aggregation === 'Workload') ?? (panel?.series.length === 1 ? panel.series[0] : undefined);
  const seriesLabels = secondary ? series.map((s) => s.labels.quantile) : panel?.series.every((s) => s.labels.aggregation) ? panel.series.map((s) => s.labels.aggregation) : series.length === 1 && Object.keys(series[0].labels).length === 0 ? [label] : undefined;
  const last =
    headline
      ? latestWorkloadValue(headline, window.end, window.stepSeconds)
      : undefined;
  return (
    <section className={`metrics-chart rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0 ${className}`}>
      <header className="flex items-baseline justify-between gap-2 mb-2">
        <h4 className="text-xs font-medium text-theme-text-secondary">
          {`${label}${hasSamples && population ? ` · ${population}` : ''}`}
        </h4>
        {last != null && (
          <span className="text-sm font-semibold tabular-nums text-theme-text-primary">
            {formatMetricValue(last, panel!.unit)}
            {secondary && (
              <span className="ml-1 text-xs text-theme-text-tertiary">p95</span>
            )}
          </span>
        )}
      </header>
      <HistoryNotice scope={scope} />
      {hasSamples ? (
        <>
          <AreaChart
            series={series}
            unit={panel!.unit}
            color="var(--accent)"
            fillColor="var(--accent-muted)"
            layout="dashboard"
            seriesLabels={seriesLabels}
            domain={{ start: window.start, end: window.end }}
            referenceLines={referenceLines?.map((line) => ({ ...line, label: `Template ${line.label}` }))}
          />
          {series.length > 1 && (
            <SeriesLegend series={series} color="var(--accent)" seriesLabels={seriesLabels} />
          )}
        </>
      ) : (
        <p className="py-8 text-xs text-theme-text-tertiary">
          {panel?.reason ||
            "No usable samples. Idle traffic has no defined error percentage or latency."}
        </p>
      )}
      {hasSamples && panel?.reason && (
        <p className="mt-2 text-xs text-theme-text-tertiary">{panel.reason}</p>
      )}
      {hasSamples && !!referenceLines?.length && <p className="mt-2 text-xs text-theme-text-tertiary">Reference lines show current template values per Pod, not historical settings.</p>}
      {secondary?.state === "error" && (
        <p className="mt-2 text-xs text-theme-text-tertiary">
          p50: {secondary.reason}
        </p>
      )}
    </section>
  );
}

function PodComparison({
  namespace,
  cpu,
  memory,
  throttle,
  window,
  children,
}: {
  namespace: string;
  cpu?: WorkloadMetricPanel;
  memory?: WorkloadMetricPanel;
  throttle?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "end" | "stepSeconds" | "pods" | "podsTotal">;
  children?: ReactNode;
}) {
  const [sort, setSort] = useState<"cpu" | "memory" | "throttling">("cpu");
  const values = {
    cpu: workloadPodValues(cpu?.series ?? [], window.end, window.stepSeconds),
    memory: workloadPodValues(
      memory?.series ?? [],
      window.end,
      window.stepSeconds,
    ),
    throttling: workloadPodValues(
      throttle?.series ?? [],
      window.end,
      window.stepSeconds,
    ),
  };
  const pods = [
    ...new Set([
      ...values.cpu.keys(),
      ...values.memory.keys(),
      ...values.throttling.keys(),
    ]),
  ].sort(
    (a, b) =>
      (values[sort].get(b) ?? -1) - (values[sort].get(a) ?? -1) ||
      a.localeCompare(b),
  );
  const show = (pod: string, metric: typeof sort, unit: string) => {
    const value = values[metric].get(pod);
    return value == null ? "—" : formatMetricValue(value, unit);
  };
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0">
      <h4 className="mb-2 text-xs font-medium text-theme-text-secondary">
        Compare current Pods · latest samples
      </h4>
      {children}
      {window.pods < window.podsTotal && <p className="mb-2 text-xs text-theme-text-secondary">Comparing {window.pods} of {window.podsTotal} current Pods. This comparison cap does not limit workload-history totals.</p>}
      {cpu?.state === "error" || memory?.state === "error" ? (
        <p className="text-xs text-theme-text-tertiary">
          Some resource queries failed; missing values are shown as —.
        </p>
      ) : null}
      {cpu?.reason && <p className="text-xs text-theme-text-tertiary">CPU: {cpu.reason}</p>}
      {memory?.reason && <p className="text-xs text-theme-text-tertiary">Memory: {memory.reason}</p>}
      <div className="max-h-72 overflow-auto">
        <table className="w-full min-w-[480px] text-xs text-left">
          <thead className="text-theme-text-tertiary">
            <tr>
              <th className="py-2 font-medium">Pod</th>
              {(["cpu", "memory", "throttling"] as const).map((metric) => (
                <th
                  key={metric}
                  className="py-2 pl-3 text-right font-medium"
                  aria-sort={sort === metric ? "descending" : "none"}
                >
                  <button onClick={() => setSort(metric)}>
                    {metric === "cpu"
                      ? "CPU"
                      : metric === "memory"
                        ? "Memory"
                        : "Throttled"}
                    {sort === metric ? " ↓" : ""}
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pods.map((pod) => (
              <tr
                key={pod}
                className="border-t border-theme-border text-theme-text-secondary"
              >
                <td className="py-2 break-all"><Link className="text-accent hover:underline" to={resourcePath({ kind: "Pod", namespace, name: pod })}>{pod}</Link></td>
                <td className="pl-3 text-right tabular-nums">
                  {show(pod, "cpu", "cores")}
                </td>
                <td className="pl-3 text-right tabular-nums">
                  {show(pod, "memory", "bytes")}
                </td>
                <td className="pl-3 text-right tabular-nums">
                  {show(pod, "throttling", "percent")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {pods.length === 0 && (
          <p className="py-6 text-theme-text-tertiary text-xs">
            No Pod samples available.
          </p>
        )}
      </div>
    </section>
  );
}
