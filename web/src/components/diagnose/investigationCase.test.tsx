import { renderToStaticMarkup } from "react-dom/server";
import { AgentClaimNote, AgentRoleChip } from "./AgentCase";
import { describe, expect, it, vi } from "vitest";

import {
  InvestigationEvidencePane,
  investigationCaseByGroup,
  investigationEvidenceShouldRevealHistory,
  partitionInvestigationEvidence,
} from "./InvestigationEvidencePane";
import {
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceTimelineItem,
  type InvestigationRootCauseEvidenceResolution,
} from "./investigationEvidence";
import {
  resolveInvestigationCase,
  type InvestigationCaseResolution,
  investigationCaseItemsStillRendered,
  mergeInvestigationCases,
  type InvestigationCaseItem,
} from "./investigationCase";
import { AssessmentSources, ResultCard, assessmentSourceRows } from "./parts";
import type { Diagnosis, DiagnosisEvidenceItem } from "../../api/diagnose";

const onViewSource = vi.fn();
const target = {
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
};

function evidenceRef(scope: string, nonce: string): string {
  return `ev_${scope.repeat(26)}_${nonce.repeat(26)}`;
}

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
    summary: JSON.stringify({
      kind: "deployment",
      namespace: "shop",
      name: "api",
    }),
    result: JSON.stringify(result),
    isError: false,
    radarEvidence: true,
    ...patch,
  };
}

function project(
  ...timeline: InvestigationEvidenceTimelineItem[]
): InvestigationEvidenceProjection {
  return projectInvestigationEvidence([{ timeline }], target);
}

function render(
  projection: InvestigationEvidenceProjection,
  investigationCase?: InvestigationCaseResolution,
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution,
): string {
  return renderToStaticMarkup(
    <InvestigationEvidencePane
      projection={projection}
      rootCauseEvidence={rootCauseEvidence}
      investigationCase={investigationCase}
      collecting={false}
      animateGroupIds={new Set()}
      onViewSource={onViewSource}
      onViewActivity={() => {}}
    />,
  );
}

function linked(
  ref: string,
  role: DiagnosisEvidenceItem["role"],
  claim: string,
  subject?: DiagnosisEvidenceItem["subject"],
): DiagnosisEvidenceItem {
  return {
    status: "linked",
    ref,
    role,
    claim,
    ...(subject ? { subject } : {}),
  };
}

const deployment = {
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { namespace: "shop", name: "api" },
  status: { readyReplicas: 0, replicas: 1 },
};

const criticalIssue = {
  id: "issue-api-crash",
  severity: "critical",
  source: "problem",
  category: "crashloop",
  category_group: "runtime",
  grouping_scope: "workload",
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
  reason: "CrashLoopBackOff",
  message: "The API container keeps restarting.",
};

function logs(lines: string[], fallback = false) {
  return {
    lines,
    totalLines: lines.length,
    matchedLines: lines.length,
    fallback,
  };
}

// One diagnose bundle fanning out into a resource card, an issue card, two
// current log streams and one previous log stream of the same container.
const diagnoseBundle = {
  resource: deployment,
  resourceContext: {
    tier: "diagnostic",
    issueSummary: {
      count: 1,
      highestSeverity: "critical",
      topReason: "CrashLoopBackOff",
    },
  },
  pods: 1,
  relatedIssues: [criticalIssue],
  logsCurrent: [
    { pod: "api-abc", container: "api", logs: logs(["ERROR auth failed"]) },
    { pod: "api-abc", container: "proxy", logs: logs(["proxy ready"], true) },
  ],
  logsPrevious: [
    {
      pod: "api-abc",
      container: "api",
      logs: logs(["FATAL: MongoServerError: Authentication failed"]),
    },
  ],
  events: [
    {
      reason: "BackOff",
      message: "Back-off restarting failed container",
      type: "Warning",
      count: 4,
      lastTimestamp: "2026-09-02T10:00:00Z",
    },
  ],
};

