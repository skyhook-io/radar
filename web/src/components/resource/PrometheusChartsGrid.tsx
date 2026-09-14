import { useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { Loader2, Wifi, WifiOff } from "lucide-react";
import {
  AreaChart,
  SeriesLegend,
  computeSaturation,
  describePodCoverage,
  type ReferenceLine,
} from "@skyhook-io/k8s-ui/components/charts";
import {
  SEVERITY_BADGE,
  SEVERITY_TEXT,
  type Severity,
} from "@skyhook-io/k8s-ui/utils/badge-colors";
import {
  usePrometheusStatus,
  usePrometheusConnect,
  usePrometheusResourceMetrics,
  useAutoPromConnect,
  type PrometheusMetricCategory,
  type PrometheusTimeRange,
} from "../../api/client";
import {
  MetricsSummary,
  TIME_RANGES,
  WORKLOAD_CATEGORIES,
  NODE_CATEGORIES,
  computeRequestLimitLines,
  type CategoryDef,
} from "./PrometheusCharts";
import { RestartEventLane } from "./RestartChart";
import { Tooltip } from "../ui/Tooltip";
import { WorkloadMetricsSection } from "./WorkloadMetricsSection";
import { RightsizingStrip } from "./RightsizingStrip";
import { useNavCustomization } from "../../context/NavCustomization";

// Used when MetricsTabContent is in expanded (full-screen) mode. Drawer mode
// uses the single-chart tabbed `PrometheusCharts` instead — drawer width
// can't fit the grid cleanly.
export interface PrometheusChartsGridProps {
  kind: string;
  namespace: string;
  name: string;
  /** Optional full K8s resource for request/limit overlay derivation. */
  resource?: any;
}

const SUPPORTED_KINDS = new Set([
  "Pod",
  "Deployment",
  "StatefulSet",
  "DaemonSet",
  "ReplicaSet",
  "Job",
  "CronJob",
  "Node",
]);

export function PrometheusChartsGrid({
  kind,
  namespace,
  name,
  resource,
}: PrometheusChartsGridProps) {
  useAutoPromConnect();
  const { data: status, isLoading: statusLoading } = usePrometheusStatus();
  const connectMutation = usePrometheusConnect();
  const isConnected = status?.connected === true;
  const isSupported = SUPPORTED_KINDS.has(kind);
  const isWorkload = ["Deployment", "StatefulSet", "DaemonSet"].includes(kind);
  const showRestartLane = isSupported && kind !== "Node";

  const settingsAvailable = !useNavCustomization().embedded;
  const [searchParams, setSearchParams] = useSearchParams();
  const timeRange = TIME_RANGES.find((range) => range.value === searchParams.get("metricsRange"))?.value ?? "1h";

  const categories = kind === "Node" ? NODE_CATEGORIES : WORKLOAD_CATEGORIES;

  // CPU + memory get reference-line overlays when a resource is provided.
  // Computed once at the parent so each panel can stay otherwise generic.
  const cpuRefLines = useMemo<ReferenceLine[] | undefined>(
    () =>
      resource ? computeRequestLimitLines(resource, kind, "cpu") : undefined,
    [resource, kind],
  );
  const memRefLines = useMemo<ReferenceLine[] | undefined>(
    () =>
      resource ? computeRequestLimitLines(resource, kind, "memory") : undefined,
    [resource, kind],
  );

  if (!isSupported) return null;

  if (statusLoading) {
    return (
      <div className="flex items-center justify-center py-12 text-theme-text-tertiary">
        <Loader2 className="w-5 h-5 animate-spin mr-2" />
        Checking Prometheus availability...
      </div>
    );
  }

  if (!isConnected && status?.discovering) {
    return (
      <div className="flex items-center justify-center py-12 text-theme-text-tertiary">
        <Loader2 className="w-5 h-5 animate-spin mr-2" />
        Discovering Prometheus…
      </div>
    );
  }

  if (!isConnected) {
    return (
      <div className="flex flex-col items-center justify-center py-12 gap-4">
        <WifiOff className="w-10 h-10 text-theme-text-quaternary" />
        <div className="text-center">
          <p className="text-sm text-theme-text-secondary mb-1">
            Prometheus not connected
          </p>
          <p className="text-xs text-theme-text-tertiary mb-4">
            {status?.error ||
              "Connect to view historical CPU, memory, and network metrics"}
          </p>
          <div className="flex flex-wrap items-center justify-center gap-3">
          <button
            onClick={() => connectMutation.mutate()}
            disabled={connectMutation.isPending}
            className="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium rounded-lg btn-brand"
          >
            {connectMutation.isPending ? (
              <Loader2 className="w-4 h-4 animate-spin" />
            ) : (
              <Wifi className="w-4 h-4" />
            )}
            Discover Prometheus
          </button>
          {settingsAvailable && <button
            type="button"
            className="text-sm text-accent hover:underline"
            onClick={() => window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'prometheus' } }))}
          >Configure metrics</button>}
          </div>
          {!settingsAvailable && <p className="mt-3 text-xs text-theme-text-tertiary">Ask your operator to configure the metrics connection for this cluster.</p>}
          {isWorkload && <a className="mt-3 inline-block text-xs text-accent hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/workload-metrics.md#what-each-chart-needs" target="_blank" rel="noopener noreferrer">What each chart needs</a>}
        </div>
      </div>
    );
  }

  const findCategory = (
    key: PrometheusMetricCategory,
  ): CategoryDef | undefined => categories.find((c) => c.key === key);

  const primaryCats: { def: CategoryDef; refLines?: ReferenceLine[] }[] = [];
  const cpu = findCategory("cpu");
  if (cpu) primaryCats.push({ def: cpu, refLines: cpuRefLines });
  const mem = findCategory("memory");
  if (mem) primaryCats.push({ def: mem, refLines: memRefLines });
  if (kind !== "Node") {
    const rx = findCategory("network_rx");
    if (rx) primaryCats.push({ def: rx });
    const tx = findCategory("network_tx");
    if (tx) primaryCats.push({ def: tx });
  }
  const disk = findCategory("filesystem");
  const chartPanels = disk ? [...primaryCats, { def: disk }] : primaryCats;
  const renderPanel = ({ def, refLines }: { def: CategoryDef; refLines?: ReferenceLine[] }) => (
    <MetricsPanel key={def.key} category={def} kind={kind} namespace={namespace} name={name} timeRange={timeRange} referenceLines={refLines} />
  );

  return (
    <div className="flex flex-col min-w-0 w-full h-full overflow-auto">
      <div className="flex shrink-0 items-center justify-between gap-3 px-4 pt-3">
        {isWorkload && <a className="text-xs text-accent hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/workload-metrics.md#what-each-chart-needs" target="_blank" rel="noopener noreferrer">What each chart needs</a>}
        <select
          aria-label="Metrics time range"
          value={timeRange}
          onChange={(e) => {
            const next = new URLSearchParams(searchParams);
            next.set("metricsRange", e.target.value);
            setSearchParams(next, { replace: true });
          }}
          className="ml-auto rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-xs text-theme-text-secondary shadow-theme-sm focus:outline-none focus:ring-1 focus:ring-accent/50"
        >
          {TIME_RANGES.map((tr) => (
            <option key={tr.value} value={tr.value}>
              {tr.label}
            </option>
          ))}
        </select>
      </div>

      {isWorkload && (
        <WorkloadMetricsSection key={`${kind}/${namespace}/${name}`} kind={kind} namespace={namespace} name={name} range={timeRange}
          cpuReferenceLines={cpuRefLines} memoryReferenceLines={memRefLines}
          nameMatchedCharts={{ cpu: cpu && renderPanel({ def: cpu }), memory: mem && renderPanel({ def: mem }) }}
          restartLane={showRestartLane && <div className="mb-3"><RestartEventLane kind={kind} namespace={namespace} name={name} range={timeRange} /></div>} />
      )}

      {/* Restart lane sits above the grid so its markers visually align with
          the time axis of the charts below. */}
      {showRestartLane && !isWorkload && (
        <div className="px-4 pt-3">
          <RestartEventLane
            kind={kind}
            namespace={namespace}
            name={name}
            range={timeRange}
          />
        </div>
      )}

      <div className="metrics-layout min-w-0 px-4 pt-4">
        {isWorkload && <h3 className="mb-2 text-sm font-semibold text-theme-text-primary">Network and storage</h3>}
        {isWorkload && <p className="mb-2 text-xs text-theme-text-tertiary">These charts use Pod-name matching, independently of the identity-checked resource and request charts above.</p>}
        <div className="metrics-chart-grid">
          {chartPanels.filter(({ def }) => !isWorkload || (def.key !== "cpu" && def.key !== "memory")).map(renderPanel)}
        </div>
      </div>

      <PodCoverageNote
        kind={kind}
        namespace={namespace}
        name={name}
        timeRange={timeRange}
        category={isWorkload ? "network_rx" : "cpu"}
      />
      {["Deployment", "StatefulSet", "DaemonSet"].includes(kind) && (
        <div className="px-4 pb-4"><RightsizingStrip kind={kind} namespace={namespace} name={name} /></div>
      )}
    </div>
  );
}

