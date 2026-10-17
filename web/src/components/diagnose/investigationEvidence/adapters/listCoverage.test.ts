import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type {
  Diagnosis,
  DiagnosisEvidenceItem,
} from "../../../../api/diagnose";
import { ResultCard } from "../../AssessmentCard";
import { InvestigationEvidencePane } from "../../InvestigationEvidencePane";
import { resolveInvestigationCase } from "../../investigationCase";
import { investigationEvidenceCoverageLimited } from "../../investigationState";
import { StoryExcerpt } from "../../StoryExcerpt";
import { namedInventoryRows } from "../bodies/inventory";
import type { InvestigationEvidenceTimelineItem } from "../types";
import { evidenceRef, groupsOf, project, tool } from "../evidenceFixtures";

// The list_resources results a partial read returns over MCP: narrowed to the
// namespaces Radar collects (alpha, gamma), with gamma still loading. internal/mcp
// pins them to the producer and internal/ai proves every agent backend hands
// their joined text blocks to Findings unchanged; this suite projects that
// text the way Findings does.
const FIXTURE = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../../../../../testdata/mcp/list-resources-partial-coverage.json",
);

interface FixtureCase {
  name: string;
  arguments: Record<string, unknown>;
  result: { content: { type: string; text: string }[] };
}

const cases: FixtureCase[] = JSON.parse(readFileSync(FIXTURE, "utf8")).cases;

function projectCase(
  name: string,
  ref?: string,
  preceding: InvestigationEvidenceTimelineItem[] = [],
) {
  const fixture = cases.find((item) => item.name === name);
  if (!fixture) throw new Error(`no fixture case ${name}`);
  return project([
    ...preceding,
    tool(
      "list",
      "list_resources",
      fixture.result.content.map((block) => block.text).join(""),
      { summary: JSON.stringify(fixture.arguments), evidenceRef: ref },
    ),
  ]);
}