describe("agent case placement (D-1, D-1b)", () => {
  const ref = evidenceRef("a", "b");
  const projection = project(
    tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
  );

  it("places a subject-less item on a fan-out result beside its source, never on a card", () => {
    const resolved = resolveInvestigationCase(
      projection,
      { evidence: [linked(ref, "cause", "Everything in this bundle.")] },
      0,
    );
    expect(resolved.items).toHaveLength(1);
    expect(resolved.items[0].placement).toBe("source");
    expect(resolved.items[0].groupId).toBeUndefined();
    const html = render(projection, resolved);
    expect(html).not.toContain("Everything in this bundle.");
    expect(html).not.toContain("data-agent-claim");
  });

  it("pins a subject-bearing item to exactly the observation it names", () => {
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "symptom", "The issue card is the symptom.", {
            kind: "Deployment",
            group: "apps",
            namespace: "shop",
            name: "api",
            observation: "issue",
          }),
          linked(ref, "context", "The Deployment itself looks ordinary.", {
            kind: "deployments",
            name: "api",
            observation: "resource",
          }),
          linked(ref, "context", "Events only repeat the back-off.", {
            kind: "Deployment",
            name: "api",
            observation: "events",
          }),
          linked(
            ref,
            "context",
            "Ambiguous: two observations share this subject.",
            {
              kind: "Deployment",
              name: "api",
            },
          ),
        ],
      },
      0,
    );
    const byKind = Object.fromEntries(
      resolved.items.map((item) => [
        item.claim,
        {
          placement: item.placement,
          kind: projection.groups.find((group) => group.id === item.groupId)
            ?.kind,
        },
      ]),
    );
    expect(byKind).toEqual({
      "The issue card is the symptom.": { placement: "card", kind: "issue" },
      "The Deployment itself looks ordinary.": {
        placement: "card",
        kind: "resource",
      },
      "Events only repeat the back-off.": { placement: "card", kind: "events" },
      "Ambiguous: two observations share this subject.": {
        placement: "source",
        kind: undefined,
      },
    });
    const html = render(projection, resolved);
    expect(html).toContain("The issue card is the symptom.");
    expect(html).toContain("Events only repeat the back-off.");
    expect(html).not.toContain("Ambiguous: two observations");
    expect(html).toContain(">Symptom<");
    expect(html).toContain(">Context<");
  });

  it("distinguishes current from previous logs of one container by stream", () => {
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "cause", "The previous instance died on auth.", {
            kind: "Pod",
            namespace: "shop",
            name: "api-abc",
            container: "api",
            stream: "previous",
          }),
          linked(ref, "symptom", "The current instance retries.", {
            kind: "Pod",
            name: "api-abc",
            container: "api",
            stream: "current",
          }),
          linked(ref, "context", "Which stream? Both match.", {
            kind: "Pod",
            name: "api-abc",
            container: "api",
          }),
          linked(ref, "context", "Only one proxy stream exists.", {
            kind: "Pod",
            name: "api-abc",
            container: "proxy",
          }),
        ],
      },
      0,
    );
    const identities = resolved.items.map((item) => [
      item.placement,
      projection.groups.find((group) => group.id === item.groupId)?.identity,
    ]);
    expect(identities).toEqual([
      ["card", "logs:previous:api-abc:api"],
      ["card", "logs:current:api-abc:api"],
      ["source", undefined],
      ["card", "logs:current:api-abc:proxy"],
    ]);
  });

  it("binds to the exact earlier read and renders on its revision row, not the card head", () => {
    const first = evidenceRef("a", "c");
    const second = evidenceRef("a", "d");
    const twice = project(
      tool(
        "read-1",
        "get_resource",
        { ...deployment, status: { readyReplicas: 0, replicas: 1 } },
        { evidenceRef: first },
      ),
      tool(
        "read-2",
        "get_resource",
        { ...deployment, status: { readyReplicas: 1, replicas: 1 } },
        { evidenceRef: second },
      ),
    );
    expect(twice.groups).toHaveLength(1);
    const resolved = resolveInvestigationCase(
      twice,
      {
        evidence: [
          linked(first, "cause", "At first read no replica was ready."),
        ],
        ruledOut: [{ hypothesis: "Replicas never recover", evidenceIndex: 0 }],
      },
      0,
    );
    expect(resolved.items[0].placement).toBe("revision");
    expect(resolved.items[0].observation?.source.stepId).toBe("read-1");
    const html = render(twice, resolved);
    const card = html.slice(html.indexOf("<article"));
    const head = card.slice(0, card.indexOf("Previous observations"));
    expect(head).not.toContain("At first read no replica was ready.");
    expect(head).not.toContain(">Cause<");
    const history = card.slice(card.indexOf("Previous observations"));
    expect(history).toContain("At first read no replica was ready.");
    expect(history).toContain(">Cause<");
    expect(history).toContain(">Initial<");
    expect(history).not.toContain("Used for assessment");
    expect(html).toContain("(earlier observation)");
  });

  it("keeps an identical earlier read on its own revision row, never folded into the card", () => {
    const first = evidenceRef("a", "c");
    const twice = project(
      tool("read-1", "get_resource", deployment, { evidenceRef: first }),
      tool("read-2", "get_resource", deployment),
    );
    const resolved = resolveInvestigationCase(
      twice,
      { evidence: [linked(first, "context", "Same both times.")] },
      0,
    );
    expect(resolved.items[0].placement).toBe("revision");
    const html = render(twice, resolved);
    expect(html).toContain("Previous observations · 1");
    expect(html).toContain("Same both times.");
    expect(html).not.toContain("Used for assessment");
    // A reveal aimed at the identical earlier read must open the history that
    // holds its row, which display equivalence alone would keep closed.
    const sourceId = resolved.items[0].observation!.source.id;
    expect(
      investigationEvidenceShouldRevealHistory(twice.groups[0], sourceId),
    ).toBe(false);
    expect(
      investigationEvidenceShouldRevealHistory(
        twice.groups[0],
        sourceId,
        new Set([sourceId]),
      ),
    ).toBe(true);
  });

  it("keeps every pinned duplicate read as its own row with its own claim", () => {
    const first = evidenceRef("a", "c");
    const second = evidenceRef("a", "d");
    const reads = project(
      tool("read-1", "get_resource", deployment, { evidenceRef: first }),
      tool("read-2", "get_resource", deployment, { evidenceRef: second }),
      tool("read-3", "get_resource", {
        ...deployment,
        status: { readyReplicas: 1, replicas: 1 },
      }),
    );
    const resolved = resolveInvestigationCase(
      reads,
      {
        evidence: [
          linked(first, "symptom", "First read: nothing ready."),
          linked(second, "symptom", "Second read: still nothing ready."),
        ],
      },
      0,
    );
    expect(resolved.items.map((item) => item.placement)).toEqual([
      "revision",
      "revision",
    ]);
    const html = render(reads, resolved);
    expect(html).toContain("Previous observations · 2");
    expect(html).toContain("First read: nothing ready.");
    expect(html).toContain("Second read: still nothing ready.");
  });

  it("requires every discriminator both sides state to agree", () => {
    const cases: Array<[DiagnosisEvidenceItem["subject"], string]> = [
      [{ kind: "Deployment", group: "apps", name: "api" }, "card"],
      [{ kind: "Deployment", group: "", name: "api" }, "source"],
      [{ kind: "Deployment", group: "extensions", name: "api" }, "source"],
      [{ kind: "Deployment", namespace: "shop", name: "api" }, "card"],
      [{ kind: "Deployment", namespace: "other", name: "api" }, "source"],
      [{ kind: "Deployment", name: "nope" }, "source"],
    ];
    const single = project(
      tool("read", "get_resource", deployment, { evidenceRef: ref }),
    );
    for (const [subject, placement] of cases) {
      const resolved = resolveInvestigationCase(
        single,
        { evidence: [linked(ref, "context", "c", subject)] },
        0,
      );
      expect([subject, resolved.items[0].placement]).toEqual([
        subject,
        placement,
      ]);
    }
    // A core Service must not accept a Knative Service subject.
    const service = project(
      tool(
        "svc",
        "get_resource",
        {
          apiVersion: "v1",
          kind: "Service",
          metadata: { namespace: "shop", name: "api" },
        },
        { evidenceRef: ref },
      ),
    );
    expect(
      resolveInvestigationCase(
        service,
        {
          evidence: [
            linked(ref, "cause", "knative", {
              kind: "Service",
              group: "serving.knative.dev",
              namespace: "shop",
              name: "api",
            }),
          ],
        },
        0,
      ).items[0].placement,
    ).toBe("source");
    // Omitting the container on a multi-container pod is ambiguous.
    expect(
      resolveInvestigationCase(
        projection,
        {
          evidence: [
            linked(ref, "context", "which container?", {
              kind: "Pod",
              name: "api-abc",
              stream: "current",
            }),
          ],
        },
        0,
      ).items[0].placement,
    ).toBe("source");
  });
});