function PodCoverageNote({
  kind,
  namespace,
  name,
  timeRange,
  category,
}: {
  kind: string;
  namespace: string;
  name: string;
  timeRange: PrometheusTimeRange;
  category: PrometheusMetricCategory;
}) {
  const { data } = usePrometheusResourceMetrics(
    kind,
    namespace,
    name,
    category,
    timeRange,
    true,
  );
  const note = data ? describePodCoverage(data) : undefined;
  if (!note) return null;
  return <p className="px-4 pb-4 text-xs text-theme-text-tertiary">{note}</p>;
}

interface MetricsPanelProps {
  category: CategoryDef;
  kind: string;
  namespace: string;
  name: string;
  timeRange: PrometheusTimeRange;
  referenceLines?: ReferenceLine[];
}

function MetricsPanel({
  category,
  kind,
  namespace,
  name,
  timeRange,
  referenceLines,
}: MetricsPanelProps) {
  const {
    data: metrics,
    isLoading,
    error,
  } = usePrometheusResourceMetrics(
    kind,
    namespace,
    name,
    category.key,
    timeRange,
    true,
  );

  const series = metrics?.result?.series;
  const hasData = (series?.length ?? 0) > 0;

  // "% of limit / request" derived from current peak vs reference lines.
  // Without this, a low-utilization workload with a high limit looks like
  // an empty chart — the user can't tell healthy from starved at a glance.
  const saturation =
    hasData && series && referenceLines
      ? computeSaturation(series, referenceLines)
      : undefined;

  return (
    <section className="metrics-chart rounded-lg border border-theme-border bg-theme-surface/30 p-3 flex flex-col min-w-0">
      <header className="flex flex-wrap items-center justify-between mb-2 gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide">
            {category.label}
          </h3>
          {saturation && <SaturationChip {...saturation} />}
        </div>
        {hasData && series && (
          <MetricsSummary
            series={series}
            category={category}
            unit={metrics!.unit}
          />
        )}
      </header>

      <div className="min-w-0">
        {isLoading ? (
          <PanelLoading />
        ) : error ? (
          <PanelError message={(error as Error).message} />
        ) : hasData && series ? (
          <>
            <AreaChart
              series={series}
              color={category.chartColor}
              fillColor={category.fillColor}
              unit={metrics!.unit}
              referenceLines={referenceLines}
              layout="dashboard"
            />
            {series.length > 1 && (
              <div className="mt-1.5">
                <SeriesLegend series={series} color={category.chartColor} />
              </div>
            )}
          </>
        ) : (
          <PanelNoData hint={metrics?.hint} />
        )}
      </div>
    </section>
  );
}

