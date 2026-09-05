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
  investigationEvidenceShouldRevealHistory,
} from "./InvestigationEvidencePane";
import {
  investigationEvidenceSourceDomId,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";

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
  afterMaterialEvidence?: string,
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution,
  onOpenResource?: (ref: DiagnosisResourceRef) => void,
): string {
  return renderToStaticMarkup(
    <InvestigationEvidencePane
      projection={projection}
      rootCauseEvidence={rootCauseEvidence}
      collecting={collecting}
      animateGroupIds={new Set()}
      onViewSource={onViewSource}
      onViewActivity={() => {}}
      onOpenResource={onOpenResource}
      afterMaterialEvidence={afterMaterialEvidence}
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

  it("promotes cited evidence ahead of other Radar observations without exposing tool-result wrappers", () => {
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
            count: 1,
            lastTimestamp: "2026-09-02T10:00:00Z",
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
    const html = render(projection, false, undefined, resolution);
    const anchor = `id="${investigationEvidenceSourceDomId(
      resolution.links[0].source.id,
    )}"`;

    expect(html).toContain("Evidence from cited results");
    expect(html).toContain(
      "Radar observations summarized from investigation results the agent cited.",
    );
    expect(html).not.toContain("validated against this run");
    expect(html).not.toContain("Agent-selected check");
    expect(html).not.toContain("from this check below");
    expect(html).toContain("Additional Radar observations");
    expect(html.indexOf("Evidence from cited results")).toBeLessThan(
      html.indexOf("Additional Radar observations"),
    );
    expect(html.match(new RegExp(anchor, "g"))).toHaveLength(1);
    expect(html).toContain('aria-label="CrashLoopBackOff evidence"');
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

  it("keeps uncited revisions visible when cited evidence shares their semantic card", () => {
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
    const html = render(projection, false, undefined, resolution);
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
      html.indexOf("Additional Radar observations"),
    );
    expect(html.indexOf(uncitedMessage)).toBeGreaterThan(
      html.indexOf("Additional Radar observations"),
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

  it("keeps an unadapted cited result in Activity instead of presenting it as evidence", () => {
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
    const html = render(projection, false, undefined, resolution);

    expect(html).toContain(
      "The assessment cites a result that could not be summarized here:",
    );
    expect(html).toContain("Query Prometheus in Activity");
    expect(html).toContain(
      'aria-label="View cited Query Prometheus result in Activity"',
    );
    expect(html).not.toContain("Agent-selected check");
    expect(html).not.toContain("View source");
    expect(html).toContain("Radar evidence");
    expect(html).not.toContain("Evidence from cited results");
    expect(html).not.toContain("Additional Radar observations");
    expect(html).toContain("No evidence could be summarized here");
  });

  it("does not frame unrelated structured evidence as other when the cited source is Activity-only", () => {
    const ref = evidenceRef("a", "c");
    const projection = project(
      tool(
        "search-cited",
        "search",
        { results: [{ kind: "Pod", namespace: "shop", name: "api-123" }] },
        { evidenceRef: ref },
      ),
      tool("events-uncited", "get_events", {
        events: [
          {
            type: "Warning",
            reason: "BackOff",
            message: "Back-off restarting failed container",
            count: 1,
            lastTimestamp: "2026-09-02T10:00:00Z",
          },
        ],
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);

    expect(resolution.links[0].group).toBeUndefined();
    expect(html).toContain("Radar evidence");
    expect(html).toContain("Kubernetes events");
    expect(html).not.toContain("Evidence from cited results");
    expect(html).not.toContain("Additional Radar observations");
    expect(html).not.toContain("No evidence could be summarized here");
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
    const html = render(projection, false, undefined, resolution);
    const anchor = `id="${investigationEvidenceSourceDomId(source.id)}"`;

    expect(resolution.links[0].group).toBeUndefined();
    expect(html).toContain("Get Resource result");
    expect(html).not.toContain("Agent-selected check");
    expect(html).toContain(
      'aria-label="View cited Get Resource result in Activity"',
    );
    expect(html).toContain("Evidence coverage is incomplete");
    expect(html).toContain("couldn&#x27;t summarize this investigation step");
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
    const missing = render(projection, false, undefined, {
      status: "missing",
      links: [],
    });
    const invalid = render(projection, false, undefined, {
      status: "invalid",
      links: [],
    });

    expect(missing).toContain(
      "Assessment does not cite specific Radar evidence",
    );
    expect(invalid).toContain("Assessment references could not be matched");
    expect(invalid).toContain(
      "could not match the assessment’s references to this investigation",
    );
  });

  it("offers a quiet Radar link only for a host-wired, unambiguous subject", () => {
    const projection = project(
      tool("issues-nav", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );

    expect(render(projection)).not.toContain(
      "Open current Deployment shop/api in Radar",
    );
    expect(render(projection, false, undefined, undefined, () => {})).toContain(
      "Open current Deployment shop/api in Radar",
    );
  });

  it("links exact resources named inside change and DNS evidence", () => {
    const projection = project(
      tool("diagnose-related-resources", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [
          {
            apiVersion: "v1",
            kind: "ConfigMap",
            namespace: "shop",
            name: "api-settings",
            changeType: "update",
            timestamp: "2026-09-02T09:55:00Z",
          },
          {
            kind: "ExternalRecord",
            namespace: "shop",
            name: "api.example.test",
            changeType: "update",
            timestamp: "2026-09-02T09:56:00Z",
          },
        ],
        dnsContext: {
          coreDNSFindings: [
            {
              kind: "ConfigMap",
              namespace: "kube-system",
              name: "coredns",
              severity: "warning",
              reason: "Suspicious forwarding rule",
            },
          ],
        },
      }),
    );
    const html = render(projection, false, undefined, undefined, () => {});

    expect(html).toMatch(/<button[^>]*>shop\/api-settings<\/button>/);
    expect(html).toMatch(/<button[^>]*>kube-system\/coredns<\/button>/);
    expect(html).not.toMatch(/<button[^>]*>shop\/api\.example\.test<\/button>/);
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

    expect(html).toContain("Critical signals");
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

    expect(html).not.toContain("Critical signals");
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
    expect(
      investigationEvidenceShouldRevealHistory(
        group,
        group.chronologicalLatest.source.id,
      ),
    ).toBe(true);
    expect(
      investigationEvidenceShouldRevealHistory(group, group.latest.source.id),
    ).toBe(false);
    expect(html).toContain("Observation history");
    expect(html).toContain("related to the investigated resource");
    expect(html).toContain("Broader context");
    expect(html).not.toContain("later broader observation retained");
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
    expect(keyHtml).toContain("More critical signals");
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
    expect(supportingHtml).toContain("More related evidence");
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
    expect(html).toContain("related to the investigated resource");

    const ordered = render(projection, false, "NEXT_STEP_MARKER");
    expect(ordered.indexOf("Critical signals")).toBeLessThan(
      ordered.indexOf("Evidence coverage is incomplete"),
    );
    expect(ordered.indexOf("Evidence coverage is incomplete")).toBeLessThan(
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

    const html = render(projection);
    expect(html.split(`id="${sourceDomId}"`)).toHaveLength(2);
    expect(html).not.toContain("confirmed by");
    expect(html).not.toContain("2 observations");
    expect(html).not.toContain("Observation history");
  });

  it("does not expose repeated tool-result counts as corroboration", () => {
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

    expect(render(projection)).not.toContain("seen in 2 results");
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
    expect(html).toContain("Observation history");
    expect(html).not.toContain("2 observations");
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
    expect(html).toContain("What Radar did not find");
    expect(html).not.toContain(`${receipt!.id}-body`);
  });

  it("does not offer an expander when the body would only repeat the summary", () => {
    const issueProjection = project(
      tool("issues", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const startupProjection = project(
      tool("startup", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: { tier: "basic" },
        startupBlockers: [
          {
            kind: "Pod",
            name: "api-123",
            reason: "ImagePullBackOff",
            severity: "critical",
            message: "The image could not be pulled.",
          },
        ],
        events: [],
        recentChanges: [],
      }),
    );
    const issue = issueProjection.groups.find(
      (group) => group.kind === "issue",
    )!;
    const startup = startupProjection.groups.find(
      (group) => group.kind === "startup",
    )!;

    expect(render(issueProjection)).not.toContain(issue.id + "-body");
    expect(render(startupProjection)).not.toContain(startup.id + "-body");
  });

  it("renders SealedSecret conditions only once in a resource card", () => {
    const projection = project(
      tool("sealed-secret", "get_resource", {
        resource: {
          apiVersion: "bitnami.com/v1alpha1",
          kind: "SealedSecret",
          metadata: { namespace: "shop", name: "db-password" },
          spec: { encryptedData: { password: "ciphertext" } },
          status: {
            conditions: [
              {
                type: "Synced",
                status: "False",
                reason: "ControllerError",
                message: "The key could not be decrypted.",
              },
            ],
          },
        },
        resourceContext: {
          tier: "basic",
          statusSummary: {
            conditions: [
              {
                type: "Synced",
                status: "False",
                reason: "ControllerError",
                message: "The key could not be decrypted.",
              },
            ],
          },
        },
      }),
    );
    const html = render(projection);

    expect(html.match(/ControllerError/g)).toHaveLength(1);
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
    expect(html).toContain("Evidence coverage is incomplete");
  });

  it("summarizes incomplete coverage and points to Activity for review", () => {
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

    expect(html).toContain("Evidence coverage is incomplete");
    expect(html).toContain("1 investigation result needs review");
    expect(html).toContain(
      "Only part of this investigation result was saved, so Radar could not summarize it here.",
    );
    expect(html).toContain('aria-label="View Activity source for Issue scan"');
    expect(source.primaryGroupId).toBeUndefined();
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(source.id)}"`,
    );
    expect(html).toContain("data-evidence-source-container");
    expect(html).toContain('aria-label="Evidence limitation for Issue scan:');
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
    expect(collecting).toContain("Evidence will appear here");
    expect(collecting).toContain(
      "The Activity pane remains the live record while the agent investigates.",
    );
    expect(collecting).not.toContain("No evidence could be summarized here");

    expect(finished).not.toContain(">collecting<");
    expect(finished).toContain("No evidence could be summarized here");
    expect(finished).toContain("This does not mean the resource is healthy.");
    expect(finished).not.toContain("Evidence will appear here");
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
