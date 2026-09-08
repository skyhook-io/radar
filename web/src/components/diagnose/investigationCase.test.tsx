import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  InvestigationEvidencePane,
  investigationCaseByGroup,
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
} from "./investigationCase";
import { AssessmentSources, assessmentSourceRows } from "./parts";
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
    expect(history).toContain("Used for assessment");
    expect(html).toContain("(earlier observation)");
  });

  it("keeps an identical earlier read on the card because the fact is unchanged", () => {
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
    expect(resolved.items[0].placement).toBe("card");
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
    expect(sources).toContain(">Context<");
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