function SaturationChip({
  ratio,
  against,
}: {
  ratio: number;
  against: "limit" | "request";
}) {
  // Thresholds match the rightsizing tone vocabulary: amber from 75% (start
  // watching), red at 90% (the same OOM-risk boundary the backend uses for
  // memory in classifyRightsizing).
  const tone: Severity =
    ratio >= 0.9
      ? "error"
      : ratio >= 0.75
        ? "warning"
        : ratio < 0.05
          ? "info"
          : "neutral";
  const label = `${(ratio * 100).toFixed(ratio < 0.1 ? 1 : 0)}% of ${against}`;
  return (
    <span className={`badge badge-sm ${SEVERITY_BADGE[tone]}`}>{label}</span>
  );
}

function PanelLoading() {
  return (
    <div className="flex items-center justify-center h-[240px] text-theme-text-tertiary text-xs">
      <Loader2 className="w-4 h-4 animate-spin mr-2" />
      Loading…
    </div>
  );
}

function PanelError({ message }: { message: string }) {
  return (
    <div
      className={`flex flex-col items-center justify-center h-[240px] ${SEVERITY_TEXT.warning} text-xs px-3 text-center`}
    >
      Query failed
      <Tooltip content={message} wrapperClassName="!block w-full">
        <span className="text-theme-text-quaternary mt-0.5 line-clamp-2">
          {message}
        </span>
      </Tooltip>
    </div>
  );
}

function PanelNoData({ hint }: { hint?: string }) {
  return (
    <div className="flex flex-col items-center justify-center h-[240px] text-theme-text-tertiary text-xs px-3 text-center">
      No data
      {hint && (
        <span className="text-theme-text-quaternary mt-1 max-w-xs">{hint}</span>
      )}
    </div>
  );
}

export { isPrometheusSupported } from "./PrometheusCharts";
