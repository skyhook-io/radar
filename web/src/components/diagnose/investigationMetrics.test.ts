import { describe, expect, it } from "vitest";

import {
  projectInvestigationEvidence,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import {
  metricsChangeMarkers,
  metricsDomain,
  metricsUnitForExpression,
} from "./investigationMetrics";

const target = {
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
};

function tool(
  id: string,
  name: string,
  result: unknown,
  patch: Partial<
    Extract<InvestigationEvidenceTimelineItem, { kind: "tool" }>
  > = {},
): Extract<InvestigationEvidenceTimelineItem, { kind: "tool" }> {
  return {
    kind: "tool",
    id,
    tool: name,
    status: "done",
    summary: JSON.stringify({ namespace: "shop", name: "api" }),
    result: JSON.stringify(result),
    isError: false,
    radarEvidence: true,
    ...patch,
  };
}

const window = { start: "2026-09-06T07:00:00Z", end: "2026-09-06T09:00:00Z" };

function rangeResult(selectors: unknown[]) {
  return {
    query:
      'sum(container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"})',
    type: "range",
    ...window,
    step: "60s",
    series: [
      {
        labels: {},
        dataPoints: [
          { timestamp: Date.parse(window.start) / 1000, value: 1 },
          { timestamp: Date.parse(window.end) / 1000, value: 2 },
        ],
      },
    ],
    selectors,
  };
}

const targetSelectors = [
  {
    metric: "container_memory_working_set_bytes",
    matchers: [
      { label: "namespace", op: "=", value: "shop" },
      { label: "pod", op: "=~", value: "api-.*" },
    ],
  },
];

function change(
  kind: string,
  name: string,
  timestamp: string,
  apiVersion = "v1",
) {
  return {
    kind,
    apiVersion,
    namespace: "shop",
    name,
    changeType: "update",
    summary: `${kind} ${name} changed`,
    timestamp,
  };
}

const diagnoseBundle = {
  resource: {
    apiVersion: "apps/v1",
    kind: "Deployment",
    metadata: { namespace: "shop", name: "api" },
  },
  resourceContext: {
    tier: "diagnostic",
    uses: {
      configMaps: [
        { kind: "ConfigMap", namespace: "shop", name: "api-config" },
      ],
      secrets: [{ kind: "Secret", namespace: "shop", name: "api-secret" }],
    },
  },
  logsCurrent: [
    {
      pod: "api-7f6-abc",
      container: "api",
      logs: { lines: ["boot"], totalLines: 1, matchedLines: 0, fallback: true },
    },
  ],
  recentChanges: [
    change("ConfigMap", "api-config", "2026-09-06T07:30:00Z"),
    change("Deployment", "api", "2026-09-06T08:10:00Z", "apps/v1"),
    change("Pod", "api-7f6-abc", "2026-09-06T08:11:00Z"),
    change("Deployment", "worker", "2026-09-06T08:20:00Z", "apps/v1"),
    change("ConfigMap", "api-config", "2026-09-06T06:30:00Z"),
    change("ConfigMap", "api-config", "2026-09-06T07:30:00Z"),
  ],
  pods: 1,
};

describe("metricsUnitForExpression", () => {
  const selector = (metric: string) => [{ metric, matchers: [] }];

  it("keeps the unit every metric states through aggregations and wrappers", () => {
    expect(
      metricsUnitForExpression(
        'sum(max by (pod,namespace,container) (container_memory_working_set_bytes{namespace="shop", pod=~"api-.*"}))',
        selector("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
    expect(
      metricsUnitForExpression(
        'sum(rate(container_network_receive_bytes_total{namespace="shop"}[5m])) by (pod)',
        selector("container_network_receive_bytes_total"),
      ),
    ).toBe("bytes/s");
    expect(
      metricsUnitForExpression(
        "increase(container_network_receive_bytes_total[1h])",
        selector("container_network_receive_bytes_total"),
      ),
    ).toBe("bytes");
    expect(
      metricsUnitForExpression(
        "max_over_time(http_request_duration_seconds[10m])",
        selector("http_request_duration_seconds"),
      ),
    ).toBe("seconds");
  });

  it("stays unitless for ratios, mixed metrics and bare numbers", () => {
    expect(
      metricsUnitForExpression('a_bytes / b_bytes{path="/api/v1"}', [
        ...selector("a_bytes"),
        ...selector("b_bytes"),
      ]),
    ).toBe("");
    expect(
      metricsUnitForExpression("a_bytes + b_seconds", [
        ...selector("a_bytes"),
        ...selector("b_seconds"),
      ]),
    ).toBe("");
    expect(
      metricsUnitForExpression(
        "container_memory_working_set_bytes + kube_pod_info",
        [
          ...selector("container_memory_working_set_bytes"),
          ...selector("kube_pod_info"),
        ],
      ),
    ).toBe("");
    expect(metricsUnitForExpression("42", [])).toBe("");
    expect(metricsUnitForExpression("1 / 2", [])).toBe("");
  });

  it("drops the unit when a function counts or flags instead of measuring", () => {
    for (const query of [
      'count(container_memory_working_set_bytes{namespace="shop"})',
      'count by (pod) (container_memory_working_set_bytes{namespace="shop"})',
      "count without (container) (container_memory_working_set_bytes)",
      "count_over_time(container_memory_working_set_bytes[1h])",
      "absent(container_memory_working_set_bytes)",
      "changes(http_request_duration_seconds[10m])",
      "resets(container_network_receive_bytes_total[1h])",
      "timestamp(container_memory_working_set_bytes)",
    ]) {
      expect(
        metricsUnitForExpression(
          query,
          selector(query.match(/[a-z_]+_(?:bytes|seconds)(?:_total)?/)![0]),
        ),
        query,
      ).toBe("");
    }
    expect(
      metricsUnitForExpression(
        "topk(3, container_memory_working_set_bytes)",
        selector("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
  });

  it("does not turn a matcher value containing a slash into a ratio", () => {
    expect(
      metricsUnitForExpression(
        'container_memory_working_set_bytes{image="ghcr.io/example/api"}',
        selector("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
  });

  it("names a unit for a bare metric with a unit suffix", () => {
    expect(
      metricsUnitForExpression(
        'container_memory_working_set_bytes{namespace="shop"}',
        selector("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
    expect(
      metricsUnitForExpression(
        "http_request_duration_seconds",
        selector("http_request_duration_seconds"),
      ),
    ).toBe("seconds");
    expect(
      metricsUnitForExpression(
        "rate(container_cpu_usage_seconds_total[5m])",
        selector("container_cpu_usage_seconds_total"),
      ),
    ).toBe("");
    expect(
      metricsUnitForExpression("a_bytes / b_bytes", [
        ...selector("a_bytes"),
        ...selector("b_bytes"),
      ]),
    ).toBe("");
    expect(
      metricsUnitForExpression(
        "kube_pod_container_status_restarts_total",
        selector("kube_pod_container_status_restarts_total"),
      ),
    ).toBe("");
  });
});

describe("metricsDomain", () => {
  it("returns the range window in unix seconds and nothing for instant results", () => {
    expect(metricsDomain({ mode: "range", ...window })).toEqual({
      start: Date.parse(window.start) / 1000,
      end: Date.parse(window.end) / 1000,
    });
    expect(metricsDomain({ mode: "instant" })).toBeUndefined();
    expect(
      metricsDomain({ mode: "range", start: window.end, end: window.start }),
    ).toBeUndefined();
  });
});

describe("metricsChangeMarkers", () => {
  function markersFor(
    timelines: InvestigationEvidenceTimelineItem[][],
    sourceId: string,
  ) {
    const projection = projectInvestigationEvidence(
      timelines.map((timeline) => ({ timeline })),
      target,
    );
    const observation = projection.groups
      .flatMap((group) => group.observations)
      .find(
        (item) =>
          item.data.type === "metrics" && item.source.stepId === sourceId,
      );
    if (!observation) throw new Error("metrics observation missing");
    return metricsChangeMarkers(projection.groups, observation);
  }

  it("marks same-turn changes to the subject and its producer-established relatives, once each, inside the window", () => {
    const markers = markersFor(
      [
        [
          tool("diag", "diagnose", diagnoseBundle),
          tool("prom", "query_prometheus", rangeResult(targetSelectors)),
        ],
      ],
      "prom",
    );
    expect(markers).toEqual([
      {
        timestamp: Date.parse("2026-09-06T07:30:00Z") / 1000,
        label: "ConfigMap api-config",
        kind: "change",
      },
      {
        timestamp: Date.parse("2026-09-06T08:10:00Z") / 1000,
        label: "Deployment api",
        kind: "change",
      },
      {
        timestamp: Date.parse("2026-09-06T08:11:00Z") / 1000,
        label: "Pod api-7f6-abc",
        kind: "change",
      },
    ]);
  });

  it("excludes a sibling workload's change even from a target-scoped change query", () => {
    const markers = markersFor(
      [
        [
          tool("diag", "diagnose", { ...diagnoseBundle, recentChanges: [] }),
          tool(
            "changes",
            "get_changes",
            {
              changes: [
                change(
                  "Deployment",
                  "worker",
                  "2026-09-06T08:20:00Z",
                  "apps/v1",
                ),
                change("Deployment", "api", "2026-09-06T08:10:00Z", "apps/v1"),
              ],
            },
            {
              summary: JSON.stringify({
                kind: "Deployment",
                namespace: "shop",
                name: "api",
              }),
            },
          ),
          tool("prom", "query_prometheus", rangeResult(targetSelectors)),
        ],
      ],
      "prom",
    );
    expect(markers.map((marker) => marker.label)).toEqual(["Deployment api"]);
  });

  it("ignores broader change results and other turns", () => {
    const markers = markersFor(
      [
        [tool("diag", "diagnose", diagnoseBundle)],
        [
          tool(
            "broad",
            "get_changes",
            {
              changes: [
                change("Deployment", "api", "2026-09-06T08:10:00Z", "apps/v1"),
              ],
            },
            { summary: JSON.stringify({ namespace: "shop" }) },
          ),
          tool("prom", "query_prometheus", rangeResult(targetSelectors)),
        ],
      ],
      "prom",
    );
    expect(markers).toEqual([]);
  });

  it("never marks a chart that is not about the target", () => {
    const markers = markersFor(
      [
        [
          tool("diag", "diagnose", diagnoseBundle),
          tool("prom", "query_prometheus", rangeResult([])),
        ],
      ],
      "prom",
    );
    expect(markers).toEqual([]);
  });
});