describe("agent case visibility and ordering (D-2, D-5)", () => {
  const ref = evidenceRef("a", "b");

  it("keeps a Key adverse card in main when labelled demoted or rules_out, and cannot recolor it", () => {
    const projection = project(
      tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
    );
    for (const role of ["demoted", "rules_out"] as const) {
      const resolved = resolveInvestigationCase(
        projection,
        {
          evidence: [
            linked(
              ref,
              role,
              "The restart loop is downstream of the auth failure.",
              {
                kind: "Deployment",
                name: "api",
                observation: "issue",
              },
            ),
          ],
        },
        0,
      );
      const partition = partitionInvestigationEvidence(
        projection.groups,
        undefined,
        resolved,
      );
      const issue = projection.groups.find((group) => group.kind === "issue")!;
      expect(partition.collectionByGroup.get(issue.id)).toBe("main");
      expect(issue.latest.tone).toBe("error");
      expect(issue.latest.tier).toBe("key");
      const html = render(projection, resolved);
      expect(html).toContain("border-l-red-500");
      expect(html).toContain(
        "The restart loop is downstream of the auth failure.",
      );
    }
  });

  it("orders main by role: cause items in agent order, unlabelled in Radar order, demoted last", () => {
    const issueRef = evidenceRef("a", "c");
    const logsRef = evidenceRef("a", "d");
    const eventsRef = evidenceRef("a", "e");
    const projection = project(
      tool(
        "issues",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        {
          summary: JSON.stringify({ namespace: "shop" }),
          evidenceRef: issueRef,
        },
      ),
      tool(
        "events",
        "get_events",
        { events: diagnoseBundle.events },
        {
          summary: JSON.stringify({
            namespace: "shop",
            kind: "Pod",
            name: "api-abc",
          }),
          evidenceRef: eventsRef,
        },
      ),
      tool(
        "logs",
        "get_pod_logs",
        {
          lines: ["FATAL: MongoServerError: Authentication failed"],
          totalLines: 1,
          matchedLines: 1,
          fallback: false,
        },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-abc",
            container: "api",
            previous: true,
          }),
          evidenceRef: logsRef,
        },
      ),
    );
    const kinds = (
      partition: ReturnType<typeof partitionInvestigationEvidence>,
    ) => partition.main.map((group) => group.kind);
    expect(kinds(partitionInvestigationEvidence(projection.groups))).toEqual([
      "issue",
    ]);
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(logsRef, "cause", "The password is rejected."),
          linked(eventsRef, "cause", "Kubelet backs off after each failure."),
          linked(
            issueRef,
            "demoted",
            "The crash loop only restates the log line.",
          ),
        ],
      },
      0,
    );
    expect(
      kinds(
        partitionInvestigationEvidence(projection.groups, undefined, resolved),
      ),
    ).toEqual(["logs", "events", "issue"]);
    // Unlabelled and ruled-out cards sit between symptom and context; a
    // demoted Key card goes last even though Radar would lead with it.
    const framed = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(issueRef, "demoted", "Downstream of the auth failure."),
          linked(logsRef, "context", "Checked the previous instance too."),
          linked(eventsRef, "rules_out", "No image pull or scheduling event."),
        ],
      },
      0,
    );
    expect(
      kinds(
        partitionInvestigationEvidence(projection.groups, undefined, framed),
      ),
    ).toEqual(["events", "logs", "issue"]);
    const html = render(projection, resolved);
    expect(html.indexOf("The password is rejected.")).toBeLessThan(
      html.indexOf("Kubelet backs off"),
    );
    expect(html.indexOf("Kubelet backs off")).toBeLessThan(
      html.indexOf("only restates the log line"),
    );
    expect(html).toContain(">Less relevant<");
  });

  it("promotes a broader row only when the agent's subject named it", () => {
    const broadRef = evidenceRef("a", "f");
    const projection = project(
      tool(
        "broad",
        "issues",
        {
          issues: [
            { ...criticalIssue, id: "db", name: "db", namespace: "other" },
          ],
          total: 1,
          total_matched: 1,
        },
        {
          summary: JSON.stringify({ namespace: "other" }),
          evidenceRef: broadRef,
        },
      ),
    );
    const subjectless = resolveInvestigationCase(
      projection,
      {
        evidence: [linked(broadRef, "cause", "The one row in this query.")],
        ruledOut: [{ hypothesis: "Dead link otherwise", evidenceIndex: 0 }],
      },
      0,
    );
    expect(subjectless.items[0].placement).toBe("card");
    expect(
      partitionInvestigationEvidence(projection.groups, undefined, subjectless)
        .collectionByGroup.size,
    ).toBe(0);
    expect(render(projection, subjectless)).not.toContain(
      "Dead link otherwise",
    );
    const named = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(broadRef, "cause", "db in other is the upstream failure.", {
            kind: "Deployment",
            namespace: "other",
            name: "db",
          }),
        ],
      },
      0,
    );
    const partition = partitionInvestigationEvidence(
      projection.groups,
      undefined,
      named,
    );
    expect(partition.main.map((group) => group.kind)).toEqual(["issue"]);
    expect(render(projection, named)).toContain("other/db");
  });

  it("selects placed groups of any role alongside legacy links", () => {
    const eventsRef = evidenceRef("a", "e");
    const projection = project(
      tool(
        "issues",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        {
          summary: JSON.stringify({ namespace: "shop" }),
        },
      ),
      tool(
        "events",
        "get_events",
        { events: diagnoseBundle.events },
        {
          summary: JSON.stringify({
            namespace: "shop",
            kind: "Pod",
            name: "api-abc",
          }),
          evidenceRef: eventsRef,
        },
      ),
    );
    const resolved = resolveInvestigationCase(
      projection,
      { evidence: [linked(eventsRef, "context", "Checked, only back-off.")] },
      0,
    );
    const partition = partitionInvestigationEvidence(
      projection.groups,
      undefined,
      resolved,
    );
    expect(partition.main.map((group) => group.kind)).toEqual([
      "issue",
      "events",
    ]);
    expect(investigationCaseByGroup(resolved).size).toBe(1);
  });
});

