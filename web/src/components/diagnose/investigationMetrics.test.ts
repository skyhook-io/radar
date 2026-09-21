import { describe, expect, it } from "vitest";

import {
  projectInvestigationEvidence,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import {
  metricsChangeCoverage,
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
      'sum(container_memory_working_set_bytes{namespace="shop",workload="api",workload_type="deployment"})',
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
      { label: "workload", op: "=", value: "api" },
      { label: "workload_type", op: "=", value: "deployment" },
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
        'sum(max by (pod,namespace,container) (container_memory_working_set_bytes{namespace="shop", workload="api"}))',
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

  it("marks a Helm change without an apiVersion on the subject's own kind, never on an unrelated kind", () => {
    const helmChange = {
      source: "helm",
      kind: "Deployment",
      namespace: "shop",
      name: "api",
      changeType: "upgrade",
      summary: "Helm release api upgraded to 2.4.1",
      timestamp: "2026-09-06T08:00:00Z",
    };
    const markers = markersFor(
      [
        [
          tool("diag", "diagnose", {
            ...diagnoseBundle,
            recentChanges: [
              helmChange,
              { ...helmChange, kind: "Service", changeType: "update" },
              { ...helmChange, name: "worker" },
            ],
          }),
          tool("prom", "query_prometheus", rangeResult(targetSelectors)),
        ],
      ],
      "prom",
    );
    expect(markers).toEqual([
      {
        timestamp: Date.parse("2026-09-06T08:00:00Z") / 1000,
        label: "Deployment api",
        kind: "change",
      },
    ]);
  });

  it("keeps the strict identity when a change carries an apiVersion", () => {
    const markers = markersFor(
      [
        [
          tool("diag", "diagnose", {
            ...diagnoseBundle,
            recentChanges: [
              change(
                "Deployment",
                "api",
                "2026-09-06T08:00:00Z",
                "example.io/v1",
              ),
            ],
          }),
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

describe("units only survive expressions that keep the meaning", () => {
  const bytes = [
    {
      metric: "container_memory_working_set_bytes",
      matchers: [{ label: "namespace", op: "=", value: "shop" }],
    },
  ];
  const seconds = [
    {
      metric: "http_request_duration_seconds_bucket",
      matchers: [{ label: "namespace", op: "=", value: "shop" }],
    },
  ];

  it("refuses a unit for expressions that produce a different quantity", () => {
    // Each of these is a number the metric's name no longer describes: a
    // boolean, a presence flag, a variance in bytes squared, a ratio.
    for (const query of [
      "container_memory_working_set_bytes > bool 0",
      "group(container_memory_working_set_bytes)",
      "stdvar(container_memory_working_set_bytes)",
      "stddev(container_memory_working_set_bytes)",
      "container_memory_working_set_bytes / 1024",
      "container_memory_working_set_bytes * 2",
      "count by (pod) (container_memory_working_set_bytes)",
      "absent(container_memory_working_set_bytes)",
      "changes(container_memory_working_set_bytes[5m])",
      "predict_linear(container_memory_working_set_bytes[1h], 3600)",
    ]) {
      expect(metricsUnitForExpression(query, bytes), query).toBe("");
    }
  });

  it("keeps the unit through aggregation and the counter's own change", () => {
    for (const query of [
      "sum(container_memory_working_set_bytes)",
      "max by (pod) (container_memory_working_set_bytes)",
      "sum(increase(container_memory_working_set_bytes[1h]))",
      "delta(container_memory_working_set_bytes[5m])",
      "max_over_time(container_memory_working_set_bytes[1h])",
    ]) {
      expect(metricsUnitForExpression(query, bytes), query).toBe("bytes");
    }
  });

  it("refuses a unit for a histogram's counting parts", () => {
    // A _bucket or _count series counts observations; only the `le` boundary
    // carries the measured unit, and nothing here reads it. The quantile of
    // those buckets is in seconds, but this cannot tell that from the
    // expression, so it says nothing rather than something wrong.
    expect(
      metricsUnitForExpression(
        "histogram_quantile(0.99, sum by (le) (http_request_duration_seconds_bucket))",
        seconds,
      ),
    ).toBe("");
    expect(
      metricsUnitForExpression("sum(http_request_duration_seconds_count)", [
        {
          metric: "http_request_duration_seconds_count",
          matchers: [{ label: "namespace", op: "=", value: "shop" }],
        },
      ]),
    ).toBe("");
    // The sum of the observed values does measure what was observed.
    expect(
      metricsUnitForExpression("sum(http_request_duration_seconds_sum)", [
        {
          metric: "http_request_duration_seconds_sum",
          matchers: [{ label: "namespace", op: "=", value: "shop" }],
        },
      ]),
    ).toBe("seconds");
  });

  it("refuses the shapes an axis label cannot survive", () => {
    const bytesOf = (metric: string) => [
      {
        metric,
        matchers: [{ label: "namespace", op: "=", value: "shop" }],
      },
    ];
    // A set operator mixes two expressions whose units may differ; a second
    // rate divides by time twice; a comment can hide a call from a reader and
    // from this; a string can contain anything at all.
    for (const [query, selectors] of [
      [
        "container_memory_working_set_bytes and rate(other_bytes_total[5m])",
        bytesOf("container_memory_working_set_bytes"),
      ],
      [
        "rate(container_memory_working_set_bytes[5m]) or container_memory_working_set_bytes",
        bytesOf("container_memory_working_set_bytes"),
      ],
      [
        "deriv(rate(container_memory_working_set_bytes[5m])[10m:])",
        bytesOf("container_memory_working_set_bytes"),
      ],
      [
        "container_memory_working_set_bytes-other_bytes",
        bytesOf("container_memory_working_set_bytes"),
      ],
    ] as const) {
      expect(metricsUnitForExpression(query, selectors), query).toBe("");
    }
    // A `#` inside a label value is not a comment. Reading comments before
    // strings let it swallow the rest of the query, so a dimensionless 0/1
    // and a ratio both came back in bytes.
    expect(
      metricsUnitForExpression(
        'container_memory_working_set_bytes{note="#"} > bool 0',
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("");
    expect(
      metricsUnitForExpression(
        'container_memory_working_set_bytes{note="#"} / container_memory_working_set_bytes',
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("");
    // A comment is not a call: stripping it leaves a plain metric in bytes.
    expect(
      metricsUnitForExpression(
        "container_memory_working_set_bytes # rate(",
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
    // A backtick string cannot smuggle a call past the reader either.
    expect(
      metricsUnitForExpression(
        'label_replace(container_memory_working_set_bytes, "note", `rate(`, "pod", ".*")',
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
  });

  it("keeps the unit through ordering, and drops it for an angle", () => {
    const bytesOf = (metric: string) => [
      {
        metric,
        matchers: [{ label: "namespace", op: "=", value: "shop" }],
      },
    ];
    // Sorting reorders samples without touching their values.
    for (const fn of ["sort", "sort_desc"]) {
      expect(
        metricsUnitForExpression(
          `${fn}(container_memory_working_set_bytes)`,
          bytesOf("container_memory_working_set_bytes"),
        ),
      ).toBe("bytes");
    }
    expect(
      metricsUnitForExpression(
        'sort_by_label(container_memory_working_set_bytes, "pod")',
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("bytes");
    // atan2 is spelled as a word but is a binary operator, and returns an
    // angle however many bytes went into it.
    expect(
      metricsUnitForExpression(
        "container_memory_working_set_bytes atan2 container_memory_working_set_bytes",
        bytesOf("container_memory_working_set_bytes"),
      ),
    ).toBe("");
  });

  it("still turns a rate of bytes into bytes per second and a rate of seconds into nothing", () => {
    expect(
      metricsUnitForExpression(
        "sum(rate(container_memory_working_set_bytes[5m]))",
        bytes,
      ),
    ).toBe("bytes/s");
    expect(
      metricsUnitForExpression(
        "sum(rate(container_cpu_usage_seconds_total[5m]))",
        [
          {
            metric: "container_cpu_usage_seconds_total",
            matchers: [{ label: "namespace", op: "=", value: "shop" }],
          },
        ],
      ),
    ).toBe("");
  });
});

describe("metricsChangeCoverage", () => {
  function coverageFor(
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
    return metricsChangeCoverage(projection.groups, observation);
  }

  it("reports a checked window when the turn captured changes and none landed in it", () => {
    const coverage = coverageFor(
      [
        [
          tool("diag", "diagnose", { ...diagnoseBundle, recentChanges: [] }),
          tool(
            "changes",
            "get_changes",
            {
              changes: [
                change("Deployment", "api", "2020-01-01T00:00:00Z", "apps/v1"),
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
    expect(coverage.markers).toEqual([]);
    expect(coverage.checked).toBe(true);
  });

  it("does not call the window checked when only a broader change result was captured", () => {
    const coverage = coverageFor(
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
    expect(coverage.markers).toEqual([]);
    expect(coverage.checked).toBe(false);
  });

  it("does not call the window checked when the turn captured no changes at all", () => {
    const coverage = coverageFor(
      [[tool("prom", "query_prometheus", rangeResult(targetSelectors))]],
      "prom",
    );
    expect(coverage.markers).toEqual([]);
    expect(coverage.checked).toBe(false);
  });
});
