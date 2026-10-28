import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { MetricsBody, metricsReading } from "./metrics";
import type { EvidenceDataOf } from "../cardParts";

const QUERY =
  'sum(kube_pod_container_status_restarts_total{namespace="gl-crash-config",pod=~"^(api-7c6f9c7bb9-p9czp)$",container="api"})';

function range(query: string): EvidenceDataOf<"metrics"> {
  return {
    type: "metrics",
    origin: "query",
    query,
    mode: "range",
    series: [
      {
        labels: {},
        dataPoints: [
          { timestamp: 1_788_679_043, value: 1 },
          { timestamp: 1_788_682_643, value: 2 },
        ],
      },
    ],
    truncated: false,
  } as EvidenceDataOf<"metrics">;
}

const occurrences = (html: string, text: string) => html.split(text).length - 1;

describe("MetricsBody", () => {
  it("prints a long query once, in full, instead of also cutting it short above the chart", () => {
    const html = renderToStaticMarkup(<MetricsBody data={range(QUERY)} />);
    expect(occurrences(html, "kube_pod_container_status_restarts_total")).toBe(
      1,
    );
    expect(html).not.toContain("investigation-metrics-axis-label");
  });

  it("keeps a short query on the line above the chart", () => {
    const html = renderToStaticMarkup(<MetricsBody data={range("sum(up)")} />);
    expect(html).toContain("investigation-metrics-axis-label");
    expect(occurrences(html, "sum(up)")).toBe(1);
  });

  it("keeps a long label that is not the query, cut short", () => {
    const label = `Restarts of every container in the checkout service ${"x".repeat(40)}`;
    const html = renderToStaticMarkup(
      <MetricsBody data={{ ...range(QUERY), label }} />,
    );
    expect(html).toContain("investigation-metrics-axis-label");
    expect(html).toContain("Restarts of every container");
  });

  it("draws a short chart only when asked", () => {
    expect(
      renderToStaticMarkup(<MetricsBody data={range(QUERY)} size="short" />),
    ).toContain("height:140px");
    expect(
      renderToStaticMarkup(<MetricsBody data={range(QUERY)} />),
    ).not.toContain("height:140px");
  });
});

describe("metricsReading", () => {
  const points = (...values: (number | null)[]) =>
    values.map((value, index) => ({ timestamp: 1_788_679_043 + index, value }));
  const data = (
    series: ReturnType<typeof points>[],
    mode: "range" | "instant" = "range",
  ) =>
    ({
      ...range("up"),
      // Values read in the chart's own unit formatting.
      unit: "count",
      mode,
      series: series.map((dataPoints) => ({ labels: {}, dataPoints })),
    }) as EvidenceDataOf<"metrics">;

  it("reads one series from its first to its last value", () => {
    expect(metricsReading(data([points(3, 9, 12)]))).toBe("3 → 12");
  });

  it("adds a peak or a low that the first and last values hide", () => {
    expect(metricsReading(data([points(3, 40, 12)]))).toBe("3 → 12 · peak 40");
    // A dip to zero or a counter reset is the incident; it must not fold away.
    expect(metricsReading(data([points(1, 0, 1)]))).toBe("1 → 1 · low 0");
    expect(metricsReading(data([points(100, 0, 101)]))).toBe(
      "100 → 101 · low 0",
    );
  });

  it("says when samples are missing and reads several series by their range", () => {
    expect(metricsReading(data([points(5, 7, null, null)]))).toBe(
      "5 → 7 · with gaps",
    );
    expect(metricsReading(data([points(1, 2), points(8, 3)]))).toBe(
      "2 series · 1 to 8",
    );
    // A series with no samples still counts as one of the series read.
    expect(metricsReading(data([points(1, 2), points(null)]))).toBe(
      "2 series · 1 to 2 · with gaps",
    );
  });

  it("reads an instant value as itself and says nothing without a sample", () => {
    expect(metricsReading(data([points(4)], "instant"))).toBe("4");
    expect(metricsReading(data([points(null)]))).toBeUndefined();
    expect(metricsReading(data([]))).toBeUndefined();
  });
});
