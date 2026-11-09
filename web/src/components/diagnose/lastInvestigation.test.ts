import { describe, expect, it } from "vitest";

import type { RunSummary } from "../../api/diagnose";
import {
  LAST_INVESTIGATION_MAX_AGE_MS,
  lastInvestigations,
  sameObject,
} from "./lastInvestigation";
import { runTargetKey } from "./target";

const now = Date.parse("2026-11-02T12:00:00Z");
const hoursAgo = (h: number) => new Date(now - h * 3600_000).toISOString();
const key = runTargetKey("Deployment", "shop", "cart", "apps");

function run(over: Partial<RunSummary>): RunSummary {
  return {
    id: "r1",
    kind: "Deployment",
    group: "apps",
    namespace: "shop",
    name: "cart",
    context: "cluster-a",
    status: "done",
    preview: "Pods crash on a bad flag",
    assessedAt: hoursAgo(2),
    createdAt: hoursAgo(3),
    updatedAt: hoursAgo(1),
    ...over,
  };
}

describe("lastInvestigations", () => {
  it("picks the newest assessment of the resource on the cluster in use", () => {
    const got = lastInvestigations(
      [
        run({ id: "old", assessedAt: hoursAgo(30) }),
        run({ id: "new", assessedAt: hoursAgo(2), preview: "Healthy" }),
        run({
          id: "other-cluster",
          context: "cluster-b",
          assessedAt: hoursAgo(1),
        }),
      ],
      "cluster-a",
      now,
    );
    expect(got.get(key)).toMatchObject({ id: "new", preview: "Healthy" });
  });

  it("counts a run made stale by a cluster switch", () => {
    const got = lastInvestigations(
      [run({ status: "stale" })],
      "cluster-a",
      now,
    );
    expect(got.get(key)?.id).toBe("r1");
  });

  it.each([
    ["a live run", { status: "running" as const }],
    [
      "a question about no resource",
      { name: "", question: "why is checkout slow?" },
    ],
    ["a run with no dated assessment", { assessedAt: undefined }],
    [
      "an assessment past the cutoff",
      {
        assessedAt: new Date(
          now - LAST_INVESTIGATION_MAX_AGE_MS - 1,
        ).toISOString(),
      },
    ],
  ])("skips %s", (_, over) => {
    expect(lastInvestigations([run(over)], "cluster-a", now).size).toBe(0);
  });

  it("matches nothing until the cluster in use is known", () => {
    expect(lastInvestigations([run({})], undefined, now).size).toBe(0);
  });
});

describe("sameObject", () => {
  const last = { id: "r1", preview: "x", assessedAt: hoursAgo(2) };
  it("rejects an object created after the assessment", () => {
    expect(sameObject(last, hoursAgo(1))).toBe(false);
  });
  it("accepts an object recreated mid-run that the assessment saw", () => {
    expect(sameObject(last, hoursAgo(2.5))).toBe(true);
  });
  it("accepts an older object, or one whose creation time is unknown", () => {
    expect(sameObject(last, hoursAgo(48))).toBe(true);
    expect(sameObject(last, undefined)).toBe(true);
  });
});
