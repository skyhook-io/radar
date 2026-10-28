import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { DiagnosisEvidenceItem } from "../../api/diagnose";
import { InvestigationEvidencePane } from "./InvestigationEvidencePane";
import { resolveInvestigationCase } from "./investigationCase";
import {
  projectInvestigationEvidence,
  type InvestigationEvidenceTarget,
  type InvestigationEvidenceTimelineItem,
} from "./investigationEvidence";
import runs from "./testdata/story-runs.json";

// Recorded investigations against the test cluster's fixtures: every Radar
// tool call of the run as the UI received it, and the final verdict.
interface RecordedRun {
  target: InvestigationEvidenceTarget;
  timeline: InvestigationEvidenceTimelineItem[];
  diagnosis: { evidence: DiagnosisEvidenceItem[]; report: string };
}
const recorded = runs as unknown as Record<string, RecordedRun>;

function resolve(run: RecordedRun) {
  const projection = projectInvestigationEvidence(
    [{ timeline: run.timeline }],
    run.target,
  );
  return {
    projection,
    investigationCase: resolveInvestigationCase(projection, run.diagnosis, 0),
  };
}

function renderPane(run: RecordedRun) {
  const { projection, investigationCase } = resolve(run);
  return renderToStaticMarkup(
    <InvestigationEvidencePane
      projection={projection}
      investigationCase={investigationCase}
      story={{ report: run.diagnosis.report, evidence: run.diagnosis.evidence }}
      collecting={false}
      animateGroupIds={new Set()}
      onViewSource={() => {}}
      onViewActivity={() => {}}
    />,
  );
}

describe("a Pod cited for the agent's own Prometheus query", () => {
  // Both runs cite their restart-count query as evidence 2, naming the one
  // Pod the query selects (`pod=~"^(<pod>)$"`) rather than the Deployment.
  const cases = [
    ["run-7885e0cdb0", "api-7c6f9c7bb9-p9czp"],
    ["run-01f749634a", "batch-worker-74b5656749-phx2t"],
  ] as const;

  it.each(cases)("%s pins the citation to the query's chart", (id, pod) => {
    const run = recorded[id];
    expect(run.diagnosis.evidence[2].subject).toMatchObject({
      kind: "Pod",
      name: pod,
      observation: "metrics",
    });
    const item = resolve(run).investigationCase.items.find(
      (candidate) => candidate.index === 2,
    );
    expect(item?.placement).toBe("card");
    expect(item?.source.tool).toBe("query_prometheus");
    expect(item?.observation?.data).toMatchObject({
      type: "metrics",
      origin: "query",
    });
  });

  it.each(cases)("%s loses no cited result in the story", (id) => {
    const html = renderPane(recorded[id]);
    expect(html).not.toContain("could not be shown here");
    expect(html).not.toContain("data-story-lost-support");
  });

  it("does not pin a Pod the query does not select", () => {
    const run = recorded["run-7885e0cdb0"];
    const other = {
      ...run,
      diagnosis: {
        ...run.diagnosis,
        evidence: run.diagnosis.evidence.map((entry, index) =>
          index === 2 && entry.subject
            ? { ...entry, subject: { ...entry.subject, name: "api-other" } }
            : entry,
        ),
      },
    };
    const item = resolve(other).investigationCase.items.find(
      (candidate) => candidate.index === 2,
    );
    expect(item?.placement).toBe("source");
  });
});

describe("run-8b924c437a", () => {
  it("places every story citation", () => {
    const html = renderPane(recorded["run-8b924c437a"]);
    expect(html).not.toContain("could not be shown here");
    expect(html).not.toContain("data-story-lost-support");
  });
});
