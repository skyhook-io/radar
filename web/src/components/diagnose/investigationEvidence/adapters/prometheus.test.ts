import { describe, expect, it } from "vitest";
import {
  investigationEvidenceSubjectRef,
  metricsScope,
  projectInvestigationEvidence,
  type InvestigationEvidenceTarget,
} from "../index";
import {
  deployment,
  groupsOf,
  project,
  target,
  tool,
  workloadLogsArgs,
  workloadLogsPayload,
} from "../evidenceFixtures";

const firingInstance = {
  state: "firing",
  activeAt: "2026-09-07T07:58:00Z",
  value: "1e+00",
  labels: {
    alertname: "KubePodCrashLooping",
    namespace: "shop",
    pod: "api-68c7b766dc-fmphn",
    container: "api",
    severity: "warning",
  },
};

function alertingRule(
  patch: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    group: "kubernetes-apps",
    type: "alerting",
    name: "KubePodCrashLooping",
    query:
      'max_over_time(kube_pod_container_status_waiting_reason{reason="CrashLoopBackOff"}[5m]) >= 1',
    state: "firing",
    health: "ok",
    labels: { severity: "warning" },
    annotations: {
      summary: "Pod is crash looping.",
      description:
        "Pod {{ $labels.namespace }}/{{ $labels.pod }} ({{ $labels.container }}) is in waiting state (reason: CrashLoopBackOff).",
    },
    alerts: [firingInstance],
    ...patch,
  };
}

const rulesArgs = JSON.stringify({ state: "firing" });

/** A diagnose read that ties the given pods to the target as its log streams. */
function diagnoseWithPods(...pods: string[]) {
  return tool("diagnose-pods", "diagnose", {
    resource: deployment,
    resourceContext: { tier: "basic" },
    logsCurrent: pods.map((pod) => ({
      pod,
      container: "api",
      logs: {
        lines: ["ready"],
        totalLines: 1,
        matchedLines: 0,
        fallback: true,
      },
    })),
  });
}

