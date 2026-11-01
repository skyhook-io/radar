import { describe, expect, it } from "vitest";
import { groupsOf, project, tool } from "../evidenceFixtures";
import fixture from "./testdata/get-changes-created.json";
import { createdChanges } from "./changes";

// The fixture is written by the Go test TestCompactCreatesSharedFixture from
// the producer's own CompactCreates, so this checks both ends of the contract.
function cardChanges(result: unknown) {
  const projection = project([tool("changes", "get_changes", result)]);
  const group = groupsOf(projection.groups, "changes")[0];
  const data = group?.latest.data;
  return { projection, changes: data?.type === "changes" ? data.changes : [] };
}

describe("get_changes created runs", () => {
  it("expand into the exact rows and order the producer replaced", () => {
    const original = cardChanges(fixture.original);
    const compacted = cardChanges(fixture.compacted);
    expect(compacted.changes).toEqual(original.changes);
    expect(compacted.changes).toHaveLength(fixture.original.changes.length);
    expect(compacted.projection.limitations).toEqual(
      original.projection.limitations,
    );
  });

  it("rebuilds each timestamp in the zone of first", () => {
    const rows = createdChanges([
      {
        namespace: "shop",
        first: "2026-10-16T23:59:30+03:00",
        count: 1,
        objects: ["+41s Service/cart"],
      },
    ]);
    expect(rows).toEqual([
      {
        changeType: "add",
        summary: "add",
        change_category: "lifecycle",
        rank_reason: "resource create/delete for config or desired state",
        kind: "Service",
        namespace: "shop",
        name: "cart",
        timestamp: "2026-10-17T00:00:11+03:00",
      },
    ]);
  });

  it("expands an absent created field to no rows", () => {
    expect(createdChanges(undefined)).toEqual([]);
  });

  it("rejects a malformed run instead of dropping its objects", () => {
    for (const created of [
      [
        {
          namespace: "shop",
          first: "2026-10-16T04:43:24+03:00",
          count: 1,
          objects: ["Service/cart"],
        },
      ],
      [
        {
          namespace: "shop",
          first: "not a time",
          count: 1,
          objects: ["+0s Service/cart"],
        },
      ],
      [
        {
          first: "2026-10-16T04:43:24+03:00",
          count: 1,
          objects: ["+0s Service/cart"],
        },
      ],
      [
        {
          namespace: "shop",
          first: "2026-10-16T04:43:24+03:00",
          count: 1,
          objects: ["+0s Service/cart"],
          apiVersions: { Service: "" },
        },
      ],
      [
        {
          namespace: "shop",
          first: "2026-10-16T04:43:24+03:00",
          count: 5,
          objects: ["+0s Service/cart"],
        },
      ],
      [
        {
          namespace: "shop",
          first: "2026-02-30T04:43:24+03:00",
          count: 1,
          objects: ["+0s Service/cart"],
        },
      ],
      [
        {
          namespace: "shop",
          first: "2026-10-16T04:43:24+03:00",
          count: 1,
          objects: ["+99999999999999999999s Service/cart"],
        },
      ],
      { namespace: "shop" },
    ]) {
      expect(createdChanges(created)).toBeUndefined();
      const { projection } = cardChanges({ changes: [], created });
      expect(groupsOf(projection.groups, "changes")).toHaveLength(0);
      expect(projection.limitations).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ source: "Recent changes" }),
        ]),
      );
    }
  });
});

describe("get_changes omittedKinds", () => {
  const sentence =
    "This window also has changes to kinds the default feed leaves out: BGPConfiguration, Node. Pass kind=<Kind> to list them.";
  const omittedKinds = [
    {
      kind: "BGPConfiguration",
      group: "crd.projectcalico.org",
      latestChangeType: "add",
      latestTimestamp: "2026-11-01T10:03:00Z",
    },
    {
      kind: "Node",
      latestChangeType: "update",
      latestTimestamp: "2026-11-01T10:01:00Z",
    },
  ];

  it("shows the kinds as a routine note, not as a truncated result", () => {
    const projection = project([
      tool("changes", "get_changes", {
        changes: [],
        narrowHint: sentence,
        omittedKinds,
      }),
    ]);
    expect(projection.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Other changed kinds",
          kind: "unknown",
          presentation: "history",
          message:
            "Changed in this window but outside the default change feed: BGPConfiguration.crd.projectcalico.org (last created 2026-11-01T10:03:00Z), Node (last edited 2026-11-01T10:01:00Z).",
        }),
      ]),
    );
    expect(
      projection.limitations.some((entry) => entry.kind === "truncated"),
    ).toBe(false);
  });

  it("keeps the rest of narrowHint as a truncation note", () => {
    const projection = project([
      tool("changes", "get_changes", {
        changes: [],
        narrowHint: `Radar fetched every recorded change in this window but returned only the highest-ranked. ${sentence}`,
        omittedKinds,
      }),
    ]);
    expect(
      projection.limitations.filter((entry) => entry.kind === "truncated"),
    ).toHaveLength(1);
  });

  it("rejects a malformed entry", () => {
    const projection = project([
      tool("changes", "get_changes", {
        changes: [],
        omittedKinds: [{ kind: "Node" }],
      }),
    ]);
    expect(projection.limitations).toEqual([
      expect.objectContaining({
        source: "Other changed kinds",
        message:
          "Radar couldn't summarize this investigation step. Review it in Activity.",
      }),
    ]);
  });
});