describe("diagnose-bundled metrics placement (D-5 with D1)", () => {
  const ref = evidenceRef("a", "m");
  const window = {
    start: "2026-09-06T07:00:00Z",
    end: "2026-09-06T08:00:00Z",
    step: "1m",
  };
  const samples = [
    { timestamp: Date.parse(window.start) / 1000, value: 1 },
    { timestamp: Date.parse(window.end) / 1000, value: 2 },
  ];
  const vital = (category: string) => ({
    category,
    unit: category === "cpu" ? "cores" : "bytes",
    query: `sum(${category}{namespace='shop',pod=~'^(api-abc)$'})`,
    series: [{ labels: {}, dataPoints: samples }],
  });
  const bundleWith = (categories: string[]) =>
    project(
      tool(
        "diag",
        "diagnose",
        {
          ...diagnoseBundle,
          metrics: {
            window,
            pods: 1,
            series: categories.map(vital),
          },
        },
        { evidenceRef: ref },
      ),
    );

  it("promotes a diagnose-origin chart into main only through a subject that names it", () => {
    const projection = bundleWith(["cpu", "memory"]);
    const metrics = projection.groups.filter(
      (group) => group.kind === "metrics",
    );
    expect(metrics).toHaveLength(2);
    expect(metrics[0].latest.data).toMatchObject({
      type: "metrics",
      origin: "diagnose",
    });
    const uncited = partitionInvestigationEvidence(projection.groups);
    expect(uncited.main.map((group) => group.kind)).not.toContain("metrics");
    expect(
      uncited.workload.filter((group) => group.kind === "metrics"),
    ).toHaveLength(2);

    const ambiguous = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "symptom", "Memory climbs until the restart.", {
            kind: "Deployment",
            namespace: "shop",
            name: "api",
            observation: "metrics",
          }),
        ],
      },
      0,
    );
    expect(ambiguous.items[0].placement).toBe("source");

    const named = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "symptom", "Memory climbs until the restart.", {
            kind: "Deployment",
            group: "apps",
            namespace: "shop",
            name: "api",
            observation: "metrics:memory",
          }),
        ],
      },
      0,
    );
    expect(named.items[0].placement).toBe("card");
    expect(named.items[0].groupId).toBe(
      metrics.find((group) => group.identity.endsWith(":memory"))!.id,
    );
    const partition = partitionInvestigationEvidence(
      projection.groups,
      undefined,
      named,
    );
    expect(partition.collectionByGroup.get(named.items[0].groupId!)).toBe(
      "main",
    );
    // The chart is now in main and, as the only symptom item, leads the
    // unlabelled Radar cards; the other chart stays where Radar had it.
    expect(partition.main[0].kind).toBe("metrics");
    expect(
      partition.main.filter((group) => group.kind === "metrics"),
    ).toHaveLength(1);
    expect(partition.main.map((group) => group.kind)).toContain("issue");
    const html = render(projection, named);
    expect(html).toContain("Memory working set · deployment shop/api");
    expect(html).toContain("Memory climbs until the restart.");
  });

  it("needs no category when the bundle captured one chart", () => {
    const projection = bundleWith(["cpu"]);
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "context", "CPU is flat.", {
            kind: "Deployment",
            name: "api",
            observation: "metrics",
          }),
        ],
      },
      0,
    );
    expect(resolved.items[0].placement).toBe("card");
    expect(
      resolveInvestigationCase(
        projection,
        {
          evidence: [
            linked(ref, "context", "Wrong chart.", {
              kind: "Deployment",
              name: "api",
              observation: "metrics:restarts",
            }),
          ],
        },
        0,
      ).items[0].placement,
    ).toBe("source");
  });
});

