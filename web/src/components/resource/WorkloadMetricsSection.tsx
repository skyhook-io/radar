import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { resourcePath } from "../../utils/navigation";
import { Info } from "lucide-react";
import { SEVERITY_TEXT } from "@skyhook-io/k8s-ui/utils/badge-colors";
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
import { WorkloadMetricsHelpDialog } from './WorkloadMetricsHelpDialog';
import { Disclosure } from '../ui/Disclosure';

interface Props {
  kind: string;
  namespace: string;
  name: string;
  range: PrometheusTimeRange;
  cpuReferenceLines?: ReferenceLine[];
  memoryReferenceLines?: ReferenceLine[];
  restartLane?: ReactNode;
  nameMatchedCharts?: Partial<Record<'cpu' | 'memory', ReactNode>>;
  controls?: ReactNode;
}

export function WorkloadMetricsSection(props: Props) {
  const [source, setSource] = useState<WorkloadRequestSource | "">("");
  const [helpOpen, setHelpOpen] = useState(false);
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
  const resourceNotice = sameResourceScope ? sharedPanelNotice([data?.panels.cpu, data?.panels.memory, data?.panels.throttling]) : undefined;
  const hasHistoricalTotals = (['cpu', 'memory'] as const).some((key) => data?.history[key]?.mode === 'workload-history');
  const hasResourceDetails = hasHistoricalTotals || data?.history.throttling?.mode === 'workload-history' || resourceNotice?.state === 'stale';
  return (
    <div className="metrics-layout min-w-0 px-4 pt-3 space-y-4">
      <div className="flex items-start justify-between gap-3">
        <button type="button" aria-haspopup="dialog" onClick={() => setHelpOpen(true)} className="min-w-0 rounded py-1 text-left text-xs text-theme-text-secondary hover:text-accent-text focus-visible:outline-accent">
          <Info aria-hidden="true" className="mr-1 inline h-3.5 w-3.5" />About these metrics{data?.attribution?.scope && ' · operator-asserted scope'}
        </button>
        {props.controls}
      </div>
      {helpOpen && <WorkloadMetricsHelpDialog kind={props.kind} namespace={props.namespace} name={props.name} range={props.range} data={data} pending={isLoading || isPlaceholderData} error={error} onClose={() => setHelpOpen(false)} />}
      {data?.scopeNotice && <p role="status" className={`text-xs ${SEVERITY_TEXT.warning}`}>{data.scopeNotice}</p>}
      {data?.state === 'partial' && data.reason && <p role="status" className={`text-xs ${SEVERITY_TEXT.warning}`}>{data.reason}</p>}
      {isPlaceholderData && <p role="status" className="text-xs text-theme-text-secondary">Loading selected request source… Resources show previous samples.</p>}
      <WorkloadRequests data={data} isLoading={isLoading || isPlaceholderData} error={error} setSource={setSource} />
      <section aria-label="Workload resource pressure">
        {(data?.panels.cpu || data?.panels.memory || data?.panels.throttling) && <div className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h3 className="text-sm font-semibold text-theme-text-primary">Resources</h3>
          {sameResourceScope && <HistoryNotice scope={data?.history.cpu} omitReason={data?.panels.cpu?.reason} />}
        </div>}
        {props.restartLane}
        {resourceNotice && <div className="mb-2"><MetricNotice panel={resourceNotice} label="CPU, memory and throttling" /></div>}
        <div className="metrics-chart-grid">
          {data?.panels.cpu && <WorkloadChart label="CPU usage" population={data.history.cpu?.mode === 'workload-history' ? 'workload' : 'per Pod'} panel={data.panels.cpu} window={data} scope={sameResourceScope ? undefined : data.history.cpu} referenceLines={data.history.cpu?.mode === 'current-pods' ? props.cpuReferenceLines : undefined} sharedNotice={!!resourceNotice} />}
          {data?.panels.memory && <WorkloadChart label="Memory working set" population={data.history.memory?.mode === 'workload-history' ? 'workload' : 'per Pod'} panel={data.panels.memory} window={data} scope={sameResourceScope ? undefined : data.history.memory} referenceLines={data.history.memory?.mode === 'current-pods' ? props.memoryReferenceLines : undefined} sharedNotice={!!resourceNotice} />}
          {data?.panels.throttling && <WorkloadChart
            className="metrics-chart-wide"
            label="CPU throttled periods"
            panel={data.panels.throttling}
            window={data}
            scope={sameResourceScope ? undefined : data.history.throttling}
            footnote="% of CFS periods throttled, not CPU time lost."
            sharedNotice={!!resourceNotice}
          />}
        </div>
        {(data?.panels.cpu || data?.panels.memory || data?.panels.throttling) && <p className="mt-2 text-xs text-theme-text-tertiary">Resource totals include reporting containers and sidecars.</p>}
        {data && hasResourceDetails && <Disclosure className="mt-2 text-xs text-theme-text-tertiary" summary="About resource metrics">
          <div className="mt-2 max-w-2xl space-y-2 text-sm leading-relaxed text-theme-text-secondary">
            {hasHistoricalTotals && <p>In workload-history CPU and memory charts, Workload is the total across reporting Pods; Maximum Pod exposes skew.</p>}
            {data.history.throttling?.mode === 'workload-history' && <p>Throttling is weighted by total periods, not an average of Pod percentages.</p>}
            {resourceNotice?.state === 'stale' && <ul className="space-y-1">{(['cpu', 'memory', 'throttling'] as const).map((key) => data.panels[key]?.reason && <li key={key}>{key}: {data.panels[key]!.reason}</li>)}</ul>}
          </div>
        </Disclosure>}
      </section>
      {nameMatchedFallbacks.length > 0 && <details aria-label="Name-matched resource metrics">
        <summary className="mb-2 cursor-pointer text-xs text-theme-text-secondary">Basic {nameMatchedFallbacks.map((key) => key === 'cpu' ? 'CPU' : 'memory').join(' / ')} · identity unverified</summary>
        <p className={`mb-2 max-w-2xl text-sm ${SEVERITY_TEXT.warning}`}>These charts match current Pod names, not Pod UIDs or historical workload ownership; matching names in a shared backend may include another cluster.</p>
        <div className="metrics-chart-grid">{nameMatchedFallbacks.map((key) => <div key={key}>{props.nameMatchedCharts?.[key]}</div>)}</div>
      </details>}
      {data && (data.comparison.cpu || data.comparison.memory || data.comparison.throttling) && <PodComparison
        namespace={props.namespace}
        cpu={data.comparison.cpu}
        memory={data.comparison.memory}
        throttle={data.comparison.throttling}
        window={data}
        cpuReferenceLines={props.cpuReferenceLines}
        memoryReferenceLines={props.memoryReferenceLines}
      />}
    </div>
  );
}

