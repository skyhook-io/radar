import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  InvestigationEvidencePane,
  VISIBLE_LOG_EVIDENCE_LINES,
  partitionInvestigationEvidence,
  investigationDisclosureSettleDelay,
  investigationDisclosureScrollTop,
  investigationEvidenceFullRowFlags,
  investigationEvidenceRevealCollection,
  investigationEvidenceShouldRevealHistory,
} from "./InvestigationEvidencePane";
import {
  investigationEvidenceSourceDomId,
  investigationEvidenceStepIdsByTurn,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import type { Diagnosis } from "../../api/diagnose";
import { AssessmentSources, ResultCard } from "./parts";
import { investigationEvidenceCoverageLimited } from "./investigationState";

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
  afterEvidence?: string,
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
      afterEvidence={afterEvidence}
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
  it("keeps the target issue from a cluster-wide response, not neighboring failures", () => {
    const projection = project(
      tool(
        "cluster-issues",
        "issues",
        {
          issues: [
            criticalIssue,
            {
              ...criticalIssue,
              id: "unrelated",
              name: "unrelated",
              reason: "UnrelatedFailure",
            },
          ],
          total: 2,
          total_matched: 2,
        },
        { summary: "{}" },
      ),
    );
    const partition = partitionInvestigationEvidence(projection.groups);
    expect(partition.main).toHaveLength(1);
    const html = render(projection);
    expect(html).toContain("CrashLoopBackOff");
    expect(html).not.toContain("UnrelatedFailure");
    expect(projection.groups).toHaveLength(2);
    expect(
      investigationEvidenceStepIdsByTurn(
        projection,
        new Set(partition.collectionByGroup.keys()),
      )
        .get(0)
        ?.has("cluster-issues"),
    ).toBe(true);
  });

  it("keeps coverage navigation when its primary broad card is omitted", () => {
    const projection = project(
      tool(
        "limited-broad",
        "issues",
        {
          issues: [{ ...criticalIssue, id: "other", name: "other" }],
          total: 1,
          total_matched: 2,
        },
        { summary: "{}" },
      ),
    );
    const partition = partitionInvestigationEvidence(projection.groups);
    const source = projection.sources[0];
    expect(source.primaryGroupId).toBeDefined();
    expect(partition.collectionByGroup.size).toBe(0);
    expect(projection.limitations.length).toBeGreaterThan(0);
    expect(
      investigationEvidenceRevealCollection(projection, source.id, partition),
    ).toBe("coverage");
    const html = render(projection);
    expect(
      html.split(`id="${investigationEvidenceSourceDomId(source.id)}"`),
    ).toHaveLength(2);
    expect(html).not.toContain("<article");
    expect(
      investigationEvidenceStepIdsByTurn(projection, new Set())
        .get(0)
        ?.has("limited-broad"),
    ).toBe(true);
  });

  it("does not promote namespace-wide change lists even when cited", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "broad-changes",
        "get_changes",
        {
          changes: [
            {
              kind: "Deployment",
              namespace: "shop",
              name: "other",
              summary: "Unrelated rollout",
              changeType: "update",
              timestamp: "2026-09-07T00:00:00Z",
            },
          ],
        },
        { summary: JSON.stringify({ namespace: "shop" }), evidenceRef: ref },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    expect(resolution.status).toBe("linked");
    expect(projection.groups.length).toBeGreaterThan(0);
    expect(render(projection, false, undefined, resolution)).not.toContain(
      "Unrelated rollout",
    );
    expect(
      partitionInvestigationEvidence(projection.groups, resolution)
        .collectionByGroup.size,
    ).toBe(0);
  });

  it("does not promote an arbitrary issue from a cited broad result", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "broad",
        "issues",
        {
          issues: ["db", "queue"].map((name) => ({
            ...criticalIssue,
            id: name,
            name,
            namespace: "other",
          })),
          total: 2,
          total_matched: 2,
        },
        { summary: JSON.stringify({ namespace: "other" }), evidenceRef: ref },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const partition = partitionInvestigationEvidence(
      projection.groups,
      resolution,
    );
    expect(partition.main).toHaveLength(0);
    expect(partition.collectionByGroup.size).toBe(0);
    expect(
      investigationEvidenceStepIdsByTurn(
        projection,
        new Set(partition.collectionByGroup.keys()),
      ).size,
    ).toBe(0);
    expect(
      investigationEvidenceRevealCollection(
        projection,
        projection.sources[0].id,
        partition,
      ),
    ).toBeUndefined();
    const html = render(projection, false, undefined, resolution);
    expect(html).not.toContain("<article");
    expect(html).not.toContain("other/");
    expect(html).toContain("No relevant evidence to show yet");
    expect(
      html.split(
        `id="${investigationEvidenceSourceDomId(projection.sources[0].id)}"`,
      ),
    ).toHaveLength(1);
    projection.groups.forEach((group) => {
      group.historical = true;
    });
    const history = partitionInvestigationEvidence(
      projection.groups,
      resolution,
    );
    expect(history.main).toHaveLength(0);
    expect(history.collectionByGroup.size).toBe(0);
    expect(history.earlier).toHaveLength(0);
  });

  it("keeps a selected focused fact but leaves unselected broader resources in Activity", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "config",
        "get_resource",
        {
          resource: {
            apiVersion: "v1",
            kind: "ConfigMap",
            metadata: { namespace: "shop", name: "api" },
            data: { HOST: "mongo" },
          },
          recentChanges: [],
          recentChangesSaturated: false,
          recentChangesCoverageLimited: false,
        },
        { evidenceRef: ref },
      ),
      tool("other", "get_resource", {
        kind: "Secret",
        namespace: "shop",
        name: "api",
        keys: ["PASSWORD"],
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const partition = partitionInvestigationEvidence(
      projection.groups,
      resolution,
    );
    expect(partition.main.map((group) => group.id)).toEqual([
      resolution.links[0].originalGroupId,
    ]);
    expect(partition.main[0].latest.tone).toBe("neutral");
    expect(partition.workload).toHaveLength(0);
    expect(partition.collectionByGroup.size).toBe(1);
    expect(projection.groups).toHaveLength(3);
    expect(render(projection, false, undefined, resolution)).not.toContain(
      'aria-expanded="true"',
    );
  });

  it("does not present collection timestamps as changed evidence", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "first",
        "issues",
        {
          issues: [{ ...criticalIssue, last_seen: "2026-09-07T08:00:00Z" }],
          total: 1,
          total_matched: 1,
        },
        { evidenceRef: ref },
      ),
      tool("again", "issues", {
        issues: [{ ...criticalIssue, last_seen: "2026-09-07T08:05:00Z" }],
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
    expect(html.match(/aria-label="CrashLoopBackOff evidence"/g)).toHaveLength(
      1,
    );
    expect(html).not.toContain("Previous observations");
    expect(projection.groups[0].observations).toHaveLength(2);
  });

  it("keeps different details behind a collapsed history even when summaries match", () => {
    const projection = project(
      tool("first", "issues", {
        issues: [
          {
            ...criticalIssue,
            cause: "The container failed",
            message: "Exit code 1",
          },
        ],
        total: 1,
        total_matched: 1,
      }),
      tool("again", "issues", {
        issues: [
          {
            ...criticalIssue,
            cause: "The container failed",
            message: "Exit code 2",
          },
        ],
        total: 1,
        total_matched: 1,
      }),
    );
    const html = render(projection);
    expect(html).toContain("Previous observations · 1");
    expect(html).toContain("Exit code 1");
    expect(html).toContain("Exit code 2");
    expect(html).not.toContain("Changed since the previous observation");
    expect(
      investigationEvidenceShouldRevealHistory(
        projection.groups[0],
        projection.sources[0].id,
      ),
    ).toBe(true);
  });

  it("folds unchanged uncited rechecks into the cited fact with every source accessible", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "cited",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        { evidenceRef: ref },
      ),
      tool("recheck", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const group = projection.groups.find(
      (item) => item.id === resolution.links[0].originalGroupId,
    )!;
    expect(resolution.links[0].source.stepId).toBe("cited");
    expect(group.observations.map((item) => item.source.stepId)).toEqual([
      "cited",
      "recheck",
    ]);
    const html = render(projection, false, undefined, resolution);
    expect(html.match(/aria-label="CrashLoopBackOff evidence"/g)).toHaveLength(
      1,
    );
    expect(html).not.toContain("Previous observations");
    expect(html).not.toContain("Changed since the previous observation");
    expect(html).not.toContain("Critical evidence");
    for (const source of projection.sources) {
      expect(
        html.split(`id="${investigationEvidenceSourceDomId(source.id)}"`),
      ).toHaveLength(2);
    }
    expect(
      investigationEvidenceShouldRevealHistory(group, projection.sources[1].id),
    ).toBe(false);
  });

  it("does not fold a changed intervening observation into an unchanged cited snapshot", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "cited",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        { evidenceRef: ref },
      ),
      tool("changed", "issues", {
        issues: [{ ...criticalIssue, message: "A different failure detail" }],
        total: 1,
        total_matched: 1,
      }),
      tool("again", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    expect(projection.groups[0].observations).toHaveLength(3);
    const html = render(projection, false, undefined, resolution);
    expect(html).not.toContain("Observed after the assessment’s source");
    expect(html).toContain("A different failure detail");
    for (const source of projection.sources) {
      expect(
        html.split(`id="${investigationEvidenceSourceDomId(source.id)}"`),
      ).toHaveLength(2);
    }
  });
  it("keeps detector guidance in the evidence data, not the Findings card or its disclosure", () => {
    const action = "Restore configuration or mark the reference optional.";
    const withGuidance = project(
      tool("issues-1", "issues", {
        issues: [{ ...criticalIssue, action }],
        total: 1,
        total_matched: 1,
      }),
    );
    const withoutGuidance = project(
      tool("issues-1", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
      }),
    );
    expect(render(withGuidance)).toEqual(render(withoutGuidance));
    expect(render(withGuidance)).not.toContain("Suggested check");
    const issue = withGuidance.groups.find((group) => group.kind === "issue")!;
    expect(issue.chronologicalLatest.data).toMatchObject({
      type: "issue",
      issue: { action },
    });
  });

  it("shows a small Secret key set directly, without redundant expansion or metadata", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "secret-keys",
        "get_resource",
        {
          kind: "Secret",
          namespace: "dev",
          name: "skyhook-agent",
          type: "Opaque",
          keys: ["QUALIFIRE_API_KEY"],
        },
        { evidenceRef: ref },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);
    expect(html).toContain("Keys: QUALIFIRE_API_KEY");
    expect(html.match(/QUALIFIRE_API_KEY/g)).toHaveLength(1);
    expect(html).not.toContain("Opaque");
    expect(html).not.toContain("values hidden");
    expect(html).not.toContain("Secret values are never shown");
    expect(html).not.toContain("aria-expanded=");
    expect(html).toContain(
      'aria-label="View source for Secret dev/skyhook-agent"',
    );
    expect(html).not.toContain("Relationship to target not established");
  });

  it("preserves already-plural resource kinds in inventory titles", () => {
    const projection = project(
      tool("endpoints", "list_resources", [
        { kind: "Endpoints", namespace: "shop", name: "api" },
      ]),
    );
    expect(projection.groups[0].latest.title).toContain("Endpoints");
    expect(projection.groups[0].latest.title).not.toContain("Endpointses");
    expect(render(projection)).not.toContain("<article");
  });

  it("keeps even cited inventories in Activity rather than promoting the whole list", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "secrets",
        "list_resources",
        [{ kind: "Secret", namespace: "autopush", name: "app" }],
        {
          summary: JSON.stringify({
            kind: "secrets",
            namespace: "autopush",
          }),
          evidenceRef: ref,
        },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);
    expect(projection.groups[0].latest.title).toContain("Secrets in autopush");
    expect(html).not.toContain("Secrets in autopush");
    const sources = renderToStaticMarkup(
      <AssessmentSources resolution={resolution} onViewSource={onViewSource} />,
    );
    expect(sources).toContain("List Resources");
    expect(html).not.toContain("Resource inventory");
    expect(html).not.toContain("found in a broader search");
    expect(html).not.toContain('aria-expanded="true"');
    expect(html).not.toContain("<article");
  });

  it("preserves the actual query under collapsed cited sources and puts actions after evidence", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "search",
        "search",
        { hits: [] },
        {
          summary: JSON.stringify({
            query: "kind:Secret project-infra",
            limit: 20,
          }),
          evidenceRef: ref,
        },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, "NEXT-STEPS-SENTINEL", resolution);
    const sources = renderToStaticMarkup(
      <AssessmentSources resolution={resolution} onViewSource={onViewSource} />,
    );
    expect(sources).toContain("kind:Secret project-infra");
    expect(sources).toContain("Sources used for this assessment");
    expect(sources).not.toContain('id="investigation-evidence-');
    expect(html).not.toContain("Cited sources in Activity");
    expect(html).not.toContain(
      "The assessment cites a result that could not be summarized",
    );
    expect(html).not.toContain('aria-controls="investigation-cited-sources"');
    expect(html.indexOf("NEXT-STEPS-SENTINEL")).toBeGreaterThan(
      html.lastIndexOf("</section>"),
    );
  });

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
      tool(
        "events-other",
        "get_events",
        {
          events: [
            {
              type: "Warning",
              reason: "BackOff",
              message: "Back-off restarting failed container",
              count: 1,
              lastTimestamp: "2026-09-02T10:00:00Z",
            },
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
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const originalGroupId = resolution.links[0].originalGroupId!;
    const before = render(projection);
    const html = render(projection, false, undefined, resolution);
    const anchor = `id="${investigationEvidenceSourceDomId(
      resolution.links[0].source.id,
    )}"`;

    expect(html).not.toContain("Cited by the agent");
    expect(html).not.toContain("validated against this run");
    expect(html).not.toContain("Agent-selected check");
    expect(html).not.toContain("from this check below");
    expect(html).not.toContain("Additional Radar observations");
    expect(html.indexOf("CrashLoopBackOff")).toBeLessThan(
      html.indexOf("Kubernetes events"),
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
    expect(html.indexOf(uncitedMessage)).toBeLessThan(
      html.indexOf(citedMessage),
    );
    expect(html).toContain("Used for assessment");
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

    const sources = renderToStaticMarkup(
      <AssessmentSources resolution={resolution} onViewSource={onViewSource} />,
    );
    expect(sources).toContain("Query Prometheus");
    expect(sources).toContain("Sources used for this assessment");
    expect(html).not.toContain("Cited sources in Activity");
    expect(html).not.toContain("Agent-selected check");
    expect(html).not.toContain("View source");
    expect(html).toContain(">Evidence</h2>");
    expect(html).not.toContain("Evidence from cited results");
    expect(html).not.toContain("Additional Radar observations");
    expect(html).toContain("No relevant evidence to show yet");
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
      tool(
        "events-uncited",
        "get_events",
        {
          events: [
            {
              type: "Warning",
              reason: "BackOff",
              message: "Back-off restarting failed container",
              count: 1,
              lastTimestamp: "2026-09-02T10:00:00Z",
            },
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
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);

    expect(resolution.links[0].originalGroupId).toBeUndefined();
    expect(html).toContain(">Evidence</h2>");
    expect(html).toContain("Kubernetes events");
    expect(html).not.toContain("Evidence from cited results");
    expect(html).not.toContain("Additional Radar observations");
    expect(html).not.toContain("No relevant evidence to show yet");
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

    expect(resolution.links[0].originalGroupId).toBeUndefined();
    expect(html).toContain("Resource details");
    expect(html).not.toContain("Agent-selected check");
    expect(
      renderToStaticMarkup(
        <AssessmentSources
          resolution={resolution}
          onViewSource={onViewSource}
        />,
      ),
    ).toContain("View Get Resource source used for this assessment");
    expect(html).toContain("Evidence coverage is incomplete");
    expect(html).toContain("couldn&#x27;t summarize this investigation step");
    expect(html.match(new RegExp(anchor, "g"))).toHaveLength(1);
  });

  it("keeps inline log evidence compact and strips terminal color codes", () => {
    const ref = evidenceRef("a", "b");
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
          evidenceRef: ref,
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-pod",
            container: "api",
          }),
        },
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);

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

  it("encloses evidence items in cards while keeping replica details unboxed", () => {
    const html = render(
      project(
        tool("workload", "diagnose", {
          resource: {
            apiVersion: "apps/v1",
            kind: "Deployment",
            metadata: { namespace: "shop", name: "api" },
          },
          resourceContext: {
            tier: "basic",
            workloadSummary: {
              replicas: { desired: 1, ready: 0, updated: 1, unavailable: 1 },
            },
            statusSummary: {
              conditions: [
                {
                  type: "Available",
                  status: "False",
                  reason: "MinimumReplicasUnavailable",
                },
              ],
            },
          },
          relatedIssues: [criticalIssue],
        }),
        tool("events", "get_events", {
          events: [
            {
              type: "Warning",
              reason: "BackOff",
              message: "Back-off restarting failed container",
              count: 2,
              lastTimestamp: "2026-09-02T10:00:00Z",
            },
          ],
        }),
      ),
    );
    const articles = html.match(/<article\b[^>]*>/g) ?? [];
    expect(articles.length).toBeGreaterThanOrEqual(3);
    for (const article of articles) {
      expect(article).toContain("rounded-lg border bg-theme-surface");
      expect(article).not.toContain("border-b ");
    }
    expect(html).toContain("border-l-red-500");
    expect(html).toContain("border-theme-border/70");
    const replicaFacts = html.match(
      /<dl[^>]*>\s*<div><dt[^>]*>Ready replicas[\s\S]*?<\/dl>/,
    )?.[0];
    expect(replicaFacts).toBeDefined();
    expect(replicaFacts).not.toContain("border");
    expect(replicaFacts).toContain("0/1");
    expect(replicaFacts).toContain("Updated");
    expect(replicaFacts).toContain("Unavailable");
    expect(html).toContain("MinimumReplicasUnavailable");
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

    expect(html).not.toContain("Critical evidence");
    expect(html).not.toContain("strongest");
    expect(html).not.toContain("main proof");
    expect(html).toContain("CrashLoopBackOff");
    expect(html).toContain('aria-label="View source for CrashLoopBackOff"');
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(source.id)}"`,
    );
    expect(source.primaryGroupId).toBe(
      projection.groups.find((group) => group.kind === "issue")?.id,
    );
  });

  it("omits an unmatched broad issue without deleting the raw evidence", () => {
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

    expect(html).not.toContain("Critical evidence");
    expect(html).not.toContain("Related resources and broader checks");
    expect(html).not.toContain("DatabaseCrashLoop");
    expect(projection.groups).toHaveLength(1);
  });

  it("keeps proof-scope provenance without surfacing identical evidence as history", () => {
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
    ).toBe(false);
    expect(
      investigationEvidenceShouldRevealHistory(group, group.latest.source.id),
    ).toBe(false);
    expect(html).not.toContain("Previous observations");
    expect(group.latest.relevance).toBe("producer-related");
    expect(html).not.toContain("Broader context");
    expect(html).not.toContain("later broader observation retained");
  });

  it.each(["critical", "warning"])(
    "shows all distinct current %s headers, with bodies collapsed",
    (severity) => {
      const projection = project(
        ...Array.from({ length: 7 }, (_, index) =>
          tool(`failure-${index}`, "issues", {
            issues: [
              {
                ...criticalIssue,
                id: `failure-${index}`,
                reason: `Failure${index}`,
                severity,
              },
            ],
            total: 1,
            total_matched: 1,
          }),
        ),
      );
      const partition = partitionInvestigationEvidence(projection.groups);
      expect(partition.main).toHaveLength(7);
      expect(partition.workload).toHaveLength(0);
      const html = render(projection);
      expect(html.match(/<article\b/g)).toHaveLength(7);
      expect(html).not.toContain('aria-expanded="true"');
      expect(html).not.toContain("More critical evidence");
      for (const source of projection.sources)
        expect(
          investigationEvidenceRevealCollection(projection, source.id),
        ).toBeUndefined();
    },
  );

  it("keeps neutral supporting observations reachable without an adverse preview", () => {
    const projection = project(
      tool("neutral", "issues", {
        issues: [{ ...criticalIssue, severity: "warning" }],
        total: 1,
        total_matched: 1,
      }),
    );
    projection.groups[0].latest.tone = "neutral";
    const html = render(projection);
    expect(html).toContain("More evidence about this workload");
    expect(html).not.toContain("with warnings or errors");
    expect(
      investigationEvidenceRevealCollection(
        projection,
        projection.sources[0].id,
      ),
    ).toBe("workload");
  });

  it("keeps critical cards and overflow ahead of noncritical cited results", () => {
    const ref = evidenceRef("a", "b");
    const projection = project(
      tool(
        "cited-warning",
        "issues",
        {
          issues: [
            {
              ...criticalIssue,
              id: "warning",
              severity: "warning",
              reason: "CitedWarning",
            },
          ],
          total: 1,
          total_matched: 1,
        },
        { evidenceRef: ref },
      ),
      ...Array.from({ length: 4 }, (_, index) =>
        tool(`critical-${index}`, "issues", {
          issues: [
            {
              ...criticalIssue,
              id: `critical-${index}`,
              reason: `Critical${index}`,
              message: `Critical detail ${index}`,
              cause: "Brief critical summary",
            },
          ],
          total: 1,
          total_matched: 1,
        }),
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    const html = render(projection, false, undefined, resolution);
    expect(html).not.toContain("Critical evidence");
    expect(html).not.toContain("Cited by the agent");
    expect(html.indexOf('aria-label="Critical3 evidence"')).toBeLessThan(
      html.indexOf('aria-label="CitedWarning evidence"'),
    );
    const firstCard = html.match(
      /<article[^>]*aria-label="Critical0 evidence"[\s\S]*?<\/article>/,
    )?.[0];
    expect(firstCard).toContain('aria-expanded="false"');
    for (const source of projection.sources) {
      expect(
        html.split(`id="${investigationEvidenceSourceDomId(source.id)}"`),
      ).toHaveLength(2);
    }
  });

  it("orders critical cited cards first without mutating citation order", () => {
    const refs = [evidenceRef("a", "b"), evidenceRef("a", "c")];
    const projection = project(
      ...["warning", "critical", "critical"].map((severity, index) =>
        tool(
          `ordered-${index}`,
          "issues",
          {
            issues: [
              {
                ...criticalIssue,
                id: `ordered-${index}`,
                reason: `Ordered${index}`,
                severity,
                message: `Detail ${index}`,
                cause: "Brief summary",
              },
            ],
            total: 1,
            total_matched: 1,
          },
          index < 2 ? { evidenceRef: refs[index] } : {},
        ),
      ),
    );
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs },
      0,
    );
    const html = render(projection, false, undefined, resolution);
    expect(html).not.toContain("Cited by the agent");
    expect(html.indexOf('aria-label="Ordered2 evidence"')).toBeLessThan(
      html.indexOf('aria-label="Ordered0 evidence"'),
    );
    expect(html.indexOf('aria-label="Ordered1 evidence"')).toBeLessThan(
      html.indexOf('aria-label="Ordered0 evidence"'),
    );
    expect(resolution.links[0].source.stepId).toBe("ordered-0");
    expect(
      html.match(
        /<article[^>]*aria-label="Ordered1 evidence"[\s\S]*?<\/article>/,
      )?.[0],
    ).toContain('aria-expanded="false"');
    expect(
      html.match(
        /<article[^>]*aria-label="Ordered2 evidence"[\s\S]*?<\/article>/,
      )?.[0],
    ).toContain('aria-expanded="false"');
  });

  it.each(["missing", "invalid"] as const)(
    "keeps %s citation notice ahead of critical facts",
    (status) => {
      const projection = project(
        tool("critical", "issues", {
          issues: [criticalIssue],
          total: 1,
          total_matched: 1,
        }),
      );
      const html = render(projection, false, undefined, { status, links: [] });
      const notice =
        status === "missing"
          ? "Assessment does not cite specific Radar evidence"
          : "Assessment references could not be matched";
      expect(html.indexOf(notice)).toBeGreaterThan(-1);
      expect(html.indexOf(notice)).toBeLessThan(
        html.indexOf("CrashLoopBackOff"),
      );
    },
  );

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
    expect(html).not.toContain('id="investigation-key-evidence-heading"');
    expect(html).not.toContain('id="investigation-evidence-tier-key"');
    expect(html.match(/<article\b/g)).toHaveLength(projection.groups.length);
    expect(html).not.toContain("related to the investigated resource");

    const ordered = render(projection, false, "NEXT_STEP_MARKER");
    expect(ordered.indexOf("CrashLoopBackOff")).toBeGreaterThan(
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
    // Body-rendering fixture: relationship eligibility is tested separately.
    resources.forEach((group) => {
      group.latest.relevance = "producer-related";
    });
    const html = render(projection);

    expect(html).toContain("No key names in this result");
    expect(html).not.toContain("values hidden");
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
    warningResource.latest.relevance = "producer-related";
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
    resource.latest.relevance = "producer-related";
    const html = render(projection);

    expect(resource.observations).toHaveLength(2);
    expect(html).toContain(`${resource.id}-body`);
    expect(html).toContain("Previous observations · 1");
    expect(html).toMatch(
      /aria-expanded="false"[^>]*>[^]*?Previous observations/,
    );
    expect(html).not.toContain("Changed since the previous observation");
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
    expect(html).toContain("More evidence about this workload");
    expect(html).toContain("No matching warning events");
    expect(html).not.toContain("What Radar did not find");
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
    projection.groups.forEach((group) => {
      group.latest.relevance = "producer-related";
    });
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

  it("puts the change age in the header and only raw evidence in the details", () => {
    const evidence = "generation=2, observedGeneration=2, 2 owned ReplicaSets";
    const projection = project(
      tool("rollout", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        changeContext: {
          changed: true,
          what: "pod_template",
          when: "29d",
          evidence,
        },
      }),
    );
    const group = projection.groups.find(
      (item) => item.latest.data.type === "changes",
    )!;
    expect(group.latest.summary).toBe(
      "Pod template changed; newest ReplicaSet created 29d ago",
    );
    const html = render(projection);
    expect(html).toContain(evidence);
    expect(html).toContain('aria-label="About change evidence"');
    expect(html).not.toContain("Why this may matter");
    expect(html).not.toContain("A recent change is context");
    const body = html.split(`id="${group.id}-body"`)[1]?.split("</article>")[0];
    expect(body).toBeDefined();
    expect(body).not.toContain("Pod template changed");
    expect(body).toContain(evidence);
  });

  it("does not offer an empty change disclosure when only the summary is available", () => {
    const projection = project(
      tool("rollout", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        changeContext: { changed: true, what: "pod_template" },
      }),
    );
    const group = projection.groups.find(
      (item) => item.latest.data.type === "changes",
    )!;
    const html = render(projection);
    expect(group.latest.summary).toBe("The workload's Pod template changed");
    expect(html).not.toContain(`aria-controls="${group.id}-body"`);
    expect(html).toContain('aria-label="About change evidence"');
  });

  it("presents qualified change history as a neutral note without repeating its details", () => {
    const projection = project(
      tool("empty-changes", "get_changes", { changes: [] }),
      tool(
        "limited-history",
        "get_resource",
        {
          resource: {
            apiVersion: "v1",
            kind: "Secret",
            metadata: { namespace: "shop", name: "api" },
          },
          recentChanges: [],
          recentChangesSaturated: false,
          recentChangesCoverageLimited: true,
        },
        {
          summary: JSON.stringify({
            kind: "secret",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
    );
    expect(projection.limitations).toHaveLength(2);
    expect(
      projection.limitations.every((item) => item.presentation === "history"),
    ).toBe(true);
    const html = render(projection);
    expect(html).toContain("Change history is limited");
    expect(html).not.toContain("Evidence coverage is incomplete");
    expect(html).not.toContain("access restrictions");
    const header = html.match(
      /<button[^>]*aria-controls="investigation-evidence-coverage"[\s\S]*?<\/button>/,
    )?.[0];
    expect(header).toBeDefined();
    expect(header).not.toContain("No changes were returned");
    expect(header).not.toContain("text-amber");
    expect(html).toContain("Change history for secret shop/api is incomplete.");
    expect(investigationEvidenceCoverageLimited(projection)).toBe(true);
    for (const source of projection.sources) {
      expect(
        html.split(`id="${investigationEvidenceSourceDomId(source.id)}"`),
      ).toHaveLength(2);
    }
  });

  it.each([
    [
      "failed read",
      tool(
        "failure",
        "get_pod_logs",
        { error: "pods/log is forbidden" },
        { isError: true },
      ),
    ],
    ["truncated result", tool("truncated", "issues", {}, { truncated: true })],
    [
      "malformed history",
      tool("malformed", "get_resource", {
        resource: {
          apiVersion: "v1",
          kind: "Secret",
          metadata: { name: "api", namespace: "shop" },
        },
        recentChangesCoverageLimited: "yes",
      }),
    ],
  ])("keeps %s prominent alongside a history note", (_label, failedTool) => {
    const projection = project(
      tool("empty-history", "get_changes", { changes: [] }),
      failedTool,
    );
    const html = render(projection);
    expect(html).toContain("Evidence coverage is incomplete");
    expect(html).not.toContain("Change history is limited");
    expect(investigationEvidenceCoverageLimited(projection)).toBe(true);
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
    expect(html).not.toContain("1 investigation result needs review");
    expect(html).toContain("Evidence coverage update: Issue scan:");
    expect(html).toContain(
      "Only part of this investigation result was saved, so Radar could not summarize it here.",
    );
    expect(html).toContain('aria-label="View source for Issue scan"');
    expect(source.primaryGroupId).toBeUndefined();
    expect(html).toContain(
      `id="${investigationEvidenceSourceDomId(source.id)}"`,
    );
    expect(html).toContain("data-evidence-source-container");
    expect(html).toContain('aria-label="Evidence limitation for Issue scan:');
    expect(html).toContain("focus:ring-2");
  });

  it("prioritizes failed reads in the summary without reordering detailed provenance", () => {
    const projection = project(
      tool("cut-issues", "issues", {}, { truncated: true }),
      tool("cut-events", "get_events", {}, { truncated: true }),
      tool(
        "denied-logs",
        "get_pod_logs",
        { error: "pods/log is forbidden" },
        { isError: true },
      ),
    );
    expect(projection.limitations).toHaveLength(3);
    const originalOrder = projection.limitations.map((item) => item.source);
    const failure = projection.limitations.find(
      (item) => item.kind === "error",
    )!;
    const html = render(projection);
    expect(html).toContain(
      renderToStaticMarkup(
        <>
          Evidence coverage update: {failure.source}: {failure.message}
        </>,
      ),
    );
    expect(projection.limitations.map((item) => item.source)).toEqual(
      originalOrder,
    );
    expect(html).toContain(
      'aria-label="Evidence limitation for Container logs:',
    );
    expect(html).toContain('aria-label="Evidence limitation for Issue scan:');
    expect(html).toContain(
      'aria-label="Evidence limitation for Kubernetes events:',
    );
  });

  it.each([
    ["denied logs", { logsError: "pods/log is forbidden" }],
    [
      "sampled pods",
      {
        logCoverage: {
          selectedPods: 2,
          resolvedPods: 5,
          selectionTruncated: true,
        },
      },
    ],
    ["unavailable changes", { recentChangesError: "timeline unavailable" }],
    [
      "denied previous logs",
      {
        logsPrevious: [
          { pod: "api-0", container: "api", error: "pods/log is forbidden" },
        ],
      },
    ],
  ])("retains %s when the agent reports healthy", (_label, limits) => {
    const projection = project(
      tool("diagnose", "diagnose", {
        resource: {
          kind: "Deployment",
          metadata: { name: "api", namespace: "shop" },
        },
        pods: 0,
        events: [],
        recentChanges: [],
        ...limits,
      }),
    );
    const assessment = renderToStaticMarkup(
      <ResultCard
        diagnosis={
          {
            healthy: true,
            rootCause: "",
            report: "The workload appears ready.",
            remediation: [],
          } as Diagnosis
        }
        coverageLimited={investigationEvidenceCoverageLimited(projection)}
      />,
    );
    expect(assessment).toContain("No problem identified in available evidence");
    expect(assessment).not.toContain("border-emerald-500/30");
    expect(render(projection)).toContain("Evidence coverage is incomplete");
    expect(projection.limitations.length).toBeGreaterThan(0);
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
    expect(collecting).not.toContain("No relevant evidence to show yet");

    expect(finished).not.toContain(">collecting<");
    expect(finished).toContain("No relevant evidence to show yet");
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
    expect(html).toContain("Previous observations");
    expect(html).toContain("Earlier does not mean resolved.");
    expect(html).toContain("CrashLoopBackOff");
  });
});
