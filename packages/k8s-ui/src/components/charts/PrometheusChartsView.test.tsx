import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import {
  describePodCoverage,
  PARTIAL_DATA_NOTE,
  PrometheusChartsView,
} from "./PrometheusChartsView";
import type { PrometheusResourceMetricsResult } from "./PrometheusChartsView";

function result(
  patch: Partial<PrometheusResourceMetricsResult>,
): PrometheusResourceMetricsResult {
  return { unit: "cores", ...patch } as PrometheusResourceMetricsResult;
}

describe("describePodCoverage", () => {
  it("names the pods a chart covers and what it therefore misses", () => {
    expect(describePodCoverage(result({ pods: 3, podsTotal: 3 }))).toBe(
      "3 current pods; pods replaced during the window are not included",
    );
    expect(describePodCoverage(result({ pods: 1, podsTotal: 1 }))).toBe(
      "1 current pod; pods replaced during the window are not included",
    );
  });

  it("says when the cap cut the list, so a short count is not read as the workload", () => {
    expect(describePodCoverage(result({ pods: 50, podsTotal: 120 }))).toBe(
      "first 50 of 120 current pods; pods replaced during the window are not included",
    );
  });

  it("distinguishes a workload with no pods from a kind that has none to report", () => {
    expect(describePodCoverage(result({ pods: 0 }))).toBe(
      "This workload has no pods running, so there is nothing to chart",
    );
    // A Node chart carries no pod count at all; it gets no caption rather
    // than a claim about pods.
    expect(describePodCoverage(result({}))).toBeUndefined();
  });
});

function renderView(metrics: PrometheusResourceMetricsResult, showEmptyState = true) {
  return renderToStaticMarkup(
    <PrometheusChartsView
      kind="Pod"
      showEmptyState={showEmptyState}
      statusLoading={false}
      isConnected
      onConnect={() => {}}
      connecting={false}
      category="cpu"
      onCategoryChange={() => {}}
      range="1h"
      onRangeChange={() => {}}
      metrics={metrics}
      metricsLoading={false}
    />,
  );
}

const point = { timestamp: 1700000000, value: 1 };
const withSeries = { series: [{ labels: { pod: "a" }, dataPoints: [point, { ...point, timestamp: 1700000060 }] }] };

describe("PrometheusChartsView cluster scope", () => {
  it("says why the chart is withheld instead of showing no data", () => {
    const html = renderView(
      result({
        result: { series: [] },
        clusterScope: { state: "several_clusters", note: "This metrics store holds several clusters." },
      }),
    );
    expect(html).toContain("This metrics store holds several clusters.");
    expect(html).toContain('data-cluster-scope="several_clusters"');
    expect(html).not.toContain("No data for this time range");
  });

  it("shows the withheld note even where an empty chart would hide", () => {
    const html = renderView(
      result({ result: { series: [] }, clusterScope: { state: "unknown", note: "Could not check." } }),
      false,
    );
    expect(html).toContain("Could not check.");
  });

  it("changes nothing for an older Radar that sends no scope, or a scoped answer", () => {
    for (const clusterScope of [undefined, { state: "scoped_verified" as const, note: "Matched." }]) {
      const empty = renderView(result({ result: { series: [] }, clusterScope }));
      expect(empty).toContain("No data for this time range");
      expect(empty).not.toContain("data-cluster-scope");
      expect(empty).not.toContain(PARTIAL_DATA_NOTE);
      expect(renderView(result({ result: { series: [] }, clusterScope }), false)).toBe("");
    }
  });

  it("notes a partial answer under a drawn chart", () => {
    expect(renderView(result({ result: withSeries, partial: true }))).toContain(PARTIAL_DATA_NOTE);
    expect(renderView(result({ result: withSeries }))).not.toContain(PARTIAL_DATA_NOTE);
  });

  it("notes a partial answer with no samples instead of claiming no data", () => {
    const html = renderView(result({ result: { series: [] }, partial: true }));
    expect(html).toContain(PARTIAL_DATA_NOTE);
    expect(html).not.toContain("No data for this time range");
    expect(renderView(result({ result: { series: [] }, partial: true }), false)).toContain(PARTIAL_DATA_NOTE);
  });
});