function HistoryNotice({ scope, omitReason }: { scope?: WorkloadMetrics['history']['cpu']; omitReason?: string }) {
  if (!scope) return null;
  return <Disclosure className={`text-xs ${scope.mode === 'workload-history' ? 'text-theme-text-tertiary' : SEVERITY_TEXT.warning}`} summary={scope.mode === 'workload-history' ? 'Workload history' : scope.mode === 'current-pods' ? 'Current Pods only' : 'Workload history unavailable'}>
    <div className="my-2 max-w-2xl space-y-2 text-sm leading-relaxed text-theme-text-secondary">
      <p>{scope.mode === 'workload-history' ? 'Includes previous replicas where ownership and metrics were retained. Missing history produces gaps, not current-Pod substitutes.' : scope.mode === 'current-pods' ? 'Previous replicas are not reconstructed.' : 'Workload history could not be checked.'}</p>
      {scope.reason && scope.reason !== omitReason && <p>{scope.reason}</p>}
    </div>
  </Disclosure>;
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
      <p role="status" className={`text-sm ${SEVERITY_TEXT.error}`}>
        Workload metrics could not be loaded: {error.message}
      </p>
    );
  if (!data) return null;
  if (data.state === "detecting" || (data.state === "error" && Object.keys(data.panels).length === 0))
    return <p role="status" className={`text-xs ${data.state === 'error' ? SEVERITY_TEXT.error : 'text-theme-text-secondary'}`}>{data.reason}</p>;
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
  const unmatchedSources = !data.source && requestPanel?.state === 'unavailable'
    ? data.sources.filter((candidate) => data.attribution?.[candidate.id]) : [];
  const hasRequests =
    requestPanel &&
    requestPanel.series.some((s) => s.dataPoints.some((p) => p.value != null));
  const requestNotice = sharedPanelNotice([data.panels.requests, data.panels.errors, data.panels.p50, data.panels.p95]);
  const observed = data.panels.observedPods?.series[0];
  const reportingPods = observed && latestWorkloadValue(observed, data.end, data.stepSeconds);
  return (
    <div className="space-y-4 min-w-0 break-words">
      <section aria-label="Workload requests">
        <div className="flex flex-wrap items-center justify-between gap-2 mb-2">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1"><h3 className="text-sm font-semibold text-theme-text-primary">
            Requests
          </h3>
          {hasRequests ? <HistoryNotice scope={data.history.requests} /> : <Disclosure className={`text-xs ${requestPanel?.state === 'error' ? SEVERITY_TEXT.error : requestPanel?.state === 'partial' ? SEVERITY_TEXT.warning : 'text-theme-text-secondary'}`} summary={requestPanel?.state === 'error' ? 'Request metrics query failed' : requestPanel?.state === 'partial' ? 'Request metrics withheld' : requestPanel?.state === 'detecting' ? 'Checking request metrics…' : 'No usable HTTP metrics in this window'}>
            {unmatchedSources.length > 0 ? <ul className="mt-2 max-w-2xl space-y-1 text-sm leading-relaxed text-theme-text-secondary">
              {unmatchedSources.map((candidate) => <li key={candidate.id}><span className="font-medium">{candidate.label}:</span> {data.attribution?.[candidate.id]}</li>)}
            </ul> : <p className="mt-2 max-w-2xl text-sm leading-relaxed text-theme-text-secondary">{requestPanel?.reason || 'No usable HTTP request samples were returned for this workload.'}</p>}
          </Disclosure>}
          </div>
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
            {requestNotice && <div className="mb-2"><MetricNotice panel={requestNotice} label="Requests, HTTP 5xx and latency" /></div>}
            <div className="metrics-chart-grid">
              <WorkloadChart
                label="Requests / sec"
                panel={requestPanel}
                window={data}
                sharedNotice={!!requestNotice}
                footnote="Ports combined · includes health checks and admin traffic."
              />
              <WorkloadChart
                label="HTTP 5xx"
                panel={data.panels.errors}
                window={data}
                sharedNotice={!!requestNotice}
                footnote="Not a gRPC error rate · excludes connection failures."
              />
              <WorkloadChart
                className="metrics-chart-wide"
                label="Latency · p50 / p95"
                panel={data.panels.p95}
                secondary={data.panels.p50}
                window={data}
                sharedNotice={!!requestNotice}
                footnote="At the selected observer · not end-to-end latency."
              />
            </div>
            <div className="mt-2 text-xs text-theme-text-tertiary space-y-2">
              <p>{Math.round(data.rateWindowSeconds / 60)}-minute rates{reportingPods != null && <> · {data.history.requests?.mode === 'workload-history' ? `${reportingPods} Pods reporting` : `${reportingPods} of ${data.podsTotal} current Pods reporting`}</>}</p>
              <Disclosure summary="Coverage details">
                <div className="mt-2 max-w-2xl space-y-2 text-sm leading-relaxed text-theme-text-secondary">
                {reportingPods == null && <p>Reporting Pod count unavailable.</p>}
                <p>
                  {data.history.requests?.mode === 'workload-history'
                    ? 'Queries follow retained ownership at each timestamp, not today’s replica list. Workload identity is cluster, namespace, kind and name, including recreation under that name. Missing ownership produces gaps; when ownership disappears after Pod deletion, that Pod leaves the aggregate even if its last rate window still has samples.'
                    : `Queries select ${data.pods} of ${data.podsTotal} current Pods; previous replicas are not reconstructed.`}
                  </p>
                  {data.source === 'istio' && <p>Istio histograms update separately and can briefly trail the request counter; latency uses the histogram observations.</p>}
                  <p>Rates use a {Math.round(data.rateWindowSeconds / 60)}-minute rolling window, evaluated every {Math.round(data.stepSeconds)} seconds. Longer windows smooth short spikes.
                </p>
                {requestNotice?.state === 'stale' && <ul className="space-y-1">{(['requests', 'errors', 'p50', 'p95'] as const).map((key) => data.panels[key]?.reason && <li key={key}>{key}: {data.panels[key]!.reason}</li>)}</ul>}
                </div>
              </Disclosure>
            </div>
          </>
        ) : null}
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
  footnote,
  sharedNotice,
}: {
  className?: string;
  label: string;
  population?: 'workload' | 'per Pod';
  panel?: WorkloadMetricPanel;
  secondary?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "start" | "end" | "stepSeconds">;
  referenceLines?: ReferenceLine[];
  scope?: WorkloadMetrics['history']['cpu'];
  footnote?: string;
  sharedNotice?: boolean;
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
    <section className={`metrics-chart flex flex-col rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0 ${className}`}>
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
      <HistoryNotice scope={scope} omitReason={panel?.reason} />
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
            stepSeconds={window.stepSeconds}
            referenceLines={referenceLines?.map((line) => ({ ...line, label: `Template ${line.label}` }))}
          />
          {series.length > 1 && (
            <SeriesLegend series={series} color="var(--accent)" seriesLabels={seriesLabels} />
          )}
        </>
      ) : (
        <div className="py-8"><MetricNotice panel={panel} empty /></div>
      )}
      {hasSamples && !sharedNotice && <MetricNotice panel={panel} />}
      {hasSamples && !!referenceLines?.length && <p className="mt-2 text-xs text-theme-text-tertiary">Template lines: current values per Pod, not historical.</p>}
      {secondary && (secondary.state !== panel?.state || secondary.reason !== panel?.reason) && !sharedNotice && <MetricNotice panel={secondary} label="p50" />}
      {hasSamples && footnote && <p className="mt-auto pt-2 text-xs text-theme-text-tertiary">{footnote}</p>}
    </section>
  );
}

