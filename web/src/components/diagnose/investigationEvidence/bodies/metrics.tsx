import {
  Badge,
  TerminalBlock,
  formatRelativeAgeTime,
} from "@skyhook-io/k8s-ui";
import {
  AreaChart,
  SERIES_COLORS,
  SeriesLegend,
  formatMetricValue,
  seriesDisplayLabels,
  seriesFill,
  type TimeSeries,
} from "@skyhook-io/k8s-ui/components/charts";
import {
  metricsDomain,
  type MetricsChangeCoverage,
} from "../../investigationMetrics";
import { Tooltip } from "../../../ui/Tooltip";
import { type EvidenceDataOf, severityBadge } from "../cardParts";

const METRICS_AXIS_LABEL_MAX_CHARS = 72;

function finiteSamples(series: TimeSeries): TimeSeries["dataPoints"] {
  return series.dataPoints.filter((point) => typeof point.value === "number");
}

function MetricsValueTable({
  series,
  labels,
  unit,
  withTime,
}: {
  series: TimeSeries[];
  /** Display name per series, derived from the complete result. */
  labels: string[];
  unit: string;
  withTime: boolean;
}) {
  return (
    <table className="w-full text-xs">
      <tbody>
        {series.map((item, index) => {
          const sample = finiteSamples(item).at(-1);
          return (
            <tr
              key={`${labels[index]}-${index}`}
              className="border-b border-theme-border/60 last:border-b-0"
            >
              <td className="py-1 pr-3 font-mono text-theme-text-secondary [overflow-wrap:anywhere]">
                {labels[index]}
              </td>
              {withTime ? (
                <td className="py-1 pr-3 text-right text-theme-text-tertiary tabular-nums">
                  {sample
                    ? new Date(sample.timestamp * 1000).toLocaleTimeString()
                    : ""}
                </td>
              ) : null}
              <td className="py-1 text-right font-mono tabular-nums text-theme-text-primary">
                {sample && typeof sample.value === "number"
                  ? formatMetricValue(sample.value, unit)
                  : "no value"}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function MetricsBody({
  data,
  changeCoverage,
}: {
  data: EvidenceDataOf<"metrics">;
  changeCoverage?: MetricsChangeCoverage;
}) {
  const annotations = changeCoverage?.markers;
  const unit = data.unit ?? "";
  const axisLabel = data.label ?? data.query;
  const axisTruncated = axisLabel.length > METRICS_AXIS_LABEL_MAX_CHARS;
  const axisText = axisTruncated
    ? `${axisLabel.slice(0, METRICS_AXIS_LABEL_MAX_CHARS - 1)}…`
    : axisLabel;
  const domain = metricsDomain(data);
  const windowText = domain
    ? `${new Date(domain.start * 1000).toLocaleString()} to ${new Date(domain.end * 1000).toLocaleString()}`
    : undefined;
  if (data.series.length === 0) {
    return (
      <div className="space-y-2">
        <p className="text-xs text-theme-text-secondary">
          No series matched this query
          {data.mode === "range" ? " in the window" : ""}.
        </p>
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
        {data.note ? (
          <p className="text-xs text-theme-text-tertiary">{data.note}</p>
        ) : null}
      </div>
    );
  }
  if (data.mode === "instant") {
    return (
      <div className="space-y-2">
        <MetricsValueTable
          series={data.series}
          labels={seriesDisplayLabels(data.series)}
          unit={unit}
          withTime={false}
        />
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
        {data.note ? (
          <p className="text-xs text-theme-text-tertiary">{data.note}</p>
        ) : null}
      </div>
    );
  }
  // Names come from the whole result so two series that differ only by a
  // label the chart would hide stay distinguishable wherever they are listed.
  const labels = seriesDisplayLabels(data.series);
  const indexed = data.series.map((item, index) => ({
    item,
    label: labels[index],
    finite: finiteSamples(item).length,
  }));
  // A series with one finite sample has no line to draw, and one with none
  // (Prometheus serializes NaN and infinities as gaps) has nothing to show.
  // Listing both keeps what was captured readable and states what was not.
  const charted = indexed.filter((entry) => entry.finite >= 2);
  const single = indexed.filter((entry) => entry.finite === 1);
  const empty = indexed.filter((entry) => entry.finite === 0);
  return (
    <div className="space-y-2">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs">
        <Tooltip
          content={axisLabel}
          delay={200}
          position="top"
          disabled={!axisTruncated}
        >
          <span
            className="min-w-0 truncate font-mono text-theme-text-secondary"
            data-testid="investigation-metrics-axis-label"
          >
            {axisText}
          </span>
        </Tooltip>
        {unit ? (
          <span className="text-theme-text-tertiary">({unit})</span>
        ) : null}
        {windowText ? (
          <span className="ml-auto text-theme-text-tertiary">{windowText}</span>
        ) : null}
      </div>
      {charted.length > 0 ? (
        <div className="space-y-1.5 rounded-md border border-theme-border/70 bg-theme-base/30 p-2">
          <AreaChart
            series={charted.map((entry) => entry.item)}
            seriesLabels={charted.map((entry) => entry.label)}
            color={SERIES_COLORS[0]}
            fillColor={seriesFill(0, SERIES_COLORS[0])}
            unit={unit}
            annotations={annotations}
            domain={domain}
            layout="auto"
          />
          {charted.length > 1 ? (
            <SeriesLegend
              series={charted.map((entry) => entry.item)}
              seriesLabels={charted.map((entry) => entry.label)}
              color={SERIES_COLORS[0]}
            />
          ) : null}
        </div>
      ) : null}
      {single.length > 0 ? (
        <div
          className="space-y-1"
          data-testid="investigation-metrics-sparse-series"
        >
          <p className="text-xs text-theme-text-tertiary">
            {single.length === 1
              ? "1 series has a single sample in this window, listed with its time:"
              : `${single.length} series have a single sample in this window, listed with their times:`}
          </p>
          <MetricsValueTable
            series={single.map((entry) => entry.item)}
            labels={single.map((entry) => entry.label)}
            unit={unit}
            withTime
          />
        </div>
      ) : null}
      {empty.length > 0 ? (
        <p
          className="text-xs text-theme-text-tertiary [overflow-wrap:anywhere]"
          data-testid="investigation-metrics-empty-series"
        >
          {empty.length === 1
            ? "1 series had no usable samples in this window: "
            : `${empty.length} series had no usable samples in this window: `}
          <span className="font-mono">
            {empty.map((entry) => entry.label).join("; ")}
          </span>
        </p>
      ) : null}
      {changeCoverage?.checked && charted.length > 0 ? (
        <p className="text-xs text-theme-text-tertiary">
          {annotations?.length === 1
            ? "1 change recorded in this window is marked on the chart."
            : annotations?.length
              ? `${annotations.length} changes recorded in this window are marked on the chart.`
              : // Only that the changes Radar read miss this window — not that
                // the window was fully covered. The change lookup has its own
                // window, which need not span the chart's.
                "None of the changes Radar read fall in this window."}
        </p>
      ) : null}
      {axisTruncated ? (
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
      ) : null}
      {data.note ? (
        <p className="text-xs text-theme-text-tertiary">{data.note}</p>
      ) : null}
    </div>
  );
}

const VISIBLE_ALERT_LABELS = 6;

function alertStateSeverity(state: string | undefined) {
  switch ((state ?? "").toLowerCase()) {
    case "firing":
      return "error" as const;
    case "pending":
      return "warning" as const;
    case "inactive":
      return "success" as const;
    default:
      return "neutral" as const;
  }
}

export function AlertsBody({ data }: { data: EvidenceDataOf<"alerts"> }) {
  const { rule, instances, annotations } = data;
  const health = rule.health?.toLowerCase();
  // Target instances lead; the rest stay visible in producer order.
  const ordered = [...instances].sort(
    (left, right) => Number(right.namesTarget) - Number(left.namesTarget),
  );
  const annotationEntries = Object.entries(annotations);
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        {rule.state ? (
          <Badge severity={alertStateSeverity(rule.state)} size="sm">
            {rule.state}
          </Badge>
        ) : null}
        {rule.labels.severity ? (
          <Badge severity={severityBadge(rule.labels.severity)} size="sm">
            severity {rule.labels.severity}
          </Badge>
        ) : null}
        <Badge tone="structural" size="sm">
          {rule.group}
        </Badge>
        {health && health !== "ok" ? (
          <Badge severity={health === "err" ? "error" : "neutral"} size="sm">
            health {health}
          </Badge>
        ) : null}
      </div>
      {ordered.length > 0 ? (
        <ol className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
          {ordered.map((instance, index) => {
            const labels = Object.entries(instance.labels).filter(
              ([key]) => key !== "alertname" && key !== "severity",
            );
            const hidden = labels.length - VISIBLE_ALERT_LABELS;
            return (
              <li
                key={`${instance.state}-${index}-${labels.map(([k, v]) => `${k}=${v}`).join(",")}`}
                className="rounded-md border border-theme-border/70 bg-theme-base/30 px-2.5 py-2"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge
                    severity={alertStateSeverity(instance.state)}
                    size="sm"
                  >
                    {instance.state}
                  </Badge>
                  {instance.namesTarget ? (
                    <Badge severity="info" size="sm">
                      names this resource
                    </Badge>
                  ) : null}
                  {instance.value ? (
                    <span className="font-mono text-xs text-theme-text-secondary">
                      value {instance.value}
                    </span>
                  ) : null}
                  {instance.activeAt ? (
                    <Tooltip
                      content={new Date(instance.activeAt).toLocaleString()}
                      delay={150}
                      position="left"
                      wrapperClassName="ml-auto"
                    >
                      <time
                        dateTime={instance.activeAt}
                        className="text-xs text-theme-text-tertiary"
                      >
                        active {formatRelativeAgeTime(instance.activeAt)}
                      </time>
                    </Tooltip>
                  ) : null}
                </div>
                {labels.length > 0 ? (
                  <div className="mt-1.5 flex flex-wrap gap-1 font-mono text-[11px] text-theme-text-secondary">
                    {labels
                      .slice(0, VISIBLE_ALERT_LABELS)
                      .map(([key, value]) => (
                        <span
                          key={key}
                          className="rounded bg-theme-base px-1.5 py-0.5 [overflow-wrap:anywhere]"
                        >
                          {key}={value}
                        </span>
                      ))}
                    {hidden > 0 ? (
                      <span className="px-1 py-0.5 text-theme-text-tertiary">
                        +{hidden} more
                      </span>
                    ) : null}
                  </div>
                ) : null}
              </li>
            );
          })}
        </ol>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No active instances were reported for this rule.
        </p>
      )}
      {annotationEntries.length > 0 ? (
        <dl className="space-y-1 text-xs">
          {annotationEntries.map(([key, value]) => (
            <div key={key} className="flex min-w-0 gap-2">
              <dt className="shrink-0 text-theme-text-tertiary">{key}</dt>
              <dd className="min-w-0 leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
                {value}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
      {rule.query ? (
        <TerminalBlock label="Rule expression">{rule.query}</TerminalBlock>
      ) : null}
    </div>
  );
}
