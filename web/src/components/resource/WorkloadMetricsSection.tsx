import { useState } from "react";
import { Link } from "react-router-dom";
import { resourcePath } from "../../utils/navigation";
import {
  AreaChart,
  SeriesLegend,
  formatMetricValue,
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
}

export function WorkloadMetricsSection(props: Props) {
  const [source, setSource] = useState<WorkloadRequestSource | "">("");
  const { data, isLoading, error } = useWorkloadMetrics(
    props.kind,
    props.namespace,
    props.name,
    props.range,
    source,
    true,
  );
  if (isLoading)
    return (
      <p className="px-4 pt-3 text-xs text-theme-text-tertiary">
        Checking request metrics and resource pressure…
      </p>
    );
  if (error)
    return (
      <p role="status" className="px-4 pt-3 text-sm text-theme-text-secondary">
        Workload metrics could not be loaded: {error.message}
      </p>
    );
  if (!data) return null;
  if (data.state === "unavailable")
    return (
      <div className="px-4 pt-3 text-xs text-theme-text-secondary">
        {data.setupRequired ? (
          <details>
            <summary className="cursor-pointer">Request and pressure metrics · operator setup required</summary>
            <p className="mt-2 max-w-3xl leading-relaxed">
              {data.reason}{" "}
              For a backend dedicated to this cluster, restart Radar with <code>--prometheus-single-cluster</code>.
              For a shared backend, use <code>--prometheus-cluster-label cluster=your-cluster</code> with its actual label.
              A context, endpoint, or credentials change requires restarting with a fresh assertion.
              In a managed installation, ask its operator to configure this.
            </p>
          </details>
        ) : <p>{data.reason}</p>}
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
  const observed = data.panels.observedPods?.series[0];
  const reportingPods = observed && latestWorkloadValue(observed, data.end, data.stepSeconds);
  return (
    <div className="px-4 pt-3 space-y-4">
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
        {hasRequests ? (
          <>
            <div className="grid grid-cols-1 lg:grid-cols-3 gap-3">
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
                label="Latency · p50 / p95"
                panel={data.panels.p95}
                secondary={data.panels.p50}
                window={data}
              />
            </div>
            <div className="mt-2 text-xs text-theme-text-tertiary space-y-2">
              <p>{data.sources.find((s) => s.id === data.source)?.label} · {Math.round(data.rateWindowSeconds / 60)}-minute rates · {reportingPods == null ? "Reporting Pod count unavailable" : `${reportingPods} of ${data.podsTotal} current Pods reporting`}</p>
              <details>
                <summary className="cursor-pointer">Coverage and interpretation</summary>
                <p className="mt-2 max-w-4xl leading-relaxed">
                  Queries select {data.pods} of {data.podsTotal} current Pods; previous replicas are not reconstructed.
                  Other Pods may be idle, new, or not instrumented. HTTP 5xx excludes failures without an HTTP response
                  and is not a gRPC error rate. Latency is a histogram approximation at the selected observer, not end-to-end user latency.
                  Rates use a {Math.round(data.rateWindowSeconds / 60)}-minute rolling window, evaluated every {Math.round(data.stepSeconds)} seconds. Longer windows smooth short spikes.
                </p>
              </details>
            </div>
          </>
        ) : (
          <details className="text-xs text-theme-text-tertiary">
            <summary className="cursor-pointer">{requestPanel?.state === "error" ? "Request metrics query failed" : "No request observations in this window"}</summary>
            <p className="mt-2 max-w-4xl leading-relaxed">{requestPanel?.reason}{requestPanel?.state !== "error" && <> If Beyla is scraped under a different job, configure <code>--beyla-job-selector</code> with its actual job matcher.</>}</p>
          </details>
        )}
      </section>
      <section aria-label="Workload resource pressure">
        <h3 className="mb-2 text-sm font-semibold text-theme-text-primary">
          Resource pressure
        </h3>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
          <WorkloadChart
            label="CPU throttled periods"
            panel={data.panels.throttling}
            window={data}
          />
          <PodComparison
            namespace={props.namespace}
            cpu={data.panels.cpu}
            memory={data.panels.memory}
            throttle={data.panels.throttling}
            window={data}
          />
        </div>
        <p className="mt-2 text-xs text-theme-text-tertiary">
          Throttling is the share of CFS periods throttled, not CPU time lost.
          Covers {data.pods} of {data.podsTotal} current Pods; previous replicas
          are not reconstructed. {data.reason}
        </p>
      </section>
    </div>
  );
}

function WorkloadChart({
  label,
  panel,
  secondary,
  window,
}: {
  label: string;
  panel?: WorkloadMetricPanel;
  secondary?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "start" | "end" | "stepSeconds">;
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
  const last =
    panel?.series.length === 1
      ? latestWorkloadValue(panel.series[0], window.end, window.stepSeconds)
      : undefined;
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0">
      <header className="flex items-baseline justify-between gap-2 mb-2">
        <h4 className="text-xs font-medium text-theme-text-secondary">
          {label}
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
      {hasSamples ? (
        <>
          <AreaChart
            series={series}
            unit={panel!.unit}
            color="var(--accent)"
            fillColor="var(--accent-muted)"
            layout="dashboard"
            seriesLabels={secondary ? series.map((s) => s.labels.quantile) : undefined}
            domain={{ start: window.start, end: window.end }}
          />
          {series.length > 1 && (
            <SeriesLegend series={series} color="var(--accent)" seriesLabels={secondary ? series.map((s) => s.labels.quantile) : undefined} />
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
}: {
  namespace: string;
  cpu?: WorkloadMetricPanel;
  memory?: WorkloadMetricPanel;
  throttle?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "end" | "stepSeconds">;
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
      {cpu?.state === "error" || memory?.state === "error" ? (
        <p className="text-xs text-theme-text-tertiary">
          Some resource queries failed; missing values are shown as —.
        </p>
      ) : null}
      <div className="max-h-72 overflow-auto">
        <table className="w-full text-xs text-left">
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
