import { hasPrometheusCharts, metricsSourceUnavailable } from '../../api/prometheusCharts'
import { clsx } from "clsx";
import { useMemo, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Loader2 } from "lucide-react";
import { Disclosure } from "@skyhook-io/k8s-ui/components/ui/Disclosure";
import { ClusterScopeNote } from "@skyhook-io/k8s-ui/components/ui/ClusterScopeNote";
import { isClusterScopeWithheld } from "@skyhook-io/k8s-ui/types/cluster-scope";
import {
  AreaChart,
  SeriesLegend,
  computeSaturation,
  describePodCoverage,
  PARTIAL_DATA_NOTE,
  type ReferenceLine,
} from "@skyhook-io/k8s-ui/components/charts";
import {
  SEVERITY_BADGE,
  SEVERITY_TEXT,
  type Severity,
} from "@skyhook-io/k8s-ui/utils/badge-colors";
import {
  usePrometheusStatus,
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
import { leastCertainScope, NAME_MATCHED_CLUSTER_NOTE, NAME_MATCHED_SUMMARY } from "./nameMatchedScope";
import { MetricsNotConnected, OpenObserveUnavailable } from "./MetricsNotConnected";

// Used when MetricsTabContent is in expanded (full-screen) mode. Drawer mode
// uses the single-chart tabbed `PrometheusCharts` instead — drawer width
// can't fit the grid cleanly.
export interface PrometheusChartsGridProps {
  kind: string;
  namespace: string;
  name: string;
  /** Optional full K8s resource for request/limit overlay derivation. */
  resource?: any;
  /** Fill and scroll its container (default). False lets the parent scroll it with content below. */
  fill?: boolean;
  workloadTotals?: ReactNode;
  showRightsizing?: boolean;
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
  fill = true,
  workloadTotals,
  showRightsizing = true,
}: PrometheusChartsGridProps) {
  useAutoPromConnect();
  const { data: status, isLoading: statusLoading } = usePrometheusStatus();
  const isConnected = hasPrometheusCharts(status);
  const isSupported = SUPPORTED_KINDS.has(kind);
  const isWorkload = ["Deployment", "StatefulSet", "DaemonSet"].includes(kind);
  const showRestartLane = isSupported && kind !== "Node";

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

  // Read from the cache the network and storage charts fill; never fetches itself.
  const rxScope = usePrometheusResourceMetrics(kind, namespace, name, "network_rx", timeRange, false).data?.clusterScope;
  const txScope = usePrometheusResourceMetrics(kind, namespace, name, "network_tx", timeRange, false).data?.clusterScope;
  const diskScope = usePrometheusResourceMetrics(kind, namespace, name, "filesystem", timeRange, false).data?.clusterScope;
  const hasDisk = kind === "Node" || !status?.gkeSystemMetrics || status.connected;
  const nameMatchedScope = leastCertainScope(hasDisk ? [rxScope, txScope, diskScope] : [rxScope, txScope]);
  const nameMatchedNote = NAME_MATCHED_CLUSTER_NOTE[nameMatchedScope];

  if (!isSupported) return null;

  const rangeControl = <select
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
        </select>;

  // OpenObserve down takes only its own charts: workload totals from another
  // source and the restart lane stay, as they do when Prometheus isn't set up.
  if (status && metricsSourceUnavailable(status)) {
    const notice = (
      <OpenObserveUnavailable status={status} compact={!!workloadTotals} />
    );
    if (!workloadTotals && !showRestartLane) return notice;
    return (
      <div className={clsx("space-y-4 p-4", fill && "h-full overflow-auto")}>
        {workloadTotals && <div className="flex">{rangeControl}</div>}
        {notice}
        {workloadTotals}
        {showRestartLane && <RestartEventLane kind={kind} namespace={namespace} name={name} range={timeRange} />}
      </div>
    );
  }

  if (workloadTotals && !isConnected) {
    return (
      <div className={clsx("space-y-4 p-4", fill && "h-full overflow-auto")}>
        <div className="flex">{rangeControl}</div>
        {workloadTotals}
        <RestartEventLane kind={kind} namespace={namespace} name={name} range={timeRange} />
      </div>
    );
  }

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

  if (!isConnected) return <MetricsNotConnected status={status} isWorkload={isWorkload} />;

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
  const disk = hasDisk ? findCategory("filesystem") : undefined;
  const chartPanels = disk ? [...primaryCats, { def: disk }] : primaryCats;
  const renderPanel = ({ def, refLines }: { def: CategoryDef; refLines?: ReferenceLine[] }) => (
    <MetricsPanel key={def.key} category={def} kind={kind} namespace={namespace} name={name} timeRange={timeRange} referenceLines={refLines} />
  );

  return (
    <div className={clsx("flex flex-col min-w-0 w-full", fill && "h-full overflow-auto")}>
      {!isWorkload && <div className="flex shrink-0 items-center justify-between gap-3 px-4 pt-3">{rangeControl}</div>}

      {isWorkload && (
        <WorkloadMetricsSection key={`${kind}/${namespace}/${name}`} kind={kind} namespace={namespace} name={name} range={timeRange}
          controls={rangeControl} workloadTotals={workloadTotals}
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
        {isWorkload && <h3 className="mb-2 text-sm font-semibold text-theme-text-primary">{disk ? "Network and storage" : "Network"}</h3>}
        {isWorkload && <Disclosure className="mb-2 text-xs text-theme-text-secondary" summary={`Pod-name matched · ${NAME_MATCHED_SUMMARY[nameMatchedScope]}`}>
          <p className="mt-2 max-w-2xl text-sm leading-relaxed">These charts match current Pod names, independently of the identity-checked charts above. {nameMatchedNote ?? "Matching names in a shared backend may include another cluster."}</p>
        </Disclosure>}
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
      {showRightsizing && status?.connected && isWorkload && (
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
            {metrics?.source === "gke" && category.key === "memory" ? "Non-evictable memory" : metrics?.source === "gke" && category.key === "filesystem" && kind === "Node" ? "Ephemeral storage used" : category.label}
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
        ) : isClusterScopeWithheld(metrics?.clusterScope) ? (
          <ClusterScopeNote scope={metrics?.clusterScope} />
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
            {metrics?.partial && (
              <p className="mt-1.5 text-xs text-theme-text-tertiary">{PARTIAL_DATA_NOTE}</p>
            )}
          </>
        ) : (
          <PanelNoData hint={metrics?.hint} partial={metrics?.partial} />
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

function PanelNoData({ hint, partial }: { hint?: string; partial?: boolean }) {
  return (
    <div className="flex flex-col items-center justify-center h-[240px] text-theme-text-tertiary text-xs px-3 text-center">
      {partial ? "No samples in a partial answer" : "No data"}
      {partial && (
        <span className="text-theme-text-quaternary mt-1 max-w-xs">{PARTIAL_DATA_NOTE}</span>
      )}
      {hint && (
        <span className="text-theme-text-quaternary mt-1 max-w-xs">{hint}</span>
      )}
    </div>
  );
}

export { isPrometheusSupported } from "./PrometheusCharts";
