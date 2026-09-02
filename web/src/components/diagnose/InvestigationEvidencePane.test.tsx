import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  InvestigationEvidencePane,
  VISIBLE_ADDITIONAL_KEY_EVIDENCE,
  VISIBLE_SUPPORTING_EVIDENCE,
  investigationDisclosureSettleDelay,
  investigationEvidenceRevealCollection,
} from "./InvestigationEvidencePane";
import {
  investigationEvidenceSourceDomId,
  projectInvestigationEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";

const onViewSource = vi.fn();
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
  collecting = false,
  afterMaterialEvidence?: string,
): string {
  return renderToStaticMarkup(
    <InvestigationEvidencePane
      projection={projection}
      collecting={collecting}
      animateGroupIds={new Set()}
      onViewSource={onViewSource}
      afterMaterialEvidence={afterMaterialEvidence}
    />,
  );
}

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

describe("InvestigationEvidencePane hierarchy and provenance", () => {
  it("waits for disclosure motion only when reduced motion is not requested", () => {
    expect(investigationDisclosureSettleDelay(false)).toBe(
      INVESTIGATION_DISCLOSURE_SETTLE_MS,
    );
    expect(INVESTIGATION_DISCLOSURE_SETTLE_MS).toBeGreaterThan(200);
    expect(investigationDisclosureSettleDelay(true)).toBe(0);

    const html = render(
      project(
        tool("resource-motion", "get_resource", {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        }),
      ),
    );
    expect(
      html.match(/motion-reduce:transition-none/g)?.length ?? 0,
    ).toBeGreaterThanOrEqual(2);
  });

  it("renders the first producer-classified failure without claiming it was ranked", () => {
    const projection = project(
      tool("issues-1", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const html = render(projection);
    const source = projection.sources[0];

    expect(html).toContain("Observed failure");
    expect(html).toContain(
      "The first failure signal Radar captured during this run.",
    );
    expect(html).not.toContain("strongest");
    expect(html).not.toContain("main proof");
    expect(html).toContain("CrashLoopBackOff");
    expect(html).toContain("View source");
    expect(html).toContain("issues");
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(source.id)}"`,
    );
    expect(source.primaryGroupId).toBe(
      projection.groups.find((group) => group.kind === "issue")?.id,
    );
  });

  it("labels an unmatched broad issue as context instead of an observed failure", () => {
    const projection = project(
      tool(
        "issues-broad",
        "issues",
        {
          issues: [
            {
              ...criticalIssue,
              id: "issue-db-crash",
              name: "db",
              reason: "DatabaseCrashLoop",
            },
          ],
          total: 1,
          total_matched: 1,
        },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    );
    const html = render(projection);

    expect(html).not.toContain("Observed failure");
    expect(html).toContain("Context");
    expect(html).toContain("Broader context");
  });

  it("labels each revision's proof scope without replacing the lead source", () => {
    const podIssue = {
      ...criticalIssue,
      id: "issue-api-pod-crash",
      kind: "Pod",
      group: "",
      name: "api-abc",
    };
    const projection = project(
      tool("diagnose-target", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        relatedIssues: [podIssue],
      }),
      tool("issues-broad", "issues", {
        issues: [podIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const group = projection.groups.find((item) => item.kind === "issue")!;
    const html = render(projection);

    expect(group.latest.source.stepId).toBe("diagnose-target");
    expect(group.chronologicalLatest.source.stepId).toBe("issues-broad");
    expect(html).toContain("Observation history");
    expect(html).toContain("Related");
    expect(html).toContain("Broader");
    expect(html).toContain("Later broader check");
  });

  it("caps key and supporting evidence while preserving source navigation", () => {
    const keyTools = Array.from(
      { length: VISIBLE_ADDITIONAL_KEY_EVIDENCE + 2 },
      (_, index) =>
        tool(`key-${index}`, "issues", {
          issues: [
            {
              ...criticalIssue,
              id: `critical-${index}`,
              reason: `CriticalReason${index}`,
            },
          ],
          total: 1,
          total_matched: 1,
        }),
    );
    const keyProjection = project(...keyTools);
    const hiddenKeySource = keyProjection.sources.at(-1)!;
    const keyHtml = render(keyProjection);

    expect(
      investigationEvidenceRevealCollection(keyProjection, hiddenKeySource.id),
    ).toBe("more-key");
    expect(keyHtml).toContain("More key evidence");
    expect(keyHtml).toContain(
      'aria-controls="investigation-more-key-evidence"',
    );
    expect(keyHtml).toContain('style="grid-template-rows:0fr"');

    const supportingTools = Array.from(
      { length: VISIBLE_SUPPORTING_EVIDENCE + 1 },
      (_, index) =>
        tool(`supporting-${index}`, "issues", {
          issues: [
            {
              ...criticalIssue,
              id: `warning-${index}`,
              severity: "warning",
              reason: `WarningReason${index}`,
            },
          ],
          total: 1,
          total_matched: 1,
        }),
    );
    const supportingProjection = project(...supportingTools);
    const hiddenSupportingSource = supportingProjection.sources.at(-1)!;
    const supportingHtml = render(supportingProjection);

    expect(
      investigationEvidenceRevealCollection(
        supportingProjection,
        hiddenSupportingSource.id,
      ),
    ).toBe("more-supporting");
    expect(supportingHtml).toContain("More supporting evidence");
  });

  it("routes source navigation through collapsed collections", () => {
    const projection = project(
      tool("issues-old", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    projection.groups[0].historical = true;
    const sourceId = projection.sources[0].id;
    expect(investigationEvidenceRevealCollection(projection, sourceId)).toBe(
      "earlier",
    );
  });

  it("routes a fan-out source to its primary Key card before secondary Context", () => {
    const projection = project(
      tool("diagnose-fan-out", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        pods: 1,
        relatedIssues: [criticalIssue],
        events: [],
        recentChanges: [],
      }),
    );
    const source = projection.sources[0];
    const primary = projection.groups.find(
      (group) => group.id === source.primaryGroupId,
    );

    expect(primary?.latest.tier).toBe("key");
    expect(
      projection.groups.some(
        (group) =>
          group.latest.tier === "context" &&
          group.observations.some(
            (observation) => observation.source.id === source.id,
          ),
      ),
    ).toBe(true);
    expect(
      investigationEvidenceRevealCollection(projection, source.id),
    ).toBeUndefined();
  });

  it("routes limitation-only sources through coverage", () => {
    const projection = project(
      tool(
        "issues-cut",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        { truncated: true },
      ),
    );
    const sourceId = projection.sources[0].id;
    const html = render(projection);

    expect(investigationEvidenceRevealCollection(projection, sourceId)).toBe(
      "coverage",
    );
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(sourceId)}"`,
    );
  });

  it("emits exactly one Evidence anchor when one bundled source fans out into cards", () => {
    const projection = project(
      tool("diagnose-1", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        pods: 1,
        relatedIssues: [criticalIssue],
        startupBlockers: [
          {
            kind: "Pod",
            name: "api-abc",
            reason: "ImagePullBackOff",
            severity: "critical",
            message: "The image tag does not exist.",
          },
        ],
        logsCurrent: [
          {
            pod: "api-abc",
            container: "api",
            logs: {
              lines: ["ERROR missing DATABASE_URL"],
              totalLines: 1,
              matchedLines: 1,
              fallback: false,
            },
          },
        ],
        events: [
          {
            reason: "BackOff",
            message: "Back-off restarting failed container",
            type: "Warning",
            count: 3,
            lastTimestamp: "2026-09-02T10:00:00Z",
          },
        ],
        eventsError: "The warning-event result was incomplete.",
        recentChanges: [],
      }),
    );
    expect(projection.groups.length).toBeGreaterThan(3);

    const html = render(projection);
    const anchor = `id="${investigationEvidenceSourceDomId(projection.sources[0].id)}"`;
    expect(html.split(anchor)).toHaveLength(2);
    expect(
      projection.groups.filter((group) => group.latest.tier === "key").length,
    ).toBeGreaterThan(1);
    expect(
      html.split('id="investigation-observed-failure-heading"'),
    ).toHaveLength(2);
    expect(
      html.split('id="investigation-other-key-evidence-heading"'),
    ).toHaveLength(2);
    expect(html).not.toContain('id="investigation-evidence-tier-key"');
    expect(html).toContain(
      'aria-labelledby="investigation-observed-failure-heading"',
    );
    expect(html).toContain(
      'aria-labelledby="investigation-other-key-evidence-heading"',
    );
    expect(html).toContain("Related resource");

    const ordered = render(projection, false, "NEXT_STEP_MARKER");
    expect(ordered.indexOf("Observed failure")).toBeLessThan(
      ordered.indexOf("Other key evidence"),
    );
    expect(ordered.indexOf("Other key evidence")).toBeLessThan(
      ordered.indexOf("Evidence coverage has"),
    );
    expect(ordered.indexOf("Evidence coverage has")).toBeLessThan(
      ordered.indexOf("NEXT_STEP_MARKER"),
    );
  });

  it("deduplicates source anchors when one call records repeated observations in a card", () => {
    const projection = project(
      tool("issues-duplicate", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const group = projection.groups[0];
    group.observations.push({
      ...group.observations[0],
      revision: group.observations.length + 1,
    });
    const sourceDomId = investigationEvidenceSourceDomId(
      group.observations[0].source.id,
    );

    expect(render(projection).split(`id="${sourceDomId}"`)).toHaveLength(2);
  });

  it("deduplicates source anchors in compact Checked receipts", () => {
    const projection = project(
      tool("diagnose-checked", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [],
      }),
    );
    const group = projection.groups.find((item) => item.kind === "receipt")!;
    const source = group.observations[0].source;
    source.primaryGroupId = group.id;
    group.observations.push({
      ...group.observations[0],
      revision: group.observations.length + 1,
    });
    projection.groups = [group];
    const sourceDomId = investigationEvidenceSourceDomId(source.id);

    expect(render(projection).split(`id="${sourceDomId}"`)).toHaveLength(2);
  });
});

describe("InvestigationEvidencePane honest result states", () => {
  it("renders evidence without details as static content, not a disabled control", () => {
    const projection = project(
      tool("resource-static", "get_resource", {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { namespace: "shop", name: "api" },
      }),
    );
    const resource = projection.groups.find(
      (group) => group.kind === "resource",
    );
    expect(resource).toBeDefined();

    const html = render(projection);
    expect(html).toContain("Deployment shop/api");
    expect(html).not.toContain("disabled");
    expect(html).not.toContain(`${resource!.id}-body`);
  });

  it("renders a confirmed empty check as a compact, non-expandable receipt", () => {
    const projection = project(
      tool("diagnose-empty", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [],
      }),
    );
    const receipt = projection.groups.find((group) => group.kind === "receipt");
    expect(receipt).toBeDefined();

    const html = render(projection);
    expect(html).toContain('id="investigation-checked-heading"');
    expect(html).toContain("No matching warning events");
    expect(html).toContain(
      "Confirmed successful checks with a meaningful empty result.",
    );
    expect(html).not.toContain(`${receipt!.id}-body`);
  });

  it("does not render Checked when the same empty result lacks explicit success", () => {
    const projection = project(
      tool("events-old", "get_events", { events: [] }, { isError: undefined }),
    );
    const html = render(projection);

    expect(projection.coverage.checked).toBe(0);
    expect(projection.groups).toHaveLength(0);
    expect(html).not.toContain('id="investigation-checked-heading"');
    expect(html).not.toContain("No matching warning events");
    expect(html).toContain("Evidence coverage has 1 limit");
  });

  it("summarizes incomplete coverage without hiding that raw results remain", () => {
    const projection = project(
      tool(
        "issues-cut",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        { truncated: true },
      ),
    );
    const html = render(projection);
    const source = projection.sources[0];

    expect(html).toContain("Evidence coverage has 1 limit");
    expect(html).toContain(
      "1 check incomplete · raw results remain in Activity",
    );
    expect(html).toContain(
      "The transcript retained only part of this tool result, so Radar did not parse it into evidence.",
    );
    expect(html).toContain("View source");
    expect(html).toContain("issues");
    expect(source.primaryGroupId).toBeUndefined();
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(source.id)}"`,
    );
    expect(html).toContain("data-evidence-source-container");
    expect(html).toContain("focus:ring-2");
  });

  it("distinguishes a live empty collection from a finished inconclusive one", () => {
    const empty: InvestigationEvidenceProjection = {
      groups: [],
      limitations: [],
      sources: [],
      coverage: { attempted: 0, projected: 0, limited: 0, checked: 0 },
    };
    const collecting = render(empty, true);
    const finished = render(empty, false);

    expect(collecting).toContain("collecting");
    expect(collecting).toContain("Completed checks will appear here");
    expect(collecting).toContain(
      "The Activity pane remains the live record while the agent investigates.",
    );
    expect(collecting).not.toContain("No structured evidence was collected");

    expect(finished).not.toContain(">collecting<");
    expect(finished).toContain("No structured evidence was collected");
    expect(finished).toContain(
      "This does not establish that the resource is healthy.",
    );
    expect(finished).not.toContain("Completed checks will appear here");
  });

  it("keeps pre-verification smoking guns inspectable as Earlier evidence", () => {
    const resource = {
      apiVersion: "apps/v1",
      kind: "Deployment",
      metadata: { namespace: "shop", name: "api" },
    };
    const projection = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool("initial", "diagnose", {
              resource,
              resourceContext: { tier: "basic" },
              pods: 1,
              relatedIssues: [criticalIssue],
              events: [],
              recentChanges: [],
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("verification", "diagnose", {
              resource,
              resourceContext: { tier: "basic" },
              pods: 1,
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      target,
    );

    const html = render(projection);
    expect(
      projection.groups.find((group) => group.kind === "issue")?.historical,
    ).toBe(true);
    expect(html).toContain("Earlier evidence");
    expect(html).toContain(
      "Kept for comparison; this does not mean it was resolved.",
    );
    expect(html).toContain("CrashLoopBackOff");
  });
});
