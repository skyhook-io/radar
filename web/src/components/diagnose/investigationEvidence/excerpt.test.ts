import { describe, expect, it } from "vitest";
import { storyExcerpt } from "./excerpt";
import type { InvestigationEvidenceObservation } from "./types";

function observation(
  data: InvestigationEvidenceObservation["data"],
): InvestigationEvidenceObservation {
  return {
    source: { id: "s1", turnIndex: 0 },
    revision: 1,
    title: "t",
    data,
  } as unknown as InvestigationEvidenceObservation;
}

describe("storyExcerpt", () => {
  it("keeps a log card's newest two selected lines even when compact, the rest behind the fold", () => {
    const logs = observation({
      type: "logs",
      pod: "api-1",
      container: "api",
      previous: false,
      warnings: [],
      logs: { lines: ["a", "b", "c", "d"], totalLines: 4, matchedLines: 4 },
    } as never);
    expect(storyExcerpt(logs, [], { compact: true })).toEqual({
      kind: "lines",
      head: ["c", "d"],
      rest: ["a", "b"],
    });
  });

  it("shows a placed chart whatever its role, folding a compact one the story does not rest on", () => {
    const metrics = observation({ type: "metrics", origin: "query" } as never);
    expect(
      storyExcerpt(metrics, [{ role: "context" }], { compact: false }),
    ).toEqual({ kind: "chart", size: "short", folded: false });
    expect(
      storyExcerpt(metrics, [{ role: "rules_out" }], { compact: true }),
    ).toEqual({ kind: "chart", size: "short", folded: true });
    // The agent marks a card compact for the prose's flow, so a symptom
    // placed compact still starts open.
    expect(
      storyExcerpt(metrics, [{ role: "symptom" }], { compact: true }),
    ).toEqual({ kind: "chart", size: "full", folded: false });
    expect(
      storyExcerpt(metrics, [{ role: "context" }, { role: "cause" }], {
        compact: true,
      }),
    ).toEqual({ kind: "chart", size: "full", folded: false });
    expect(
      storyExcerpt(metrics, [{ role: "cause" }], {
        compact: true,
        preview: true,
      }),
    ).toBeUndefined();
  });

  it("picks the workload's own rows from a ranking and the findings on it from a posture card", () => {
    const ranking = observation({
      type: "ranking",
      kind: "pods",
      sort: "memory",
      scope: "namespace shop",
      rows: [
        { kind: "Pod", name: "other", cpu: "1m", memory: "1Mi", target: false },
        { kind: "Pod", name: "api-1", cpu: "2m", memory: "2Mi", target: true },
      ],
    } as never);
    expect(storyExcerpt(ranking, [], { compact: false })).toMatchObject({
      kind: "ranking",
      rows: [{ name: "api-1" }],
    });
    expect(
      storyExcerpt(ranking, [{ subject: { kind: "Pod", name: "other" } }], {
        compact: false,
      }),
    ).toMatchObject({
      kind: "ranking",
      rows: [{ name: "other" }, { name: "api-1" }],
    });
    const posture = observation({
      type: "posture",
      source: "audit",
      scope: "namespace shop",
      findings: [
        {
          kind: "Deployment",
          name: "api",
          check: "runAsRoot",
          severity: "high",
          message: "m",
          target: true,
        },
      ],
    } as never);
    expect(storyExcerpt(posture, [], { compact: false })).toMatchObject({
      kind: "posture",
      findings: [{ check: "runAsRoot" }],
    });
  });

  it("shows the rows a citation names on a listing, and nothing for an unnamed one", () => {
    const listing = observation({
      type: "inventory",
      scope: "namespace shop",
      resources: [{ kind: "ConfigMap", namespace: "shop", name: "cfg" }],
    } as never);
    expect(storyExcerpt(listing, [], { compact: false })).toBeUndefined();
    expect(
      storyExcerpt(
        listing,
        [{ subject: { kind: "ConfigMap", namespace: "shop", name: "cfg" } }],
        { compact: false },
      ),
    ).toMatchObject({ kind: "rows", entries: [{ matches: 1 }] });
  });
});
