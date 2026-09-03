import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  InvestigationEvidencePane,
  VISIBLE_ADDITIONAL_KEY_EVIDENCE,
  VISIBLE_LOG_EVIDENCE_LINES,
  VISIBLE_SUPPORTING_EVIDENCE,
  investigationDisclosureSettleDelay,
  investigationDisclosureScrollTop,
  investigationEvidenceFullRowFlags,
  investigationEvidenceRevealCollection,
} from "./InvestigationEvidencePane";
import {
  investigationEvidenceSourceDomId,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
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
  collecting = false,
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution,
  onOpenResource?: (ref: unknown) => void,
): string {
  return renderToStaticMarkup(
    <InvestigationEvidencePane
      projection={projection}
      rootCauseEvidence={rootCauseEvidence}
      collecting={collecting}
      animateGroupIds={new Set()}
      onViewSource={onViewSource}
      onOpenResource={onOpenResource}
    />,
  );
}

function evidenceRef(scope: string, nonce: string): string {
  return `ev_${scope.repeat(26)}_${nonce.repeat(26)}`;
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
  it("fills supporting-evidence rows instead of leaving orphan half-width cards", () => {
    expect(
      investigationEvidenceFullRowFlags(["resource", "events", "changes"]),
    ).toEqual([true, true, true]);
    expect(
      investigationEvidenceFullRowFlags([
        "resource",
        "changes",
        "logs",
        "resource",
        "inventory",
      ]),
    ).toEqual([false, false, true, false, false]);
    expect(
      investigationEvidenceFullRowFlags(["resource", "changes", "resource"]),
    ).toEqual([false, false, true]);
  });

  it("promotes exact agent-selected checks ahead of other Radar observations without duplicate anchors", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "diagnose-cited",
        "diagnose",
        {
          resource: {
            apiVersion: "apps/v1",
            kind: "Deployment",
            metadata: { namespace: "shop", name: "api" },
          },
          resourceContext: { tier: "basic" },
          relatedIssues: [criticalIssue],
        },
        { evidenceRef: ref },
      ),
      tool("events-other", "get_events", {
        events: [
          {
            type: "Warning",
            reason: "BackOff",
            message: "Back-off restarting failed container",
          },
        ],
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const originalGroupId = resolution.links[0].originalGroupId!;
    const citedSnapshotId = resolution.links[0].group!.id;
    const before = render(projection);
    const html = render(projection, false, resolution);
    const anchor = `id="${investigationEvidenceSourceDomId(
      resolution.links[0].source.id,
    )}"`;

    expect(html).toContain("Evidence cited by assessment");
    expect(html).toContain("Agent-selected check 1");
    expect(html).toContain("Other Radar observations");
    expect(html.indexOf("Evidence cited by assessment")).toBeLessThan(
      html.indexOf("Other Radar observations"),
    );
    expect(html.match(new RegExp(anchor, "g"))).toHaveLength(1);
    // The terminal assessment moves an existing card without adding an
    // evidence revision. Keep its old DOM id so the workspace's layout anchor
    // can compensate the relocation instead of jumping the reader's scroll.
    expect(before).toContain(`id="${originalGroupId}"`);
    expect(html.match(new RegExp(`id="${originalGroupId}"`, "g"))).toHaveLength(
      1,
    );
    expect(html).not.toContain(`id="${citedSnapshotId}"`);
    expect(html).not.toContain("animate-transcript-enter");
  });

  it("keeps uncited revisions visible when a cited check shares their semantic card", () => {
    const ref = evidenceRef("a", "b");
    const citedMessage = "The first check saw one crashing replica.";
    const uncitedMessage = "A later uncited check saw every replica crashing.";
    const projection = project(
      tool(
        "issues-cited",
        "issues",
        {
          issues: [{ ...criticalIssue, message: citedMessage }],
          total: 1,
          total_matched: 1,
        },
        { evidenceRef: ref },
      ),
      tool("issues-uncited", "issues", {
        issues: [{ ...criticalIssue, message: uncitedMessage }],
        total: 1,
        total_matched: 1,
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, resolution);
    const citedSource = projection.sources.find(
      (source) => source.stepId === "issues-cited",
    )!;
    const uncitedSource = projection.sources.find(
      (source) => source.stepId === "issues-uncited",
    )!;

    expect(projection.groups).toHaveLength(1);
    expect(html).toContain(citedMessage);
    expect(html).toContain(uncitedMessage);
    expect(html.indexOf(citedMessage)).toBeLessThan(
      html.indexOf("Other Radar observations"),
    );
    expect(html.indexOf(uncitedMessage)).toBeGreaterThan(
      html.indexOf("Other Radar observations"),
    );
    expect(html).toContain(`id="${resolution.links[0].group!.id}"`);
    expect(
      html.match(new RegExp(`id="${projection.groups[0].id}"`, "g")),
    ).toHaveLength(1);
    for (const source of [citedSource, uncitedSource]) {
      expect(
        html.match(
          new RegExp(
            `id="${investigationEvidenceSourceDomId(source.id)}"`,
            "g",
          ),
        ),
      ).toHaveLength(1);
    }
  });

  it("links an agent-selected unadapted successful check directly to Activity", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "metrics-cited",
        "query_prometheus",
        { result: [1] },
        { evidenceRef: ref },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, resolution);

    expect(html).toContain("Query Prometheus");
    expect(html).toContain(
      'aria-label="View cited query_prometheus result in Activity"',
    );
    expect(html).not.toContain("View source");
    expect(html).not.toContain("No structured evidence was collected");
  });

  it("gives a promoted fallback check one source anchor while retaining its coverage limit", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "resource-invalid-cited",
        "get_resource",
        { unexpected: true },
        { evidenceRef: ref },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const source = resolution.links[0].source;
    const html = render(projection, false, resolution);
    const anchor = `id="${investigationEvidenceSourceDomId(source.id)}"`;

    expect(resolution.links[0].group).toBeUndefined();
    expect(html).toContain("Get Resource");
    expect(html).toContain(
      'aria-label="View cited get_resource result in Activity"',
    );
    expect(html).toContain("Evidence coverage limited");
    expect(html).toContain(
      "result into an evidence card",
    );
    expect(html.match(new RegExp(anchor, "g"))).toHaveLength(1);
  });

  it("keeps inline log evidence compact and strips terminal color codes", () => {
    const lines = Array.from(
      { length: VISIBLE_LOG_EVIDENCE_LINES + 3 },
      (_, index) =>
        `\u001b[31mentry-${String(index + 1).padStart(2, "0")}\u001b[0m`,
    );
    const projection = project(
      tool(
        "logs-compact",
        "get_pod_logs",
        {
          lines,
          totalLines: lines.length,
          matchedLines: lines.length,
          fallback: false,
        },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-pod",
            container: "api",
          }),
        },
      ),
    );
    const html = render(projection);

    expect(html).toContain(
      `Selected log excerpt · last ${VISIBLE_LOG_EVIDENCE_LINES} of ${lines.length} lines`,
    );
    expect(html).not.toContain("entry-01");
    expect(html).not.toContain("\u001b[31m");
    expect(html).toContain(`entry-${lines.length}`);
  });

  it("shows compact honest boundaries for missing or invalid assessment links", () => {
    const projection = project(
      tool("resource-1", "get_resource", {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { namespace: "shop", name: "api" },
      }),
    );
    const missing = render(projection, false, {
      status: "missing",
      links: [],
    });
    const invalid = render(projection, false, {
      status: "invalid",
      links: [],
    });

    expect(missing).toContain("Assessment is not linked to specific checks");
    expect(invalid).toContain("Cited checks could not be validated");
    expect(invalid).toContain("did not promote those references as evidence");
  });

  it("demotes uncited key evidence to a closed disclosure once the assessment is linked", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "issues-cited",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        { evidenceRef: ref },
      ),
      tool("issues-other", "issues", {
        issues: [
          {
            ...criticalIssue,
            id: "issue-api-oom",
            category: "oom",
            reason: "OOMKilled",
            message: "The API container ran out of memory.",
          },
        ],
        total: 1,
        total_matched: 1,
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );

    const before = render(projection);
    expect(before).toContain('id="investigation-key-evidence-heading"');

    const linked = render(projection, false, resolution);
    expect(linked).not.toContain('id="investigation-key-evidence-heading"');
    expect(linked).toContain('aria-controls="investigation-more-key-evidence"');
    expect(linked).toContain(
      "Failure signals Radar captured that the assessment did not cite",
    );
    // The reveal contract must open that disclosure for any demoted key group.
    const uncited = projection.sources.find(
      (source) => source.stepId === "issues-other",
    )!;
    expect(
      investigationEvidenceRevealCollection(projection, uncited.id, true),
    ).toBe("more-key");
    expect(
      investigationEvidenceRevealCollection(projection, uncited.id),
    ).toBeUndefined();
  });

  it("offers open-in-Radar navigation only when the host wires it and the subject is unambiguous", () => {
    const projection = project(
      tool("issues-nav", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const withoutHandler = render(projection);
    expect(withoutHandler).not.toContain("Open Deployment shop/api in Radar");

    const withHandler = render(projection, false, undefined, () => {});
    expect(withHandler).toContain(
      "Open Deployment shop/api in Radar",
    );
  });

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

  it("reveals a newly expanded disclosure without skipping the start of tall content", () => {
    expect(
      investigationDisclosureScrollTop({
        scrollTop: 500,
        viewportTop: 100,
        viewportBottom: 1000,
        disclosureTop: 780,
        disclosureBottom: 1180,
      }),
    ).toBe(688);
    expect(
      investigationDisclosureScrollTop({
        scrollTop: 500,
        viewportTop: 100,
        viewportBottom: 1000,
        disclosureTop: 780,
        disclosureBottom: 1900,
      }),
    ).toBe(1172);
    expect(
      investigationDisclosureScrollTop({
        scrollTop: 500,
        viewportTop: 100,
        viewportBottom: 1000,
        disclosureTop: 240,
        disclosureBottom: 900,
      }),
    ).toBeUndefined();
    expect(
      investigationDisclosureScrollTop({
        scrollTop: 500,
        viewportTop: 100,
        viewportBottom: 1000,
        disclosureTop: 90,
        disclosureBottom: 500,
      }),
    ).toBe(482);
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

    expect(html).toContain("Key evidence");
    expect(html).not.toContain("strongest");
    expect(html).not.toContain("main proof");
    expect(html).toContain("CrashLoopBackOff");
    expect(html).toContain(
      'aria-label="View Activity source for CrashLoopBackOff"',
    );
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

    expect(html).not.toContain("Key evidence");
    expect(html).toContain("Context");
    expect(html).toContain("Broader-scope signals and direct relationships");
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
    expect(html).toContain("Other observations");
    expect(html).toContain("related resource");
    expect(html).toContain("Broader");
    expect(html).toContain("later broader check retained");
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
      { length: VISIBLE_SUPPORTING_EVIDENCE + 3 },
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
    const finalOverflowGroup = supportingProjection.groups.find(
      (group) => group.id === hiddenSupportingSource.primaryGroupId,
    )!;
    const supportingHtml = render(supportingProjection);

    expect(
      investigationEvidenceRevealCollection(
        supportingProjection,
        hiddenSupportingSource.id,
      ),
    ).toBe("more-supporting");
    expect(supportingHtml).toContain("More supporting evidence");
    expect(supportingHtml).not.toContain('aria-expanded="true"');
    expect(
      supportingHtml.match(
        new RegExp(`<article[^>]*id="${finalOverflowGroup.id}"[^>]*>`),
      )?.[0],
    ).toContain("@min-[760px]/evidence:col-span-2");
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
    expect(html.split('id="investigation-key-evidence-heading"')).toHaveLength(
      2,
    );
    expect(html).not.toContain('id="investigation-evidence-tier-key"');
    expect(html).toContain(
      'aria-labelledby="investigation-key-evidence-heading"',
    );
    expect(html).toContain("related resource");

    expect(html.indexOf("Key evidence")).toBeLessThan(
      html.indexOf("Evidence coverage limited"),
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

    const html = render(projection);
    expect(html.split(`id="${sourceDomId}"`)).toHaveLength(2);
    expect(html).not.toContain("confirmed by");
    expect(html).not.toContain("2 observations");
    expect(html).not.toContain("Other observations");
  });

  it("counts distinct tool calls when repeated evidence confirms a card", () => {
    const projection = project(
      tool("issues-first", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
      tool("issues-second", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );

    expect(render(projection)).toContain("confirmed by 2 checks");
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

  it("keeps empty specialized resources static unless another real detail exists", () => {
    const projection = project(
      tool("config-empty", "get_resource", {
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { namespace: "shop", name: "empty-config" },
        data: {},
      }),
      tool("secret-empty", "get_resource", {
        kind: "Secret",
        name: "empty-secret",
        namespace: "shop",
        type: "Opaque",
        keys: [],
      }),
      tool("config-empty-context", "get_resource", {
        resource: {
          apiVersion: "v1",
          kind: "ConfigMap",
          metadata: { namespace: "shop", name: "empty-context" },
          data: {},
        },
        resourceContext: {
          tier: "basic",
          workloadSummary: { replicas: {} },
        },
      }),
    );
    const resources = projection.groups.filter(
      (group) => group.kind === "resource",
    );
    const html = render(projection);

    expect(html).toContain("No data keys in this result");
    expect(html).toContain("0 keys · Opaque · values hidden");
    expect(html).not.toContain("0/0 replicas ready");
    for (const resource of resources) {
      expect(html).not.toContain(`${resource.id}-body`);
    }

    const withWarning = project(
      tool("config-warning", "get_resource", {
        resource: {
          apiVersion: "v1",
          kind: "ConfigMap",
          metadata: { namespace: "shop", name: "empty-config" },
          data: {},
        },
        warnings: ["The captured ConfigMap result is incomplete."],
      }),
    );
    const warningResource = withWarning.groups.find(
      (group) => group.kind === "resource",
    )!;
    const warningHtml = render(withWarning);
    expect(warningHtml).toContain(`${warningResource.id}-body`);
    expect(warningHtml).toContain(
      "The captured ConfigMap result is incomplete.",
    );
  });

  it("keeps revision history expandable when the latest resource is empty", () => {
    const projection = project(
      tool("config-before", "get_resource", {
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { namespace: "shop", name: "changing-config" },
        data: { API_ENDPOINT: "https://api.example.test" },
      }),
      tool("config-after", "get_resource", {
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { namespace: "shop", name: "changing-config" },
        data: {},
      }),
    );
    const resource = projection.groups.find(
      (group) => group.kind === "resource",
    )!;
    const html = render(projection);

    expect(resource.observations).toHaveLength(2);
    expect(html).toContain(`${resource.id}-body`);
    expect(html).toContain("Other observations");
    expect(html).toContain("2 observations");
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
    expect(html).toContain("Checks with no finding");
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
    expect(html).toContain("Evidence coverage limited");
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

    expect(html).toContain("Evidence coverage limited");
    expect(html).toContain(
      "1 check incomplete · raw results remain in Activity",
    );
    expect(html).toContain(
      "The transcript retained only part of this tool result, so Radar did not parse it into evidence.",
    );
    expect(html).toContain('aria-label="View Activity source for issues"');
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
      evidenceRefSources: [],
      citableSources: [],
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
      "Retained for comparison; earlier does not mean resolved.",
    );
    expect(html).toContain("CrashLoopBackOff");
  });
});