describe("follow-up answers that cite evidence", () => {
  const first = evidenceRef("a", "b");
  const second = evidenceRef("c", "d");
  const window = { start: "2026-09-06T07:00:00Z", end: "2026-09-06T09:00:00Z" };
  const chart = {
    query:
      "sum(container_memory_working_set_bytes{namespace='shop',pod=~'^api-.*',container='api'})",
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
    selectors: [
      {
        metric: "container_memory_working_set_bytes",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "pod", op: "=~", value: "^api-.*" },
          { label: "container", op: "=", value: "api" },
        ],
      },
    ],
  };
  const projection = projectInvestigationEvidence(
    [
      {
        timeline: [
          tool("diag", "diagnose", diagnoseBundle, { evidenceRef: first }),
        ],
      },
      {
        timeline: [
          tool("prom", "query_prometheus", chart, {
            summary: JSON.stringify({ query: chart.query }),
            evidenceRef: second,
          }),
        ],
        question:
          "chart this workload's memory over the last 2 hours and cite it",
      },
    ],
    target,
  );

  it("lets the answer turn's placed chart select into main with its chip", () => {
    const chartGroup = projection.groups.find(
      (group) => group.kind === "metrics",
    )!;
    expect(chartGroup.latest.source.turnIndex).toBe(1);
    expect(
      partitionInvestigationEvidence(projection.groups).collectionByGroup.get(
        chartGroup.id,
      ),
    ).toBe("workload");
    const answerCase = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(second, "context", "Memory is flat across the window.", {
            kind: "Deployment",
            group: "apps",
            namespace: "shop",
            name: "api",
            container: "api",
            observation: "metrics:memory",
          }),
        ],
      },
      1,
    );
    expect(answerCase.items[0].placement).toBe("card");
    expect(answerCase.items[0].groupId).toBe(chartGroup.id);
    // The chart states no resource (its selectors only pattern-match the
    // pods), so a subject without an evidence kind cannot name it.
    expect(
      resolveInvestigationCase(
        projection,
        {
          evidence: [
            linked(second, "context", "No kind given.", {
              kind: "Deployment",
              name: "api",
            }),
          ],
        },
        1,
      ).items[0].placement,
    ).toBe("source");
    const partition = partitionInvestigationEvidence(
      projection.groups,
      undefined,
      answerCase,
    );
    expect(partition.collectionByGroup.get(chartGroup.id)).toBe("main");
    const html = render(projection, answerCase);
    expect(html).toContain("Memory is flat across the window.");
    expect(html).toContain(">Context<");
  });

  it("keeps a malformed subject at its source instead of treating it as omitted", () => {
    // Omitting a subject asks Radar to place the note wherever the cited call
    // produced; supplying a broken one asked for something specific. Falling
    // back to the omitted behaviour put the note on whichever observation
    // happened to be the only one for that source.
    const omitted = resolveInvestigationCase(
      projection,
      { evidence: [linked(second, "context", "Flat.")] },
      1,
    );
    expect(omitted.items[0].placement).toBe("card");

    const malformed = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(second, "context", "Flat.", {
            name: "api",
          } as unknown as Parameters<typeof linked>[3]),
        ],
      },
      1,
    );
    expect(malformed.items[0].placement).toBe("source");
    expect(malformed.items[0].groupId).toBeUndefined();
  });

  it("places a log claim that names the diagnosed workload and its container", () => {
    const assessmentCase = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(first, "demoted", "The ERROR lines are routes elsewhere.", {
            kind: "Deployment",
            group: "apps",
            namespace: "shop",
            name: "api",
            container: "api",
            stream: "current",
            observation: "logs",
          }),
        ],
      },
      0,
    );
    expect(assessmentCase.items[0].placement).toBe("card");
    expect(
      projection.groups.find(
        (group) => group.id === assessmentCase.items[0].groupId,
      )?.identity,
    ).toBe("logs:current:api-abc:api");
  });

  it("lists an answer turn's sources under the answer", () => {
    const answerCase = resolveInvestigationCase(
      projection,
      { evidence: [linked(second, "context", "Flat.")] },
      1,
    );
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          rootCause: "",
          report: "Memory is flat.",
          remediation: [],
        }}
        followup
        assessmentSources={
          <AssessmentSources
            investigationCase={answerCase}
            onViewSource={onViewSource}
          />
        }
      />,
    );
    expect(html).toContain("Assessment details");
    expect(html).toContain("Sources used for this assessment");
    expect(html).toContain("1 agent note on an evidence card");
    expect(html).not.toContain(">Context<");
  });
});

