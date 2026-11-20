import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type {
  DiagnosisEvidenceItem,
  DiagnosisRuledOut,
} from "../../api/diagnose";
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
  diagnosis: {
    evidence: DiagnosisEvidenceItem[];
    report: string;
    ruledOut?: DiagnosisRuledOut[];
  };
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

describe("a Pod cited for a target query that selects a sibling", () => {
  it("does not pin the sibling to the target's chart", () => {
    const run = recorded["run-7885e0cdb0"];
    // `deployment="api"` makes the query target-scoped while the pod
    // matcher names a Pod nothing established as the target's own.
    const timeline = run.timeline.map((item) => {
      if (item.kind !== "tool" || item.tool !== "query_prometheus") return item;
      const result = JSON.parse(item.result ?? "{}");
      result.selectors = [
        {
          metric: "kube_pod_container_status_restarts_total",
          matchers: [
            { label: "namespace", op: "=", value: "gl-crash-config" },
            { label: "deployment", op: "=", value: "api" },
            { label: "pod", op: "=", value: "sibling-0" },
          ],
        },
      ];
      return { ...item, result: JSON.stringify(result) };
    });
    const evidence = run.diagnosis.evidence.map((entry, index) =>
      index === 2 && entry.subject
        ? { ...entry, subject: { ...entry.subject, name: "sibling-0" } }
        : entry,
    );
    const { projection, investigationCase } = resolve({
      ...run,
      timeline,
      diagnosis: { ...run.diagnosis, evidence },
    });
    const chart = projection.groups
      .flatMap((group) => group.observations)
      .find((observation) => observation.source.tool === "query_prometheus");
    expect(chart?.relevance).toBe("target");
    const item = investigationCase.items.find(
      (candidate) => candidate.index === 2,
    );
    expect(item?.placement).toBe("source");
  });
});

