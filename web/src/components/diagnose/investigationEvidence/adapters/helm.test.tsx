import { describe, expect, it } from "vitest";
import { investigationEvidenceSubjectRef } from "../index";
import { groupsOf, project, tool } from "../evidenceFixtures";
import { renderToStaticMarkup } from "react-dom/server";
import { HelmBody } from "../bodies/platform";

const helmRelease = {
  name: "shop",
  namespace: "shop",
  chart: "shop",
  chartVersion: "1.4.2",
  appVersion: "2.0.0",
  status: "deployed",
  revision: 7,
  updated: "2026-09-07T07:00:00Z",
  description: "Upgrade complete",
  resources: [
    {
      kind: "Deployment",
      apiVersion: "apps/v1",
      name: "api",
      namespace: "shop",
      status: "Running",
      ready: "0/1",
    },
    {
      kind: "Service",
      apiVersion: "v1",
      name: "api",
      namespace: "shop",
      status: "Active",
    },
  ],
};

describe("helm release adapter", () => {
  it("relates a release through the resources it manages", () => {
    const projection = project([
      tool("helm", "get_helm_release", helmRelease, {
        summary: JSON.stringify({ namespace: "shop", name: "shop" }),
      }),
      tool(
        "helm-other",
        "get_helm_release",
        {
          ...helmRelease,
          name: "ingress",
          resources: [
            {
              kind: "Deployment",
              apiVersion: "apps/v1",
              name: "api",
              namespace: "ingress",
            },
          ],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "ingress" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(releases.map((group) => group.latest.relevance)).toEqual([
      "producer-related",
      "broader",
    ]);
    expect(releases[0].latest).toMatchObject({
      tier: "context",
      tone: "info",
      title: "Helm release shop/shop",
      summary: "shop 1.4.2 · deployed · revision 7",
    });
    expect(investigationEvidenceSubjectRef(releases[0].latest.data)).toEqual({
      kind: "HelmRelease",
      group: "helm.sh",
      namespace: "shop",
      name: "shop",
    });
  });

  it("never fills a missing owned-resource API version from the target", () => {
    const projection = project([
      tool(
        "helm-no-version",
        "get_helm_release",
        {
          ...helmRelease,
          resources: [{ kind: "Deployment", name: "api", namespace: "shop" }],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-other-group",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-x",
          resources: [
            {
              kind: "Deployment",
              apiVersion: "example.io/v1",
              name: "api",
              namespace: "shop",
            },
          ],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-x" }) },
      ),
    ]);
    expect(
      groupsOf(projection.groups, "helm").map(
        (group) => group.latest.relevance,
      ),
    ).toEqual(["broader", "broader"]);
  });

  it("keys a release by its storage namespace and withholds a link it cannot express", () => {
    const projection = project([
      tool(
        "helm-a",
        "get_helm_release",
        { ...helmRelease, storageNamespace: "flux-a" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-b",
        "get_helm_release",
        { ...helmRelease, storageNamespace: "flux-b", revision: 9 },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(releases.map((group) => group.identity)).toEqual([
      "helm:flux-a:shop:shop",
      "helm:flux-b:shop:shop",
    ]);
    expect(releases[0].latest.data).toMatchObject({
      release: { storageNamespace: "flux-a" },
    });
    expect(investigationEvidenceSubjectRef(releases[0].latest.data)).toBe(
      undefined,
    );
  });

  it("treats a non-deployed status, a health issue, or a failed operation as adverse", () => {
    const projection = project([
      tool(
        "helm-failed",
        "get_helm_release",
        { ...helmRelease, status: "failed", description: "Upgrade failed" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-issue",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-cache",
          resourceHealth: "unhealthy",
          healthIssue: "Deployment shop/cache has 0/1 ready replicas",
          healthSummary: "1 of 2 resources unhealthy",
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-cache" }) },
      ),
      tool(
        "helm-stuck",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-jobs",
          status: "pending-upgrade",
          lastOperation: {
            kind: "pending",
            status: "stuck_pending",
            source: "helm_status",
            confidence: "high",
            message: "Revision 8 has been pending-upgrade for 42m",
            revision: 8,
          },
          valuesError:
            'Radar Cloud role "viewer" cannot view Helm release values (requires member or higher)',
          diffError:
            'Radar Cloud role "viewer" cannot view Helm release diffs (requires member or higher)',
          valuesDiffError: "values diff failed",
          notesDiffError: "notes diff failed",
          resourceDiffError: "resource diff failed",
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-jobs" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(
      releases.map((group) => [group.latest.tier, group.latest.tone]),
    ).toEqual([
      ["supporting", "error"],
      ["supporting", "warning"],
      ["supporting", "warning"],
    ]);
    expect(releases[1].latest.summary).toBe(
      "deployed · Deployment shop/cache has 0/1 ready replicas",
    );
    expect(releases[2].latest.data).toMatchObject({
      type: "helm",
      release: {
        lastOperation: { kind: "pending", status: "stuck_pending" },
      },
    });
    // Every comparison the producer could not finish has to reach the reader:
    // three of these are role denials, and a release card that omits a withheld
    // diff without saying so reads as a complete one.
    expect(projection.limitations).toEqual([
      expect.objectContaining({ source: "Helm values", kind: "error" }),
      expect.objectContaining({ source: "Helm values diff", kind: "error" }),
      expect.objectContaining({ source: "Helm manifest diff", kind: "error" }),
      expect.objectContaining({ source: "Helm notes diff", kind: "error" }),
      expect.objectContaining({ source: "Helm resource diff", kind: "error" }),
    ]);
  });

  it("rejects malformed release payloads", () => {
    const projection = project([
      tool(
        "helm-bad",
        "get_helm_release",
        { ...helmRelease, revision: "7" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-bad-resource",
        "get_helm_release",
        { ...helmRelease, resources: [{ kind: "Deployment" }] },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-bad-operation",
        "get_helm_release",
        { ...helmRelease, lastOperation: { kind: "rollback" } },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => limitation.source),
    ).toEqual(["Helm release", "Helm resources", "Helm operation"]);
  });
});

describe("helm release card detail", () => {
  it("keeps hooks, the newest ten revisions and the user's values, and renders them", () => {
    const projection = project([
      tool("helm", "get_helm_release", {
        ...helmRelease,
        history: [
          {
            revision: 5,
            status: "superseded",
            chart: "shop-1.4.0",
            updated: "2026-09-01T07:00:00Z",
          },
          {
            revision: 7,
            status: "deployed",
            chart: "shop-1.4.2",
            updated: "2026-09-07T07:00:00Z",
            description: "Upgrade complete",
          },
          {
            revision: 6,
            status: "failed",
            chart: "shop-1.4.1",
            updated: "2026-09-03T07:00:00Z",
            description: "pre-upgrade hooks failed",
          },
        ],
        hooks: [
          {
            name: "shop-test-connection",
            kind: "Pod",
            events: ["test"],
            weight: 0,
            status: "Succeeded",
            completedAt: "2026-09-07T07:01:00Z",
          },
        ],
        values: {
          replicaCount: 2,
          ui: { color: "#34577c", message: "hi" },
          tags: ["a", "b"],
        },
      }),
    ]);
    const [group] = groupsOf(projection.groups, "helm");
    const data = group.latest.data;
    if (data.type !== "helm") throw new Error("expected a helm card");
    expect(data.release.history?.map((rev) => rev.revision)).toEqual([7, 6, 5]);
    expect(data.release.hooks?.[0].name).toBe("shop-test-connection");
    const html = renderToStaticMarkup(<HelmBody data={data} />);
    expect(html).toContain('data-helm-revision="6"');
    expect(html).toContain("pre-upgrade hooks failed");
    expect(html).toContain("data-helm-hook");
    expect(html).toContain("shop-test-connection");
    expect(html).toContain("Values set by the user");
    expect(html).toContain("replicaCount");
    expect(html).toContain("{ color, message }");
    expect(html).toContain("[2 items]");
  });
});