describe("carrying earlier assessments' card notes", () => {
  const item = (
    index: number,
    groupId: string | undefined,
    placement: "card" | "revision" | "source",
    role: "cause" | "context" | "demoted" = "context",
  ) =>
    ({
      index,
      role,
      claim: `claim ${index}`,
      source: { id: `s${index}` },
      placement,
      groupId,
    }) as unknown as InvestigationCaseItem;

  it("keeps an earlier turn's note on a card the live turn did not address", () => {
    const live = {
      items: [item(0, "chart", "card")],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const first = {
      items: [
        item(0, "logs", "card", "demoted"),
        item(1, "chart", "card", "cause"),
        item(2, undefined, "source"),
        item(3, "deploy", "revision"),
      ],
      ruledOut: [{ hypothesis: "x" }],
    } as unknown as InvestigationCaseResolution;
    const merged = mergeInvestigationCases(live, [first]);
    expect(merged?.items.map((entry) => [entry.groupId, entry.role])).toEqual([
      ["chart", "context"],
      ["logs", "demoted"],
      ["deploy", "context"],
    ]);
    expect(merged?.ruledOut).toEqual([]);
  });

  it("lets the newer earlier turn win a card and leaves a lone live case untouched", () => {
    const live = {
      items: [],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const newer = {
      items: [item(0, "logs", "card", "context")],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const older = {
      items: [item(0, "logs", "card", "demoted"), item(1, "pod", "card")],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const merged = mergeInvestigationCases(live, [newer, older]);
    expect(merged?.items.map((entry) => [entry.groupId, entry.role])).toEqual([
      ["logs", "context"],
      ["pod", "context"],
    ]);
    expect(mergeInvestigationCases(live, [])).toBe(live);
    expect(mergeInvestigationCases(undefined, [])).toBeUndefined();
  });

  it("recognises its own surviving notes across a re-resolved merge", () => {
    // The pane feeds the merge freshly resolved copies of every turn, so the
    // assessment's own items arrive equal but not identical. An identity test
    // reported them as gone, which silently retired the conflict banner's
    // reframing whenever any unrelated follow-up arrived.
    const assessmentItems = [
      item(0, "logs", "card", "demoted"),
      item(1, "crash", "card", "demoted"),
    ];
    const reResolved = assessmentItems.map(
      (entry) => ({ ...entry }) as InvestigationCaseItem,
    );
    expect(reResolved[0]).not.toBe(assessmentItems[0]);
    expect(
      investigationCaseItemsStillRendered(assessmentItems, reResolved).map(
        (entry) => entry.groupId,
      ),
    ).toEqual(["logs", "crash"]);
    // A note the merge dropped is correctly reported as gone.
    expect(
      investigationCaseItemsStillRendered(assessmentItems, [reResolved[0]]).map(
        (entry) => entry.groupId,
      ),
    ).toEqual(["logs"]);
    expect(investigationCaseItemsStillRendered(assessmentItems, [])).toEqual(
      [],
    );
  });

  it("carries every note the winning assessment left on a card, not just the first", () => {
    // A card note and a note pinned to a superseded read are two readings of
    // one group by the same assessment. Taking coverage per item dropped the
    // second, so a visible Cause note could vanish behind a revision note.
    const live = {
      items: [item(0, "other", "card", "context")],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const winner = {
      items: [
        item(1, "logs", "revision", "context"),
        item(2, "logs", "card", "cause"),
      ],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const older = {
      items: [item(3, "logs", "card", "demoted")],
      ruledOut: [],
    } as unknown as InvestigationCaseResolution;
    const merged = mergeInvestigationCases(live, [winner, older]);
    expect(
      merged?.items.map((entry) => [
        entry.groupId,
        entry.placement,
        entry.role,
      ]),
    ).toEqual([
      ["other", "card", "context"],
      ["logs", "revision", "context"],
      ["logs", "card", "cause"],
    ]);
  });
});

describe("agent case robustness", () => {
  const ref = evidenceRef("a", "b");

  it("renders a healthy assessment's context items and sources", () => {
    const projection = project(
      tool(
        "read",
        "get_resource",
        { ...deployment, status: { readyReplicas: 1, replicas: 1 } },
        {
          evidenceRef: ref,
        },
      ),
    );
    const diagnosis = {
      healthy: true,
      rootCause: "",
      report: "Looks fine.",
      remediation: [],
      evidence: [linked(ref, "context", "One replica, ready, no restarts.")],
    } satisfies Diagnosis;
    const resolved = resolveInvestigationCase(projection, diagnosis, 0);
    expect(resolved.items[0].placement).toBe("card");
    const partition = partitionInvestigationEvidence(
      projection.groups,
      undefined,
      resolved,
    );
    expect(partition.main.map((group) => group.kind)).toEqual(["resource"]);
    const html = render(projection, resolved);
    expect(html).toContain("One replica, ready, no restarts.");
    expect(html).toContain(">Context<");
    const sources = renderToStaticMarkup(
      <AssessmentSources
        investigationCase={resolved}
        onViewSource={onViewSource}
      />,
    );
    expect(sources).toContain("Sources used for this assessment");
    expect(sources).toContain("1 agent note on an evidence card");
    expect(sources).not.toContain(">Context<");
    expect(sources).not.toContain("One replica, ready, no restarts.");
  });

  it("renders legacy refs without a case exactly as before", () => {
    const projection = project(
      tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const legacyDiagnosis: Diagnosis = {
      rootCause: "auth",
      report: "",
      remediation: [],
      rootCauseEvidence: { status: "linked", refs: [ref] },
    };
    const resolved = resolveInvestigationCase(projection, legacyDiagnosis, 0);
    expect(resolved).toEqual({ items: [], ruledOut: [] });
    expect(render(projection, undefined, resolution)).toBe(
      render(projection, resolved, resolution),
    );
    expect(
      partitionInvestigationEvidence(projection.groups, resolution).main.map(
        (group) => group.id,
      ),
    ).toEqual(
      partitionInvestigationEvidence(
        projection.groups,
        resolution,
        resolved,
      ).main.map((group) => group.id),
    );
    const legacySources = renderToStaticMarkup(
      <AssessmentSources resolution={resolution} onViewSource={onViewSource} />,
    );
    expect(legacySources).toContain(
      '<div class="font-medium text-theme-text-primary">Diagnose</div>',
    );
    expect(legacySources).not.toContain("agent note");
    expect(legacySources).not.toContain("data-source-placed-claims");
    expect(
      renderToStaticMarkup(
        <AssessmentSources
          resolution={resolution}
          onViewSource={onViewSource}
        />,
      ),
    ).toBe(
      renderToStaticMarkup(
        <AssessmentSources
          resolution={resolution}
          investigationCase={resolved}
          onViewSource={onViewSource}
        />,
      ),
    );
  });

  it("keeps valid items when siblings are unlinked, malformed, foreign, or from another turn", () => {
    const stale = evidenceRef("a", "z");
    const projection = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("old", "get_resource", deployment, { evidenceRef: stale }),
          ],
        },
        {
          timeline: [
            tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
          ],
        },
      ],
      target,
    );
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          { status: "unlinked" },
          linked(ref, "cause", "Valid, on the issue.", {
            kind: "Deployment",
            name: "api",
            observation: "issue",
          }),
          linked("not-a-ref", "cause", "Bad ref."),
          {
            status: "linked",
            ref,
            role: "verdict" as never,
            claim: "Bad role.",
          },
          linked(stale, "cause", "Previous turn."),
          linked(ref, "context", "Bad subject drops to source.", {
            kind: "Pod",
            name: "api-abc",
            stream: "older" as never,
          }),
        ],
        ruledOut: [
          { hypothesis: "Points at the unlinked slot", evidenceIndex: 0 },
          { hypothesis: "Points past the end", evidenceIndex: 9 },
          { hypothesis: "Points at a source-placed item", evidenceIndex: 5 },
          { hypothesis: "Kept", evidenceIndex: 1 },
        ],
      },
      1,
    );
    expect(resolved.items.map((item) => [item.index, item.placement])).toEqual([
      [1, "card"],
      [5, "source"],
    ]);
    expect(resolved.ruledOut.map((entry) => entry.hypothesis)).toEqual([
      "Kept",
    ]);
    const duplicated = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "rules_out", "Once.", {
            kind: "Deployment",
            name: "api",
            observation: "issue",
          }),
        ],
        ruledOut: [
          { hypothesis: "Twice", evidenceIndex: 0 },
          { hypothesis: "Twice", evidenceIndex: 0 },
        ],
      },
      1,
    );
    expect(duplicated.ruledOut).toHaveLength(1);
    const html = render(projection, resolved);
    expect(html).toContain('data-testid="investigation-ruled-out"');
    expect(html).toContain("Kept");
    expect(html).not.toContain("Points at a source-placed item");
    expect(html).toContain("See CrashLoopBackOff");
  });

  it("shows a source-placed claim in Assessment details with its role", () => {
    const projection = project(
      tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
    );
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "rules_out", "Nothing in this bundle points at DNS."),
        ],
      },
      0,
    );
    const rows = assessmentSourceRows(undefined, resolved);
    expect(rows).toHaveLength(1);
    expect(rows[0].items[0].placement).toBe("source");
    const html = renderToStaticMarkup(
      <AssessmentSources
        investigationCase={resolved}
        onViewSource={onViewSource}
      />,
    );
    expect(html).toContain("Nothing in this bundle points at DNS.");
    expect(html).toContain(">Rules out<");
    expect(html).toContain("data-source-placed-claims");
    expect(render(projection, resolved)).not.toContain("points at DNS");
  });

  it("lists an earlier assessment's placed claims read-only with their observation", () => {
    const projection = project(
      tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
    );
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [
          linked(ref, "cause", "Was the cause back then.", {
            kind: "Deployment",
            name: "api",
            observation: "issue",
          }),
        ],
      },
      0,
    );
    const current = renderToStaticMarkup(
      <AssessmentSources
        investigationCase={resolved}
        onViewSource={onViewSource}
      />,
    );
    expect(current).not.toContain("Was the cause back then.");
    const earlier = renderToStaticMarkup(
      <AssessmentSources
        investigationCase={resolved}
        readOnly
        onViewSource={onViewSource}
      />,
    );
    expect(earlier).toContain("Notes from this assessment");
    const withCode = renderToStaticMarkup(
      <AgentClaimNote claim="The `REDIS_PASSWORD` key is unset; `envFrom` still mounts it." />,
    );
    expect(withCode).toContain('<code class="inline-code');
    expect(withCode).toContain(">REDIS_PASSWORD</code>");
    expect(withCode).toContain(">envFrom</code>");
    expect(withCode).not.toContain("`");
    expect(
      renderToStaticMarkup(<AgentClaimNote claim="Unbalanced `tick here." />),
    ).toContain("Unbalanced `tick here.");
    expect(earlier).toContain(
      "CrashLoopBackOff · </span>Was the cause back then.",
    );
    expect(earlier).toContain(">Cause<");
    expect(earlier).toContain("Agent&#x27;s note:");
  });

  it("lists an unpinnable ruled-out hypothesis nowhere", () => {
    const projection = project(
      tool("diag", "diagnose", diagnoseBundle, { evidenceRef: ref }),
    );
    const resolved = resolveInvestigationCase(
      projection,
      {
        evidence: [linked(ref, "rules_out", "Bundle-wide claim.")],
        ruledOut: [{ hypothesis: "DNS is broken", evidenceIndex: 0 }],
      },
      0,
    );
    expect(resolved.ruledOut).toEqual([]);
    expect(render(projection, resolved)).not.toContain("DNS is broken");
  });
});