describe("prometheus rules adapter", () => {
  it("attributes workload-labelled alerts in saved plural-kind Hub runs", () => {
    const projection = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("rules", "get_prometheus_rules", {
              count: 1,
              rules: [
                alertingRule({
                  alerts: [
                    {
                      state: "firing",
                      labels: { namespace: "shop", deployment: "api" },
                    },
                  ],
                }),
              ],
            }),
          ],
        },
      ],
      { ...target, kind: "deployments" },
    );

    expect(groupsOf(projection.groups, "alerts")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("relates a rule to the target through its instances, never the rule alone", () => {
    const projection = project([
      diagnoseWithPods("api-68c7b766dc-fmphn"),
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 4,
          rules: [
            alertingRule(),
            alertingRule({
              name: "KubeDeploymentReplicasMismatch",
              labels: { severity: "warning", namespace: "shop" },
              alerts: [
                {
                  state: "firing",
                  labels: {
                    namespace: "shop",
                    deployment: "api-worker",
                  },
                },
              ],
            }),
            alertingRule({
              name: "TargetDown",
              group: "general",
              labels: { severity: "critical" },
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "monitoring", job: "node-exporter" },
                },
              ],
            }),
            alertingRule({
              name: "SiblingCrash",
              alerts: [
                {
                  state: "firing",
                  labels: {
                    namespace: "shop",
                    pod: "api-worker-5d8f7c9b6-x2k9p",
                  },
                },
              ],
            }),
          ],
        },
        { summary: rulesArgs },
      ),
    ]);
    const alerts = groupsOf(projection.groups, "alerts");
    expect(alerts.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
      "broader",
      "broader",
    ]);
    expect(alerts.map((group) => group.latest.tier)).toEqual([
      "supporting",
      "context",
      "context",
      "context",
    ]);
    expect(alerts[0].latest.tone).toBe("alert");
    expect(alerts[2].latest.tone).toBe("error");
    expect(alerts[0].latest.title).toBe("KubePodCrashLooping firing");
    expect(alerts[0].latest.summary).toBe(
      "1 active instance, 1 naming Deployment shop/api · kubernetes-apps",
    );
    expect(alerts[0].latest.data).toMatchObject({
      type: "alerts",
      rule: { group: "kubernetes-apps", state: "firing", health: "ok" },
      instances: [{ namesTarget: true, value: "1e+00" }],
      annotations: { summary: "Pod is crash looping." },
    });
    expect(alerts[3].latest.data).toMatchObject({
      instances: [{ namesTarget: false }],
    });
    expect(
      groupsOf(projection.groups, "receipt").filter((group) =>
        group.identity.startsWith("alerts:"),
      ),
    ).toHaveLength(0);
    expect(
      projection.limitations.filter((limitation) =>
        limitation.source.startsWith("Alert"),
      ),
    ).toHaveLength(0);
  });

  it("uses rule-level labels only as producer-related context", () => {
    const projection = project([
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 1,
          rules: [
            alertingRule({
              name: "ApiDown",
              labels: {
                severity: "warning",
                namespace: "shop",
                deployment: "api",
              },
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "shop", instance: "10.0.0.4:8080" },
                },
              ],
            }),
          ],
        },
        { summary: rulesArgs },
      ),
    ]);
    const alert = groupsOf(projection.groups, "alerts")[0].latest;
    expect(alert.relevance).toBe("producer-related");
    expect(alert.tier).toBe("supporting");
    expect(alert.summary).toBe("1 active instance · kubernetes-apps");
    expect(groupsOf(projection.groups, "receipt")).toHaveLength(0);
  });

  it("matches workload labels per kind and pod labels only through producer-established pods", () => {
    const run = (
      target: InvestigationEvidenceTarget,
      labels: object,
      capturedPods: string[] = [],
    ) =>
      groupsOf(
        projectInvestigationEvidence(
          [
            {
              timeline: [
                ...(capturedPods.length > 0
                  ? [
                      tool("diagnose-pods", "diagnose", {
                        resource: {
                          ...deployment,
                          kind: target.kind,
                          metadata: {
                            namespace: target.namespace,
                            name: target.name,
                          },
                        },
                        resourceContext: { tier: "basic" },
                        logsCurrent: capturedPods.map((pod) => ({
                          pod,
                          container: "app",
                          logs: {
                            lines: ["ready"],
                            totalLines: 1,
                            matchedLines: 0,
                            fallback: true,
                          },
                        })),
                      }),
                    ]
                  : []),
                tool(
                  "rules",
                  "get_prometheus_rules",
                  {
                    count: 1,
                    rules: [
                      alertingRule({ alerts: [{ state: "firing", labels }] }),
                    ],
                  },
                  { summary: rulesArgs },
                ),
              ],
            },
          ],
          target,
        ).groups,
        "alerts",
      )[0].latest.relevance;
    const statefulSet = { ...target, kind: "StatefulSet", name: "db" };
    expect(run(target, { namespace: "shop", deployment: "api" })).toBe(
      "target",
    );
    expect(run(target, { namespace: "other", deployment: "api" })).toBe(
      "broader",
    );
    expect(run(target, { namespace: "shop", statefulset: "api" })).toBe(
      "broader",
    );
    expect(
      run(target, {
        namespace: "shop",
        workload: "api",
        workload_type: "deployment",
      }),
    ).toBe("target");
    expect(
      run(target, {
        namespace: "shop",
        workload: "api",
        workload_type: "statefulset",
      }),
    ).toBe("broader");
    // A controller-shaped pod name proves nothing on its own: a sibling
    // ReplicaSet `api-worker` produces `api-worker-abcde` too.
    expect(
      run(target, { namespace: "shop", pod: "api-68c7b766dc-fmphn" }),
    ).toBe("broader");
    expect(
      run(target, { namespace: "shop", pod: "api-68c7b766dc-fmphn" }, [
        "api-68c7b766dc-fmphn",
      ]),
    ).toBe("target");
    expect(
      run(target, { namespace: "shop", pod: "api-worker-abcde" }, [
        "api-68c7b766dc-fmphn",
      ]),
    ).toBe("broader");
    expect(run(statefulSet, { namespace: "shop", pod: "db-0" }, ["db-0"])).toBe(
      "target",
    );
    expect(
      run(statefulSet, { namespace: "other", pod: "db-0" }, ["db-0"]),
    ).toBe("broader");
    expect(
      run(target, { pod: "api-68c7b766dc-fmphn" }, ["api-68c7b766dc-fmphn"]),
    ).toBe("broader");
  });

  it("issues a scoped receipt only for a complete, unfiltered, evaluated active-state query", () => {
    const unrelated = alertingRule({
      name: "TargetDown",
      alerts: [{ state: "firing", labels: { namespace: "monitoring" } }],
    });
    const receipt = (
      payload: Record<string, unknown>,
      args: Record<string, unknown>,
    ) =>
      groupsOf(
        project([
          tool("rules", "get_prometheus_rules", payload, {
            summary: JSON.stringify(args),
          }),
        ]).groups,
        "receipt",
      );

    const firing = receipt(
      { count: 1, rules: [unrelated] },
      { state: "firing" },
    );
    expect(firing).toHaveLength(1);
    expect(firing[0].latest).toMatchObject({
      tier: "checked",
      relevance: "target",
      title: "No firing alerts matched this Deployment",
      summary: "Deployment shop/api",
      data: {
        type: "receipt",
        checked: "alerts",
        scope: "Deployment shop/api",
        message:
          "1 alerting rule returned (filter: state=firing), every instance in another namespace; none names Deployment shop/api.",
      },
    });

    expect(
      receipt(
        {
          count: 0,
          rules: [],
          note: "no rules matched — drop filters to see what exists, or the backend may have no rules configured",
        },
        { state: "pending" },
      )[0].latest.data,
    ).toMatchObject({
      message: "0 alerting rules returned (filter: state=pending).",
    });

    // A name or group filter never looked at the other rules.
    expect(
      receipt(
        { count: 1, rules: [unrelated] },
        { state: "firing", name: "Target" },
      ),
    ).toHaveLength(0);
    expect(
      receipt({ count: 0, rules: [] }, { state: "firing", group: "general" }),
    ).toHaveLength(0);
    // An instance this projection cannot place is not evidence of absence.
    expect(
      receipt(
        {
          count: 1,
          rules: [
            alertingRule({
              name: "Vague",
              alerts: [
                { state: "firing", labels: { instance: "10.0.0.4:9100" } },
              ],
            }),
          ],
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        {
          count: 1,
          rules: [
            alertingRule({
              name: "SameNamespace",
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "shop", pod: "other-abc" },
                },
              ],
            }),
          ],
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(receipt({ count: 1, rules: [unrelated] }, {})).toHaveLength(0);
    expect(
      receipt({ count: 1, rules: [unrelated] }, { state: "inactive" }),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [alertingRule({ type: "recording", name: "x" })] },
        { type: "record", state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        {
          count: 1,
          rules: [unrelated],
          truncated: true,
          note: "narrow with name, group, state, or type filters",
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: "err" }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: "degraded" }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: undefined }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
  });

  it("advances the card through alert state transitions and keeps distinct rules apart", () => {
    const firingForTarget = alertingRule();
    const resolved = alertingRule({ state: "inactive", alerts: [] });
    const firingElsewhere = alertingRule({
      alerts: [
        {
          state: "firing",
          labels: { namespace: "shop", pod: "api-worker-5d8f7c9b6-x2k9p" },
        },
      ],
    });
    const run = (...later: Record<string, unknown>[]) =>
      groupsOf(
        project(
          [
            diagnoseWithPods("api-68c7b766dc-fmphn"),
            tool(
              "rules-1",
              "get_prometheus_rules",
              { count: 1, rules: [firingForTarget] },
              { summary: rulesArgs },
            ),
          ],
          [
            tool(
              "rules-2",
              "get_prometheus_rules",
              { count: later.length, rules: later },
              { summary: JSON.stringify({}) },
            ),
          ],
        ).groups,
        "alerts",
      );

    const [resolvedGroup] = run(resolved);
    expect(resolvedGroup.observations).toHaveLength(2);
    expect(resolvedGroup.latest).toMatchObject({
      title: "KubePodCrashLooping inactive",
      relevance: "target",
      tier: "context",
      tone: "neutral",
      summary: "No active instances · kubernetes-apps",
    });
    expect(resolvedGroup.latest).toBe(resolvedGroup.chronologicalLatest);
    expect(resolvedGroup.historical).toBe(false);

    const [movedGroup] = run(firingElsewhere);
    expect(movedGroup.observations).toHaveLength(2);
    expect(movedGroup.latest.relevance).toBe("broader");
    expect(movedGroup.latest.summary).toBe(
      "1 active instance · kubernetes-apps",
    );

    const distinct = groupsOf(
      project([
        tool(
          "rules",
          "get_prometheus_rules",
          {
            count: 2,
            rules: [
              alertingRule({ labels: { severity: "warning", team: "a" } }),
              alertingRule({
                query: "vector(1)",
                labels: { severity: "critical", team: "b" },
              }),
            ],
          },
          { summary: rulesArgs },
        ),
      ]).groups,
      "alerts",
    );
    expect(distinct).toHaveLength(2);
    expect(distinct.every((group) => group.observations.length === 1)).toBe(
      true,
    );

    // Identical definitions from different rule files arrive as identical
    // rows: two rules this read cannot tell apart, so neither inherits the
    // other's history on a later read.
    const twin = () => alertingRule({ alerts: [], state: "inactive" });
    const twins = groupsOf(
      project(
        [
          diagnoseWithPods("api-68c7b766dc-fmphn"),
          tool(
            "rules-1",
            "get_prometheus_rules",
            { count: 2, rules: [alertingRule(), twin()] },
            { summary: rulesArgs },
          ),
        ],
        [
          tool(
            "rules-2",
            "get_prometheus_rules",
            { count: 2, rules: [alertingRule(), twin()] },
            { summary: rulesArgs },
          ),
        ],
      ).groups,
      "alerts",
    );
    expect(twins).toHaveLength(4);
    expect(twins.every((group) => group.observations.length === 1)).toBe(true);
    expect(twins.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
      "target",
      "broader",
    ]);
    expect(twins.map((group) => group.latest.title)).toEqual([
      "KubePodCrashLooping firing",
      "KubePodCrashLooping inactive",
      "KubePodCrashLooping firing",
      "KubePodCrashLooping inactive",
    ]);
  });

  it("never proves absence by namespace for a cluster-scoped target", () => {
    const node = { kind: "Node", group: "", name: "worker-1" };
    const run = (rules: Record<string, unknown>[]) =>
      groupsOf(
        projectInvestigationEvidence(
          [
            {
              timeline: [
                tool(
                  "rules",
                  "get_prometheus_rules",
                  { count: rules.length, rules },
                  { summary: rulesArgs },
                ),
              ],
            },
          ],
          node,
        ).groups,
        "receipt",
      );
    expect(
      run([
        alertingRule({
          name: "KubeNodeNotReady",
          alerts: [
            {
              state: "firing",
              labels: { node: "worker-1", namespace: "kube-system" },
            },
          ],
        }),
      ]),
    ).toHaveLength(0);
    expect(run([])).toHaveLength(1);
    expect(run([])[0].latest.title).toBe("No firing alerts matched this Node");
  });

  it("keeps recording rules off the card list and flags truncation and evaluation health", () => {
    const projection = project([
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 3,
          rules: [
            alertingRule({
              type: "recording",
              name: "namespace:container_cpu_usage_seconds_total:sum_rate",
              alerts: undefined,
              state: undefined,
            }),
            alertingRule({ health: "err", alerts: [] }),
            alertingRule({
              name: "KubeContainerWaiting",
              state: "inactive",
              health: "unknown",
              alerts: [],
            }),
          ],
          truncated: true,
          note: "narrow with name, group, state, or type filters",
        },
        { summary: rulesArgs },
      ),
    ]);
    const alerts = groupsOf(projection.groups, "alerts");
    expect(alerts.map((group) => group.latest.title)).toEqual([
      "KubePodCrashLooping firing",
      "KubeContainerWaiting inactive",
    ]);
    expect(alerts[1].latest).toMatchObject({
      tier: "context",
      tone: "neutral",
      relevance: "broader",
      summary: "No active instances · kubernetes-apps",
    });
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Alert rules", "truncated"],
      ["Alert rule KubePodCrashLooping", "error"],
      ["Alert rule KubeContainerWaiting", "unknown"],
    ]);
    expect(projection.limitations[0].message).toContain(
      "narrow with name, group, state, or type filters",
    );
    expect(groupsOf(projection.groups, "receipt")).toHaveLength(0);
  });

  it("does not let sampled values churn revision history, and rejects malformed rules", () => {
    const later = {
      ...firingInstance,
      value: "2e+00",
      activeAt: "2026-09-07T08:10:00Z",
    };
    const projection = project(
      [
        tool(
          "rules-1",
          "get_prometheus_rules",
          { count: 1, rules: [alertingRule()] },
          { summary: rulesArgs },
        ),
      ],
      [
        tool(
          "rules-2",
          "get_prometheus_rules",
          { count: 1, rules: [alertingRule({ alerts: [later] })] },
          { summary: rulesArgs },
        ),
      ],
    );
    const group = groupsOf(projection.groups, "alerts")[0];
    expect(group.observations).toHaveLength(2);
    expect(group.observations[1].changedFromPrevious).toBe(false);

    const malformed = project([
      tool(
        "rules-bad",
        "get_prometheus_rules",
        { count: 1, rules: [{ name: "NoGroup", type: "alerting" }] },
        { summary: rulesArgs },
      ),
      tool(
        "rules-bad-instance",
        "get_prometheus_rules",
        { count: 1, rules: [alertingRule({ alerts: [{ labels: {} }] })] },
        { summary: rulesArgs },
      ),
    ]);
    expect(malformed.groups).toHaveLength(0);
    expect(malformed.limitations).toHaveLength(1);
    expect(malformed.limitations[0].sources).toHaveLength(2);
    expect(malformed.limitations[0].source).toBe("Alert rules");

    const envelope = project([
      tool(
        "rules-count",
        "get_prometheus_rules",
        { count: 2, rules: [alertingRule()] },
        { summary: rulesArgs },
      ),
      tool(
        "rules-truncated",
        "get_prometheus_rules",
        { count: 0, rules: [], truncated: "true" },
        { summary: rulesArgs },
      ),
      tool(
        "rules-no-query",
        "get_prometheus_rules",
        { count: 1, rules: [alertingRule({ query: undefined })] },
        { summary: rulesArgs },
      ),
    ]);
    expect(envelope.groups).toHaveLength(0);
    expect(envelope.limitations[0].sources).toHaveLength(3);
  });
});