describe("a Pod cited for a diagnose bundle's chart", () => {
  // The herring run rules out memory pressure on the bundle's memory chart,
  // naming the Deployment's one Pod: `{kind: Pod, observation:
  // "metrics:memory"}` against charts that state the Deployment.
  const run = recorded["run-01f749634a"];
  const itemThree = (candidate: RecordedRun) =>
    resolve(candidate).investigationCase.items.find((item) => item.index === 3);
  // Rewrites the diagnose payload, the source of both the charts and the
  // pods Radar establishes as the target's own.
  const withDiagnose = (
    edit: (payload: Record<string, unknown>) => void,
  ): RecordedRun => ({
    ...run,
    timeline: run.timeline.map((item) => {
      if (item.kind !== "tool" || item.tool !== "diagnose") return item;
      const payload = JSON.parse(item.result ?? "{}");
      edit(payload);
      return { ...item, result: JSON.stringify(payload) };
    }),
  });

  it("pins the citation to the chart its category names when the target has one Pod", () => {
    expect(run.diagnosis.evidence[3].subject).toMatchObject({
      kind: "Pod",
      name: "batch-worker-74b5656749-phx2t",
      observation: "metrics:memory",
    });
    const item = itemThree(run);
    expect(item?.placement).toBe("card");
    expect(item?.source.tool).toBe("diagnose");
    expect(item?.groupId).toBe(
      resolve(run).projection.groups.find(
        (group) =>
          group.kind === "metrics" &&
          group.identity.endsWith(":memory") &&
          group.latest.source.tool === "diagnose",
      )?.id,
    );
  });

  it("does not pin one Pod of a target with two", () => {
    const twoPods = withDiagnose((payload) => {
      payload.pods = 2;
      payload.podNames = [
        "batch-worker-74b5656749-phx2t",
        "batch-worker-74b5656749-zzzzz",
      ];
      (payload.metrics as { pods: number }).pods = 2;
    });
    expect(resolve(twoPods).projection.targetPods).toHaveLength(2);
    expect(itemThree(twoPods)?.placement).toBe("source");
  });

  it("does not pin a Pod when the bundle's pod list was cut short", () => {
    const truncated = withDiagnose((payload) => {
      payload.podNamesTruncated = true;
    });
    expect(itemThree(truncated)?.placement).toBe("source");
  });

  it("keeps the citation pinned when a later turn diagnoses a replacement Pod", () => {
    // A follow-up reads the workload again after its Pod was replaced. The
    // first chart still covered only the first Pod, so the first assessment's
    // citation stays on that read, now a revision under the newer chart.
    const diagnose = run.timeline.find(
      (item) => item.kind === "tool" && item.tool === "diagnose",
    )!;
    const payload = JSON.parse(
      (diagnose as { result?: string }).result ?? "{}",
    );
    payload.podNames = ["batch-worker-74b5656749-new01"];
    const later = {
      ...diagnose,
      id: "toolu_followup_diagnose",
      result: JSON.stringify(payload),
      evidenceRef: undefined,
    } as InvestigationEvidenceTimelineItem;
    const projection = projectInvestigationEvidence(
      [{ timeline: run.timeline }, { timeline: [later] }],
      run.target,
    );
    expect(projection.targetPods).toHaveLength(2);
    const item = resolveInvestigationCase(
      projection,
      run.diagnosis,
      0,
    ).items.find((candidate) => candidate.index === 3);
    expect(item?.placement).toBe("revision");
    expect(item?.observation?.source.id).toBe(item?.source.id);
  });

  it("does not pin a Pod to a chart that summed more than one", () => {
    const summedTwo = withDiagnose((payload) => {
      (payload.metrics as { pods: number }).pods = 2;
    });
    expect(itemThree(summedTwo)?.placement).toBe("source");
  });

  it("does not pin a Pod the target does not have", () => {
    const other: RecordedRun = {
      ...run,
      diagnosis: {
        ...run.diagnosis,
        evidence: run.diagnosis.evidence.map((entry, index) =>
          index === 3 && entry.subject
            ? {
                ...entry,
                subject: { ...entry.subject, name: "batch-worker-other" },
              }
            : entry,
        ),
      },
    };
    expect(itemThree(other)?.placement).toBe("source");
  });

  it("does not pick one of the bundle's charts for a Pod citation without a category", () => {
    const bare: RecordedRun = {
      ...run,
      diagnosis: {
        ...run.diagnosis,
        evidence: run.diagnosis.evidence.map((entry, index) =>
          index === 3 && entry.subject
            ? {
                ...entry,
                subject: { ...entry.subject, observation: "metrics" },
              }
            : entry,
        ),
      },
    };
    expect(itemThree(bare)?.placement).toBe("source");
  });
});

describe("a healthy verdict's Pod citations (run-40c65bb5ed)", () => {
  // boutique/adservice: one ready Pod. The agent rules out restarts on its own
  // 7-day query and resource pressure on the bundle's memory chart, naming
  // the Pod both times.
  const run = recorded["run-40c65bb5ed"];

  it("places both citations and keeps both ruled-out hypotheses", () => {
    const { investigationCase } = resolve(run);
    const byIndex = (index: number) =>
      investigationCase.items.find((item) => item.index === index);
    expect(byIndex(2)).toMatchObject({
      placement: "card",
      source: { tool: "query_prometheus" },
    });
    expect(byIndex(3)).toMatchObject({
      placement: "card",
      source: { tool: "diagnose" },
    });
    expect(byIndex(3)?.observation?.data).toMatchObject({
      type: "metrics",
      origin: "diagnose",
      label: "Memory working set",
    });
    expect(investigationCase.ruledOut.map((entry) => entry.hypothesis)).toEqual(
      [
        "Crash loop or OOM-kill restarts",
        "Container pressing against its memory/CPU limits",
      ],
    );
  });

  it("loses no cited result in the story", () => {
    const html = renderPane(run);
    expect(html).not.toContain("could not be shown here");
    expect(html).not.toContain("data-story-lost-support");
  });
});

describe("run-8b924c437a", () => {
  it("places every story citation", () => {
    const html = renderPane(recorded["run-8b924c437a"]);
    expect(html).not.toContain("could not be shown here");
    expect(html).not.toContain("data-story-lost-support");
  });
});