describe("a list Radar read only in some of the namespaces it collects", () => {
  it("shows the rows it read with the namespaces they came from", () => {
    const projection = projectCase("partial", undefined, [
      tool("diagnose", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { name: "api", namespace: "shop" },
        },
        pods: 0,
        events: [],
        recentChanges: [],
      }),
    ]);
    const [inventory] = groupsOf(projection.groups, "inventory");
    if (inventory?.latest.data.type !== "inventory") {
      throw new Error("expected an inventory card");
    }
    expect(
      inventory.latest.data.resources.map((row) => [row.namespace, row.name]),
    ).toEqual([["alpha", "alpha-object"]]);
    expect(inventory.latest.summary).toBe("1 returned · only alpha read");
    expect(inventory.latest.data.namespacesRead).toEqual(["alpha"]);
    expect(projection.limitations).toEqual([]);
    const coverageLimited = investigationEvidenceCoverageLimited(
      projection,
      "Deployment",
    );
    expect(coverageLimited).toBe(false);
    const html = renderToStaticMarkup(
      createElement(ResultCard, {
        diagnosis: {
          healthy: true,
          rootCause: "",
          report: "The workload appears healthy.",
          remediation: [],
        } as Diagnosis,
        coverageLimited,
      }),
    );
    expect(html).toContain("border-emerald-500/30");
    expect(html).not.toContain("No problem identified in available evidence");
    expect(html).not.toContain("Evidence coverage is limited");
  });

  it("does not present an empty partial read as an empty cluster", () => {
    const projection = projectCase("partial and empty");
    expect(groupsOf(projection.groups, "inventory")).toEqual([]);
    expect(projection.limitations.map((item) => item.message)).toEqual([
      'Radar found no services in namespace "alpha". Not read: namespace "gamma", because Radar is still loading services there. Services may exist there that this list doesn\'t show. Other namespaces weren\'t read either: Radar collects services only in namespaces "alpha", "gamma"; whoever installed Radar can add namespaces with --namespaces through the chart\'s extraArgs, if its service account can list services there.',
    ]);
    expect(investigationEvidenceCoverageLimited(projection, "Service")).toBe(
      true,
    );
  });

  it("says what a listing that read unnamed namespaces shows about a citation", () => {
    const namespaces = Array.from(
      { length: 40 },
      (_, index) => `team-${String(index + 1).padStart(2, "0")}`,
    );
    const coverage = {
      status: "partial",
      scope: { type: "all_namespaces" },
      read: {
        type: "namespaces",
        namespaces: { count: 40 },
      },
      gaps: [{ code: "collector_scope_limited" }],
    };
    const summary =
      "These are the services in 40 namespaces only. Not read: part of the scope, because Radar doesn't collect services there. Services may exist there that this list doesn't show.";
    const rows = [
      { kind: "Service", name: "present", namespace: namespaces[0] },
      { kind: "Service", name: "observed", namespace: "team-39" },
    ];
    const projection = project([
      tool(
        "list",
        "list_resources",
        { summary, coverage, items: rows },
        {
          summary: JSON.stringify({ kind: "services" }),
        },
      ),
    ]);
    const [inventory] = groupsOf(projection.groups, "inventory");
    if (inventory.latest.data.type !== "inventory") {
      throw new Error("expected an inventory card");
    }
    const data = inventory.latest.data;
    expect(data.namespacesRead).toEqual([]);
    expect(data.namespacesReadOmitted).toBe(40);
    expect(inventory.latest.summary).toBe(
      "2 returned · only 40 namespaces read",
    );
    const entries = namedInventoryRows(
      data.resources,
      [
        { subject: { kind: "Service", name: "api" } },
        { subject: { kind: "Service", name: "api", namespace: "team-40" } },
        { subject: { kind: "Service", name: "api", namespace: "unread" } },
        { subject: { kind: "Service", name: "api", namespace: "team-39" } },
      ],
      undefined,
      data.namespacesRead,
      data.namespacesReadOmitted,
    );
    expect(entries[0].readUnnamed).toEqual({ count: 40, namespace: undefined });
    expect(entries[1].readUnnamed).toEqual({ count: 40, namespace: "team-40" });
    expect(entries[2].readUnnamed).toEqual({ count: 40, namespace: "unread" });
    expect(entries[2].notRead).toBeUndefined();
    expect(entries[3].readUnnamed).toBeUndefined();
    expect(entries[3].notRead).toBeUndefined();
    const html = renderToStaticMarkup(
      createElement(StoryExcerpt, {
        excerpt: { kind: "rows", entries },
        data,
        prominence: "primary",
        open: false,
        canExpand: false,
      }),
    );
    expect(html).toContain("api: not in the 40 namespaces read");
    expect(html).toContain(
      "team-40/api: not in this listing, which read 40 namespaces without naming them all; team-40 may not have been one",
    );
    expect(html).not.toContain("team-04");
    expect(html).not.toContain("Not in this listing: team-40/api");
    expect(html).toContain("Not in this listing: team-39/api");
    expect(html).not.toContain("unread/api: unread was not read");
    expect(html).toContain(
      "unread/api: not in this listing, which read 40 namespaces without naming them all; unread may not have been one",
    );
    const empty = project([
      tool("empty", "list_resources", { summary, coverage, items: [] }),
    ]);
    expect(empty.limitations.map((item) => item.message)).toEqual([summary]);
    expect(empty.limitations[0].message).not.toContain("team-09");
  });

  it("names the resource Radar read, not the alias the agent asked for", () => {
    const projection = projectCase("partial and empty, kind by alias");
    const [message] = projection.limitations.map((item) => item.message);
    expect(message).toContain("Radar found no services in");
    expect(message).not.toMatch(/svcs/i);
  });

  it("says a cited name's namespace was not read instead of calling it absent", () => {
    const ref = evidenceRef("a", "b");
    const projection = projectCase("partial", ref);
    const evidence: DiagnosisEvidenceItem[] = [
      { namespace: "beta", claim: "Service api is missing from beta." },
      { namespace: "alpha", claim: "Service api is missing from alpha." },
      { namespace: undefined, claim: "No Service api exists." },
    ].map(({ namespace, claim }) => ({
      status: "linked",
      ref,
      role: "cause",
      claim,
      subject: { kind: "Service", namespace, name: "api" },
    }));
    const investigationCase = resolveInvestigationCase(
      projection,
      { evidence },
      0,
    );
    expect(investigationCase.items.map((item) => item.placement)).toEqual([
      "card",
      "card",
      "card",
    ]);
    const html = renderToStaticMarkup(
      createElement(InvestigationEvidencePane, {
        projection,
        investigationCase,
        story: {
          report: "The Ingress has no backend.\n\n[[radar:evidence=0]]",
          evidence,
        },
        collecting: false,
        animateGroupIds: new Set<string>(),
        onViewSource: () => {},
        onViewActivity: () => {},
      }),
    );
    const start = html.indexOf("data-story-inventory-rows");
    expect(start).toBeGreaterThan(-1);
    const block = html.slice(start, html.indexOf('-body"', start));
    // beta was never read, so the listing cannot say api is missing there.
    expect(block).toContain("beta/api: beta was not read");
    expect(block).not.toContain("Not in this listing: beta/api");
    // alpha was read and has no api: there the listing does show absence.
    expect(block).toContain("Not in this listing: alpha/api");
    // An unqualified name was looked for only where the listing read.
    expect(block).toContain("api: not in the namespaces read (alpha)");
    expect(block).not.toContain("Not in this listing: api<");
  });

  it("keeps a complete list's rows and copy unchanged", () => {
    const complete = project([
      tool(
        "list",
        "list_resources",
        [{ kind: "Service", name: "alpha-object", namespace: "alpha" }],
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    const [inventory] = groupsOf(complete.groups, "inventory");
    expect(inventory.latest.summary).toBe("1 returned");
    expect(inventory.latest.data).not.toHaveProperty("namespacesRead");
    expect(complete.limitations).toEqual([]);
    const empty = project([
      tool("list", "list_resources", [], {
        summary: JSON.stringify({ kind: "services" }),
      }),
    ]);
    expect(empty.limitations.map((item) => item.message)).toEqual([
      "Radar found no matching resources for services resources. Anything you do not have permission to read was not searched.",
    ]);
  });

  it("does not guess at a coverage object it cannot read", () => {
    for (const payload of [
      { items: [], coverage: { complete: false } },
      { items: [], coverage: { complete: true, namespaces: ["alpha"] } },
      { items: {}, coverage: { complete: false, namespaces: ["alpha"] } },
    ]) {
      const projection = project([
        tool("list", "list_resources", payload, {
          summary: JSON.stringify({ kind: "services" }),
        }),
      ]);
      expect(projection.groups).toEqual([]);
      expect(projection.limitations.map((item) => item.message)).toEqual([
        "Radar couldn't summarize this investigation step. Review it in Activity.",
      ]);
    }
  });
});

describe("a list Radar read all of", () => {
  const rows = [{ kind: "Service", name: "api", namespace: "alpha" }];

  it("shows rows without a coverage caveat when nothing narrowed the scope", () => {
    const coverage = {
      status: "complete",
      scope: { type: "all_namespaces" },
      read: { type: "all_namespaces" },
      gaps: [],
    };
    const projection = project([
      tool(
        "list",
        "list_resources",
        {
          summary: "These are all the services in every namespace.",
          coverage,
          items: rows,
        },
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    const [inventory] = groupsOf(projection.groups, "inventory");
    expect(inventory.latest.summary).toBe("1 returned");
    expect(projection.limitations).toEqual([]);
    const empty = project([
      tool(
        "empty",
        "list_resources",
        {
          summary: "There are no services in any namespace.",
          coverage,
          items: [],
        },
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    expect(empty.limitations.map((item) => item.message)).toEqual([
      "There are no services in any namespace.",
    ]);
  });

  it("keeps a narrowed scope from reading as the cluster", () => {
    const coverage = {
      status: "complete",
      scope: {
        type: "namespaces",
        namespaces: { count: 2, names: ["alpha", "beta"] },
      },
      narrowedBy: ["caller_access"],
      read: {
        type: "namespaces",
        namespaces: { count: 2, names: ["alpha", "beta"] },
      },
      gaps: [],
    };
    const summary =
      'These are the services in namespaces "alpha", "beta" only: your access covers only those namespaces. Other namespaces may have services this list doesn\'t show.';
    const emptySummary =
      'There are no services in namespaces "alpha", "beta": your access covers only those namespaces. Other namespaces may have services this list doesn\'t show.';
    const projection = project([
      tool(
        "list",
        "list_resources",
        { summary, coverage, items: rows },
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    const [inventory] = groupsOf(projection.groups, "inventory");
    expect(inventory.latest.summary).toBe("1 returned · only alpha, beta read");
    const empty = project([
      tool(
        "empty",
        "list_resources",
        { summary: emptySummary, coverage, items: [] },
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    expect(empty.limitations.map((item) => item.message)).toEqual([
      emptySummary,
    ]);
  });

  it("never reads an older Radar's bare array as complete", () => {
    const empty = project([
      tool("empty", "list_resources", [], {
        summary: JSON.stringify({ kind: "services" }),
      }),
    ]);
    expect(empty.limitations[0].message).toContain(
      "Anything you do not have permission to read was not searched.",
    );
  });

  it("rejects a list that does not explain itself in words", () => {
    const projection = project([
      tool("list", "list_resources", {
        coverage: {
          status: "partial",
          scope: { type: "all_namespaces" },
          read: { type: "all_namespaces" },
          gaps: [{ code: "kind_sync_pending" }],
        },
        items: rows,
      }),
    ]);
    expect(groupsOf(projection.groups, "inventory")).toEqual([]);
  });
});