function sharedPanelNotice(panels: (WorkloadMetricPanel | undefined)[]) {
  const first = panels[0];
  if (!first || (first.state !== 'stale' && (first.state !== 'partial' || !first.reason))) return undefined;
  return panels.every((panel) => panel?.state === first.state
    && (first.state === 'stale' || panel.reason === first.reason)
    && panel.series.some((series) => series.dataPoints.some((point) => point.value != null && Number.isFinite(point.value)))) ? first : undefined;
}

function MetricNotice({ panel, empty, label }: { panel?: WorkloadMetricPanel; empty?: boolean; label?: string }) {
  if (!empty && !panel?.reason && panel?.state !== 'stale') return null;
  const tone = panel?.state === 'error' ? SEVERITY_TEXT.error : panel?.state === 'partial' || panel?.state === 'stale' ? SEVERITY_TEXT.warning : 'text-theme-text-secondary';
  if (panel?.state === 'stale' && !empty) return <Disclosure className={`mt-2 text-xs ${tone}`} summary={<>{label && `${label}: `}Historical samples only · no recent samples</>}>
    {panel.reason && <p className="mt-2 max-w-2xl text-sm leading-relaxed text-theme-text-secondary">{panel.reason}</p>}
  </Disclosure>;
  return <p role="status" className={`mt-2 text-xs ${tone}`}>{label && `${label}: `}{panel?.reason || 'No usable samples in this window.'}</p>;
}

