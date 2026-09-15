import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { InvestigationEvidencePane } from "./InvestigationEvidencePane";
import { resolveInvestigationCase } from "./investigationCase";
import {
  projectInvestigationEvidence,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import type { DiagnosisEvidenceItem } from "../../api/diagnose";

const target = { kind: "Deployment", group: "apps", namespace: "shop", name: "api" };

function evidenceRef(scope: string, nonce: string): string {
  return `ev_${scope.repeat(26)}_${nonce.repeat(26)}`;
}

function tool(
  id: string,
  name: string,
  result: unknown,
  patch: Partial<Extract<InvestigationEvidenceTimelineItem, { kind: "tool" }>> = {},
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

const crashIssue = {
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

describe("InvestigationEvidencePane under a story", () => {
  const ref = evidenceRef("a", "b");
  const projection = projectInvestigationEvidence(
    [
      {
        timeline: [
          tool("issues", "issues", { issues: [crashIssue], total: 1, total_matched: 1 }, {
            evidenceRef: ref,
          }),
        ],
      },
    ],
    target,
  );
  const evidence: DiagnosisEvidenceItem[] = [
    { status: "linked", ref, role: "cause", claim: "" },
    { status: "unlinked" },
  ];
  const investigationCase = resolveInvestigationCase(projection, { evidence }, 0);
  const render = (report: string) =>
    renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={projection}
        investigationCase={investigationCase}
        story={{ report, evidence }}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
        afterEvidence={<div data-next-steps>steps</div>}
      />,
    );

  it("places the cited card in the story, marks it in the inventory, and puts next steps before it", () => {
    const html = render(
      "The container keeps dying.\n\n[[radar:evidence=0]]\n\nThat is the whole story.",
    );
    expect(html).toContain("data-story");
    expect(html).toContain('id="story-');
    expect(html).toContain("Captured results");
    expect(html).toContain("1 in the analysis");
    expect(html).toContain("In the analysis");
    expect(html).not.toContain(">Evidence<");
    // Next steps sit between the story and the inventory.
    const story = html.indexOf("data-story");
    const steps = html.indexOf("data-next-steps");
    const inventory = html.indexOf("Captured results");
    expect(story).toBeGreaterThan(-1);
    expect(steps).toBeGreaterThan(story);
    expect(inventory).toBeGreaterThan(steps);
    // The inventory is collapsed once the story placed something.
    expect(html).toMatch(/aria-controls="investigation-captured-results"[^>]*/);
    expect(html).toMatch(/aria-expanded="false"[^>]*aria-controls="investigation-captured-results"/);
  });

  it("shows lost support at the sentence and opens the inventory when nothing was placed", () => {
    const html = render("Nothing placed, but see [[radar:evidence=1]] and [[radar:evidence=7]].");
    expect(html).toContain('data-story-lost-support="unlinked"');
    expect(html).toContain('data-story-lost-support="invalid"');
  });

  it("links a bound call that produced no card to its raw result in Activity", () => {
    const searchRef = evidenceRef("e", "f");
    const withSearch = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("issues", "issues", { issues: [crashIssue], total: 1, total_matched: 1 }, {
              evidenceRef: ref,
            }),
            tool("search", "search", { results: [], total: 0 }, {
              evidenceRef: searchRef,
              summary: JSON.stringify({ query: "metrics-server" }),
            }),
          ],
        },
      ],
      target,
    );
    const items: DiagnosisEvidenceItem[] = [
      { status: "linked", ref: searchRef, role: "rules_out", claim: "Nothing named metrics-server exists." },
    ];
    const html = renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={withSearch}
        investigationCase={resolveInvestigationCase(withSearch, { evidence: items }, 0)}
        story={{ report: "No metrics-server anywhere.\n\n[[radar:evidence=0]]", evidence: items }}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
      />,
    );
    expect(html).toContain('data-story-lost-support="nocard"');
    expect(html).toContain("View it in Activity");
    expect(html).toContain("None placed in the analysis");
    expect(html).toMatch(/aria-expanded="true"[^>]*aria-controls="investigation-captured-results"/);
  });

  it("places a subject-less citation of a fan-out call on that call's primary card", () => {
    const bundleRef = evidenceRef("c", "d");
    const bundle = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "diag",
              "diagnose",
              {
                resource: {
                  apiVersion: "apps/v1",
                  kind: "Deployment",
                  metadata: { name: "api", namespace: "shop" },
                  status: { replicas: 1, readyReplicas: 0 },
                },
                issues: [crashIssue],
                events: [],
              },
              { evidenceRef: bundleRef },
            ),
          ],
        },
      ],
      target,
    );
    const items: DiagnosisEvidenceItem[] = [
      { status: "linked", ref: bundleRef, role: "context", claim: "" },
    ];
    const bundleCase = resolveInvestigationCase(bundle, { evidence: items }, 0);
    // The resolver itself leaves the item at its source: several cards match.
    expect(bundleCase.items[0]?.placement).toBe("source");
    const html = renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={bundle}
        investigationCase={bundleCase}
        story={{ report: "Radar's bundle says so.\n\n[[radar:evidence=0]]", evidence: items }}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
      />,
    );
    expect(html).toContain('data-story-placement="0"');
    expect(html).not.toContain("data-story-lost-support");
    expect(html).toContain("1 in the analysis");
  });

  it("renders a cited earlier read as that read, labelled, when a newer one exists", () => {
    const early = evidenceRef("a", "b");
    const later = evidenceRef("c", "d");
    const twoReads = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("issues-early", "issues", { issues: [crashIssue], total: 1, total_matched: 1 }, {
              evidenceRef: early,
            }),
          ],
        },
        {
          timeline: [
            tool(
              "issues-later",
              "issues",
              {
                issues: [{ ...crashIssue, message: "The API container keeps restarting, 40 times now." }],
                total: 1,
                total_matched: 1,
              },
              { evidenceRef: later },
            ),
          ],
        },
      ],
      target,
    );
    const items: DiagnosisEvidenceItem[] = [
      { status: "linked", ref: early, role: "cause", claim: "" },
    ];
    // A revised assessment in turn 1 citing the read from turn 0.
    const html = renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={twoReads}
        investigationCase={resolveInvestigationCase(twoReads, { evidence: items }, 1)}
        story={{ report: "It was crashing then.\n\n[[radar:evidence=0]]", evidence: items }}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
      />,
    );
    expect(html).toContain('data-story-placement="0"');
    expect(html).toContain("Captured in turn 1");
    expect(html).toContain("a newer read");
  });

  it("binds a subject-less earlier citation to that call's own read, never a later one", () => {
    const early = evidenceRef("a", "b");
    const later = evidenceRef("c", "d");
    const bundleResult = (ready: number) => ({
      resource: {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { name: "api", namespace: "shop" },
        status: { replicas: 1, readyReplicas: ready },
      },
      issues: [crashIssue],
      events: [],
    });
    const twoBundles = projectInvestigationEvidence(
      [
        { timeline: [tool("diag-early", "diagnose", bundleResult(0), { evidenceRef: early })] },
        { timeline: [tool("diag-later", "diagnose", bundleResult(1), { evidenceRef: later })] },
      ],
      target,
    );
    const items: DiagnosisEvidenceItem[] = [
      { status: "linked", ref: early, role: "cause", claim: "" },
    ];
    const html = renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={twoBundles}
        investigationCase={resolveInvestigationCase(twoBundles, { evidence: items }, 1)}
        story={{ report: "Then it was down.\n\n[[radar:evidence=0]]", evidence: items }}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
      />,
    );
    const placed = html.slice(html.indexOf('data-story-placement="0"'));
    expect(placed).toContain('data-evidence-source="turn-0-step-diag-early"');
    expect(placed).toContain("Captured in turn 1");
  });

  it("keeps the previous layout without a story", () => {
    const html = renderToStaticMarkup(
      <InvestigationEvidencePane
        projection={projection}
        investigationCase={investigationCase}
        collecting={false}
        animateGroupIds={new Set()}
        onViewSource={() => {}}
        onViewActivity={() => {}}
        afterEvidence={<div data-next-steps>steps</div>}
      />,
    );
    expect(html).toContain(">Evidence<");
    expect(html).not.toContain("Captured results");
    expect(html.indexOf("data-next-steps")).toBeGreaterThan(html.indexOf("More evidence about this resource"));
  });
});