describe("query_prometheus evidence", () => {
  const rangeSeries = [
    {
      labels: { pod: "api-7f6-abc" },
      dataPoints: [
        { timestamp: 1_788_679_043, value: 169_377_792 },
        { timestamp: 1_788_679_068, value: 171_401_216 },
      ],
    },
  ];
  // The pods a diagnose bundle established as the target's own, so an agent
  // query naming exactly them is target evidence. A prefix over the same
  // namespace is not: "api-.*" also selects api-worker's pods.
  const establishedPods = ["api-7f6-abc", "api-7f6-def"];
  const podSetPattern = `^(${establishedPods.join("|")})$`;
  const targetSelectors = [
    {
      metric: "container_memory_working_set_bytes",
      matchers: [
        { label: "namespace", op: "=", value: "shop" },
        { label: "pod", op: "=~", value: podSetPattern },
      ],
    },
  ];
  const prefixSelectors = [
    {
      metric: "container_memory_working_set_bytes",
      matchers: [
        { label: "namespace", op: "=", value: "shop" },
        { label: "pod", op: "=~", value: "api-.*" },
      ],
    },
  ];
  // A diagnose of the target that lists its pods, so a later query can be
  // proved to be about them.
  function podsBundle() {
    return tool(
      "diag-pods",
      "diagnose",
      { resource: deployment, pods: 2, podNames: establishedPods },
      {
        summary: JSON.stringify({
          kind: "Deployment",
          namespace: "shop",
          name: "api",
        }),
      },
    );
  }
  function promResult(patch: Record<string, unknown> = {}) {
    return {
      query: `sum(container_memory_working_set_bytes{namespace="shop",pod=~"${podSetPattern}"})`,
      type: "range",
      start: "2026-09-06T07:17:23Z",
      end: "2026-09-06T09:17:23Z",
      step: "25s",
      resultType: "matrix",
      seriesCount: 1,
      series: rangeSeries,
      selectors: targetSelectors,
      ...patch,
    };
  }

  it("keeps an unrecognised metric's own name as the title", () => {
    const projection = project([
      tool(
        "prom",
        "query_prometheus",
        promResult({
          query: 'sum(acme_widget_queue_depth{namespace="shop"})',
          selectors: [
            {
              metric: "acme_widget_queue_depth",
              matchers: [{ label: "namespace", op: "=", value: "shop" }],
            },
          ],
        }),
      ),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.title).toBe("acme_widget_queue_depth");
    // No family label means the summary keeps its generic lead-in rather than
    // repeating the name already in the title.
    expect(group.latest.summary).toContain("Prometheus · 1 series");
  });

  it("keeps the generic title when the selectors name no metric", () => {
    const projection = project([
      tool(
        "prom",
        "query_prometheus",
        promResult({
          query: 'sum({namespace="shop"})',
          selectors: [
            {
              metric: "",
              matchers: [{ label: "namespace", op: "=", value: "shop" }],
            },
          ],
        }),
      ),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.title).toBe("Prometheus metrics");
    expect(group.latest.summary).toBe("1 series · 2h window · 25s step");
  });

  it("charts a range query scoped to the target as target evidence", () => {
    const projection = project([
      podsBundle(),
      tool("prom", "query_prometheus", promResult(), {
        summary: JSON.stringify({ query: "sum(...)", type: "range" }),
      }),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group).toBeDefined();
    expect(group.latest.relevance).toBe("target");
    expect(group.latest.tier).toBe("supporting");
    expect(group.latest.tone).toBe("neutral");
    expect(group.latest.title).toBe("Memory working set · Deployment shop/api");
    expect(group.latest.summary).toBe(
      "container_memory_working_set_bytes · 1 series · 2h window · 25s step",
    );
    const data = group.latest.data;
    if (data.type !== "metrics") throw new Error("expected metrics");
    expect(data.mode).toBe("range");
    expect(data.origin).toBe("query");
    expect(data.subject).toEqual({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
    });
    expect(data.unit).toBe("bytes");
    expect(data.series).toEqual(rangeSeries);
    expect(investigationEvidenceSubjectRef(data)).toEqual(data.subject);
    // The chart and the diagnose bundle that established its pods.
    expect(projection.coverage.projected).toBe(2);
  });

  it("claims a unit only for a single bare metric that states one", () => {
    const projection = project([
      tool(
        "bare",
        "query_prometheus",
        promResult({
          query:
            'container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"}',
        }),
      ),
      tool(
        "rate",
        "query_prometheus",
        promResult({
          query:
            'rate(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-.*"}[5m])',
          selectors: [
            {
              metric: "container_cpu_usage_seconds_total",
              matchers: targetSelectors[0].matchers,
            },
          ],
        }),
      ),
    ]);
    const units = groupsOf(projection.groups, "metrics").map((group) =>
      group.latest.data.type === "metrics" ? group.latest.data.unit : "?",
    );
    expect(units).toEqual(["bytes", ""]);
  });

  it("renders an instant query as values, not a chart", () => {
    const projection = project([
      tool(
        "instant",
        "query_prometheus",
        promResult({
          type: "instant",
          start: undefined,
          end: undefined,
          step: undefined,
          resultType: "vector",
          series: [
            {
              labels: { pod: "api-7f6-abc" },
              dataPoints: [{ timestamp: 1_788_679_068, value: 3 }],
            },
          ],
        }),
      ),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.title).toBe("Memory working set");
    expect(group.latest.summary).toBe(
      "container_memory_working_set_bytes · 1 series",
    );
    expect(group.latest.data.type === "metrics" && group.latest.data.mode).toBe(
      "instant",
    );
  });

  it("reports a truncated result as a limitation with its cardinality, never as a chart", () => {
    const projection = project([
      tool(
        "big",
        "query_prometheus",
        promResult({
          series: [],
          truncated: true,
          summary: {
            seriesCount: 812,
            totalDataPoints: 194_880,
            labelCardinality: { pod: 812, container: 3 },
            suggestion: "topk(5, ...)",
          },
          note: "Result exceeded the 96 KiB cap.",
        }),
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    expect(projection.limitations).toHaveLength(1);
    expect(projection.limitations[0].kind).toBe("truncated");
    expect(projection.limitations[0].message).toContain("812 series");
    expect(projection.limitations[0].message).toContain(
      "812 distinct pod values",
    );
    expect(projection.limitations[0].message).toContain("Result exceeded");
    expect(projection.coverage.limited).toBe(1);
  });

  it("keeps a query that matched nothing as a fact about the window", () => {
    const projection = project([
      tool("none", "query_prometheus", promResult({ series: [] })),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.summary).toBe(
      "container_memory_working_set_bytes · No series matched · 2h window · 25s step",
    );
  });

  it("classifies scope from the producer's selectors, never from the expression text", () => {
    const withNamespace = (
      metric: string,
      extra: Array<{ label: string; op: string; value: string }> = [],
    ) => ({
      metric,
      matchers: [{ label: "namespace", op: "=", value: "shop" }, ...extra],
    });
    const cases: Array<[string, Record<string, unknown>, string]> = [
      ["namespace only", { selectors: [withNamespace("up")] }, "broader"],
      [
        "workload label",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=", value: "api" },
            ]),
          ],
        },
        "target",
      ],
      [
        "pod named by the workload's prefix, never established",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=", value: "api-7f6-abc" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "owner join over the namespace",
        {
          selectors: [
            withNamespace("kube_pod_owner", [
              { label: "owner_kind", op: "=", value: "ReplicaSet" },
              { label: "owner_name", op: "=~", value: "^(api-7f6)$" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "container alone",
        {
          selectors: [
            withNamespace("container_memory_working_set_bytes", [
              { label: "container", op: "=", value: "api" },
            ]),
          ],
        },
        "broader",
      ],
      [
        "sibling workload",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=", value: "worker" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "pod regex with an optional dash",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=~", value: "api-?.*" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "pod regex alternation behind the target prefix",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=~", value: "api-.*|worker-.*" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "workload alternation",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=~", value: "api|worker" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "statefulset label on a deployment target",
        {
          selectors: [
            withNamespace("kube_statefulset_status_replicas_ready", [
              { label: "statefulset", op: "=", value: "api" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "braced target numerator over a bare cluster denominator",
        {
          query:
            'sum(container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"}) / scalar(sum(machine_memory_bytes))',
          selectors: [
            ...targetSelectors,
            { metric: "machine_memory_bytes", matchers: [] },
          ],
        },
        "broader",
      ],
      [
        "other namespace",
        {
          selectors: [
            {
              metric: "up",
              matchers: [
                { label: "namespace", op: "=", value: "other" },
                { label: "pod", op: "=~", value: "api-.*" },
              ],
            },
          ],
        },
        "broader",
      ],
      ["empty selectors", { selectors: [] }, "broader"],
      [
        "unknown selectors",
        { selectors: targetSelectors, selectorsUnknown: true },
        "broader",
      ],
    ];
    for (const [name, patch, expected] of cases) {
      const projection = project([
        tool(`case-${name}`, "query_prometheus", promResult(patch)),
      ]);
      const [group] = groupsOf(projection.groups, "metrics");
      expect(group?.latest.relevance, name).toBe(expected);
      expect(
        group?.latest.data.type === "metrics"
          ? group.latest.data.subject
          : "missing",
        name,
      ).toEqual(
        expected === "target"
          ? {
              kind: "Deployment",
              group: "apps",
              namespace: "shop",
              name: "api",
            }
          : undefined,
      );
      expect(group?.latest.tier, name).toBe(
        expected === "broader" ? "context" : "supporting",
      );
    }
  });

  // The diagnose prompt tells the agent to scope a query as `pod=~'^(a|b)$'`.
  // A Pod target has to recognise its own name written that way, or following
  // our own instruction demotes the target's own series.
  it("accepts the anchored set form the prompt asks for on a Pod target", () => {
    const podTarget = {
      kind: "Pod",
      group: "",
      namespace: "shop",
      name: "api-7f6-abc",
    };
    const podSelector = (value: string) => [
      {
        metric: "container_cpu_usage_seconds_total",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "pod", op: "=~", value },
        ],
      },
    ];
    expect(metricsScope(podTarget, podSelector("api-7f6-abc"), false)).toBe(
      "target",
    );
    expect(metricsScope(podTarget, podSelector("^(api-7f6-abc)$"), false)).toBe(
      "target",
    );
    // A set that also names a neighbour is not evidence about this pod alone,
    // and a prefix still selects pods this one does not stand for.
    expect(
      metricsScope(
        podTarget,
        podSelector("^(api-7f6-abc|api-7f6-def)$"),
        false,
      ),
    ).toBe("producer-related");
    expect(metricsScope(podTarget, podSelector("api-7f6-.*"), false)).toBe(
      "producer-related",
    );
  });

  it("treats an unescaped dot in a workload regex as naming more than the target", () => {
    const dotted = { ...target, name: "api.v2" };
    const selector = (value: string) => [
      {
        metric: "kube_deployment_status_replicas_available",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "deployment", op: "=~", value },
        ],
      },
    ];
    expect(metricsScope(dotted, selector("api.v2"), false)).toBe(
      "producer-related",
    );
    expect(metricsScope(dotted, selector("api\\.v2"), false)).toBe("target");
    expect(
      metricsScope(
        dotted,
        [
          {
            metric: "up",
            matchers: [
              { label: "namespace", op: "=", value: "shop" },
              { label: "pod", op: "=~", value: "api\\.v2-.*" },
            ],
          },
        ],
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        dotted,
        [
          {
            metric: "up",
            matchers: [
              { label: "namespace", op: "=", value: "shop" },
              { label: "pod", op: "=~", value: "api.v2-.*" },
            ],
          },
        ],
        false,
      ),
    ).toBe("producer-related");
  });

  it("names a Pod target only by its exact name and accepts an anchored namespace regex", () => {
    const pod = { ...target, kind: "Pod", group: "", name: "api-7f6-abc" };
    const selector = (
      namespace: { op: string; value: string },
      podMatcher: { op: string; value: string },
    ) => [
      {
        metric: "container_memory_working_set_bytes",
        matchers: [
          { label: "namespace", ...namespace },
          { label: "pod", ...podMatcher },
        ],
      },
    ];
    const exactNs = { op: "=", value: "shop" };
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=~", value: "api-7f6-abc-.*" }),
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=", value: "api-7f6-abc" }),
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=~", value: "api-7f6-abc" }),
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=", value: "api-7f6-abc-extra" }),
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        target,
        selector({ op: "=~", value: "shop" }, { op: "=~", value: "api-.*" }),
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        target,
        selector(
          { op: "=~", value: "shop" },
          {
            op: "=~",
            value: "^(api-7f6-abc)$",
          },
        ),
        false,
        new Set(["api-7f6-abc"]),
      ),
    ).toBe("target");
    expect(
      metricsScope(
        target,
        selector(
          { op: "=~", value: "shop|other" },
          { op: "=~", value: "api-.*" },
        ),
        false,
      ),
    ).toBe("broader");
    expect(
      metricsScope(
        target,
        selector(
          { op: "!=", value: "kube-system" },
          { op: "=~", value: "api-.*" },
        ),
        false,
      ),
    ).toBe("broader");
  });

  // Membership can also be established by a workload-logs read, which the
  // main pass adapts into groups as it goes. Classifying a metrics query
  // against those groups while they are still being built made the verdict
  // depend on which tool the agent happened to call first: the same facts,
  // read in the other order, demoted the same chart.
  it("classifies a metrics query the same whichever order the agent worked in", () => {
    const loggedPod = "api-68c7b766dc-fmphn";
    const exactLoggedPod = {
      metric: "container_memory_working_set_bytes",
      matchers: [
        { label: "namespace", op: "=", value: "shop" },
        { label: "pod", op: "=~", value: `^(${loggedPod})$` },
      ],
    };
    const logs = () =>
      tool("wl-logs", "get_workload_logs", workloadLogsPayload, {
        summary: workloadLogsArgs,
      });
    const metrics = () =>
      tool(
        "prom",
        "query_prometheus",
        promResult({
          selectors: [exactLoggedPod],
          query: `sum(container_memory_working_set_bytes{namespace="shop",pod=~"^(${loggedPod})$"})`,
        }),
      );

    const logsFirst = groupsOf(project([logs(), metrics()]).groups, "metrics");
    const metricsFirst = groupsOf(
      project([metrics(), logs()]).groups,
      "metrics",
    );
    expect(logsFirst[0].latest.relevance).toBe("target");
    expect(metricsFirst[0].latest.relevance).toBe(
      logsFirst[0].latest.relevance,
    );

    // Across turns too: the read that names the pod may arrive in a later one.
    const acrossTurns = groupsOf(
      project([metrics()], [logs()]).groups,
      "metrics",
    );
    expect(acrossTurns[0].latest.relevance).toBe("target");
  });

  it("proves pod membership from the bundle instead of the workload's name", () => {
    const withBundle = project([
      podsBundle(),
      tool("prom", "query_prometheus", promResult()),
    ]);
    expect(groupsOf(withBundle.groups, "metrics")[0].latest.relevance).toBe(
      "target",
    );

    // The same exact set with no bundle to establish it, and the prefix that
    // would also select a sibling workload's pods: kept, never promoted.
    const unproven = project([tool("prom", "query_prometheus", promResult())]);
    expect(groupsOf(unproven.groups, "metrics")[0].latest.relevance).toBe(
      "producer-related",
    );
    const prefixed = project([
      podsBundle(),
      tool(
        "prom",
        "query_prometheus",
        promResult({ selectors: prefixSelectors }),
      ),
    ]);
    const prefixGroup = groupsOf(prefixed.groups, "metrics")[0];
    expect(prefixGroup.latest.relevance).toBe("producer-related");
    expect(
      prefixGroup.latest.data.type === "metrics" &&
        prefixGroup.latest.data.subject,
    ).toBeUndefined();

    // A subset of the established pods is still about the target.
    const subset = project([
      podsBundle(),
      tool(
        "prom",
        "query_prometheus",
        promResult({
          selectors: [
            {
              metric: "container_memory_working_set_bytes",
              matchers: [
                { label: "namespace", op: "=", value: "shop" },
                { label: "pod", op: "=", value: "api-7f6-abc" },
              ],
            },
          ],
        }),
      ),
    ]);
    expect(groupsOf(subset.groups, "metrics")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("proves membership whatever order the agent ran its tools in", () => {
    // The agent may know the pod names from an earlier turn and chart them
    // before it calls diagnose. The same facts must read the same way.
    const queryFirst = project([
      tool("prom", "query_prometheus", promResult()),
      podsBundle(),
    ]);
    expect(groupsOf(queryFirst.groups, "metrics")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("will not establish target pods from a diagnose that never confirmed success", () => {
    // This set is what proves a Prometheus selector is about the target, so it
    // takes confirmed success — the same test the sources use — rather than
    // merely the absence of an error.
    const unconfirmed = {
      ...podsBundle(),
      isError: undefined as unknown as boolean,
    };
    const projection = project([
      unconfirmed,
      tool(
        "prom",
        "query_prometheus",
        promResult({
          query: `sum(container_memory_working_set_bytes{namespace="shop",pod=~"${podSetPattern}"})`,
          selectors: [
            {
              metric: "container_memory_working_set_bytes",
              matchers: [
                { label: "namespace", op: "=", value: "shop" },
                { label: "pod", op: "=~", value: podSetPattern },
              ],
            },
          ],
        }),
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")[0].latest.relevance).not.toBe(
      "target",
    );
  });

  it("does not let a generic workload label claim another kind's series", () => {
    const workloadSelectors = (type?: string) => [
      {
        metric: "container_cpu_usage_seconds_total",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "workload", op: "=", value: "api" },
          ...(type ? [{ label: "workload_type", op: "=", value: type }] : []),
        ],
      },
    ];
    const asDeployment = project([
      tool(
        "prom",
        "query_prometheus",
        promResult({ selectors: workloadSelectors("deployment") }),
      ),
    ]);
    expect(groupsOf(asDeployment.groups, "metrics")[0].latest.relevance).toBe(
      "target",
    );
    // A StatefulSet also called api in the same namespace is a different
    // workload, and the series says so.
    const asStatefulSet = project([
      tool(
        "prom",
        "query_prometheus",
        promResult({ selectors: workloadSelectors("statefulset") }),
      ),
    ]);
    expect(groupsOf(asStatefulSet.groups, "metrics")[0].latest.relevance).toBe(
      "producer-related",
    );

    // The same exclusion written as a regex. Reading only `=` let a sibling's
    // chart take the target's identity and its change markers.
    const regexSelectors = (value: string) => [
      {
        metric: "container_cpu_usage_seconds_total",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "workload", op: "=", value: "api" },
          { label: "workload_type", op: "=~", value },
        ],
      },
    ];
    for (const [value, relevance] of [
      ["statefulset", "producer-related"],
      ["^(statefulset)$", "producer-related"],
      // A set that includes the target kind could be the target, so it is no
      // conflict; an inexact regex proves nothing either way.
      ["^(deployment|statefulset)$", "target"],
      [".*", "target"],
    ] as const) {
      const projection = project([
        tool(
          "prom",
          "query_prometheus",
          promResult({ selectors: regexSelectors(value) }),
        ),
      ]);
      expect(groupsOf(projection.groups, "metrics")[0].latest.relevance).toBe(
        relevance,
      );
    }
  });

  it("refuses an unescaped dot as proof that a pod set names the target", () => {
    const dotted = ["api.v2-0"];
    const bundle = tool(
      "diag-dotted",
      "diagnose",
      { resource: deployment, pods: 1, podNames: dotted },
      {
        summary: JSON.stringify({
          kind: "Deployment",
          namespace: "shop",
          name: "api",
        }),
      },
    );
    const podSelector = (value: string) => [
      {
        metric: "container_memory_working_set_bytes",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "pod", op: "=~", value },
        ],
      },
    ];
    // `^(api.v2-0)$` also selects api-v2-0, so it names more than the pod it
    // appears to and cannot prove membership.
    const wildcard = project([
      bundle,
      tool(
        "prom",
        "query_prometheus",
        promResult({ selectors: podSelector("^(api.v2-0)$") }),
      ),
    ]);
    expect(groupsOf(wildcard.groups, "metrics")[0].latest.relevance).toBe(
      "producer-related",
    );
    const escaped = project([
      bundle,
      tool(
        "prom",
        "query_prometheus",
        promResult({ selectors: podSelector("^(api\\.v2-0)$") }),
      ),
    ]);
    expect(groupsOf(escaped.groups, "metrics")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("rejects a result without the producer's selector inventory", () => {
    const projection = project([
      tool("old", "query_prometheus", promResult({ selectors: undefined })),
      tool(
        "bad-series",
        "query_prometheus",
        promResult({
          series: [{ labels: {}, dataPoints: [{ timestamp: "x", value: 1 }] }],
        }),
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    expect(projection.limitations.map((item) => item.kind)).toEqual([
      "unknown",
    ]);
    expect(projection.limitations[0].source).toBe("Prometheus query");
    expect(projection.limitations[0].sources).toHaveLength(2);
  });

  it("merges the same query into one card with revisions", () => {
    const projection = project(
      [tool("first", "query_prometheus", promResult())],
      [
        tool(
          "second",
          "query_prometheus",
          promResult({
            series: [
              {
                labels: { pod: "api-7f6-abc" },
                dataPoints: [{ timestamp: 1_788_679_043, value: 1 }],
              },
            ],
          }),
        ),
      ],
    );
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups).toHaveLength(1);
    expect(groups[0].observations).toHaveLength(2);
    expect(groups[0].observations[1].changedFromPrevious).toBe(true);
  });
});