function PodComparison({
  namespace,
  cpu,
  memory,
  throttle,
  window,
  cpuReferenceLines,
  memoryReferenceLines,
}: {
  namespace: string;
  cpu?: WorkloadMetricPanel;
  memory?: WorkloadMetricPanel;
  throttle?: WorkloadMetricPanel;
  window: Pick<WorkloadMetrics, "end" | "stepSeconds" | "pods" | "podsTotal">;
  cpuReferenceLines?: ReferenceLine[];
  memoryReferenceLines?: ReferenceLine[];
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
  const comparisonPanels = [{ label: 'CPU', panel: cpu }, { label: 'Memory', panel: memory }, { label: 'Throttling', panel: throttle }];
  const pending = comparisonPanels.filter(({ panel }) => panel?.state === 'detecting');
  const notices = new Map<string, { labels: string[]; panel: WorkloadMetricPanel }>();
  for (const { label, panel } of comparisonPanels) {
    if (!panel?.reason || panel.state === 'detecting') continue;
    const key = `${panel.state}\0${panel.reason}`;
    const notice = notices.get(key);
    if (notice) notice.labels.push(label);
    else notices.set(key, { labels: [label], panel });
  }
  const hasTemplate = !!cpuReferenceLines?.length || !!memoryReferenceLines?.length;
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/30 p-3 min-w-0">
      <h4 className="mb-2 text-xs font-medium text-theme-text-secondary">
        Compare current Pods · latest samples
      </h4>
      {hasTemplate && <Disclosure className="mb-2 text-xs text-theme-text-tertiary" summary="Template per Pod · actual Pods may differ">
        <p className="mt-2 max-w-2xl text-sm leading-relaxed text-theme-text-secondary">Actual Pods can differ after injection or rollout. Template values below are not measured allocations for each Pod.</p>
      </Disclosure>}
      {window.pods < window.podsTotal && <Disclosure className={`mb-2 text-xs ${SEVERITY_TEXT.warning}`} summary={<>Comparing {window.pods} of {window.podsTotal} current Pods</>}>
        <p className="mt-2 text-sm text-theme-text-secondary">This comparison cap does not limit workload-history totals.</p>
      </Disclosure>}
      {pending.length > 0 && <p role="status" className="mb-2 text-xs text-theme-text-secondary">Matching {pending.map(({ label }) => label).join(' / ')} metrics to current Pods…</p>}
      {Array.from(notices, ([key, { labels, panel }]) => <MetricNotice key={key} panel={panel} label={labels.join(' / ')} />)}
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
                  {(metric === 'cpu' ? cpuReferenceLines : metric === 'memory' ? memoryReferenceLines : undefined)?.map((line) => <div key={line.kind} className="mt-1 font-normal text-theme-text-tertiary">Template {line.label}</div>)}
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
        {pods.length === 0 && pending.length === 0 && notices.size === 0 && (
          <p className="py-6 text-theme-text-tertiary text-xs">
            No Pod samples available.
          </p>
        )}
      </div>
    </section>
  );
}
