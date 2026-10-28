import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { MetricsBody } from "./metrics";
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