describe("the agent's contribution is one attributed row", () => {
  it("keeps the role inside the note rather than beside the resource title", () => {
    const html = renderToStaticMarkup(
      <AgentClaimNote role="cause" claim="The sealed value is stale." />,
    );
    // Sparkle, role and sentence share one row, so the role is as plainly the
    // agent's as the sentence is. Beside the title it sat in the same slot as
    // Radar's own badges.
    const row = html.match(/<p[^>]*data-agent-claim[\s\S]*<\/p>/)?.[0] ?? "";
    expect(row).toContain("Cause");
    expect(row).toContain("Agent&#x27;s note:");
    expect(row).toContain("The sealed value is stale.");
  });

  it("names the hypothesis a rules-out card excludes", () => {
    const html = renderToStaticMarkup(
      <AgentClaimNote
        role="rules_out"
        excludes="The Atlas user was deleted"
        claim="Same user authenticates fine here."
      />,
    );
    expect(html).toContain("Rules out: The Atlas user was deleted");
    // Without the hypothesis the reader had to find it in a separate block.
    const bare = renderToStaticMarkup(
      <AgentClaimNote role="rules_out" claim="Same user authenticates fine." />,
    );
    expect(bare).toContain("Rules out");
    expect(bare).not.toContain("Rules out:");
  });

  it("only names a hypothesis for the role that excludes one", () => {
    const html = renderToStaticMarkup(
      <AgentRoleChip role="demoted" excludes="something else" />,
    );
    expect(html).toContain("Less relevant");
    expect(html).not.toContain("something else");
  });

  it("still shows an attributed role when the agent left no sentence", () => {
    const html = renderToStaticMarkup(
      <AgentClaimNote role="context" claim="" />,
    );
    expect(html).toContain("Context");
    expect(html).not.toContain("Agent&#x27;s note:");
    // Nothing at all to say and no role: render nothing.
    expect(renderToStaticMarkup(<AgentClaimNote claim="" />)).toBe("");
  });
});
