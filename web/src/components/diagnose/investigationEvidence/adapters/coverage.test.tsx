import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { groupsOf, project, tool } from "../evidenceFixtures";
import { RankingBody } from "../bodies/ranking";
import { PostureBody } from "../bodies/posture";
import { InventoryBody } from "../bodies/inventory";

const targetBundle = {
  resource: {
    apiVersion: "apps/v1",
    kind: "Deployment",
    metadata: { namespace: "shop", name: "api" },
    spec: { replicas: 1 },
    status: { readyReplicas: 1 },
  },
  pods: 1,
  podNames: ["api-7f6-abc"],
  events: [],
};

describe("ranking card (top_resources)", () => {
  it("marks the target's own pod and renders the rows", () => {
    const projection = project([
      tool("diag", "diagnose", targetBundle),
      tool(
        "top",
        "top_resources",
        {
          kind: "pods",
          sort: "memory",
          metricsAvailable: true,
          items: [
            {
              kind: "Pod",
              namespace: "shop",
              name: "worker-1",
              cpuMilli: 120,
              memoryMi: 900,
              restarts: 0,
            },
            {
              kind: "Pod",
              namespace: "shop",
              name: "api-7f6-abc",
              cpuMilli: 40,
              memoryMi: 512,
              memoryLimitMi: 600,
              restarts: 3,
            },
          ],
        },
        {
          summary: JSON.stringify({
            kind: "pods",
            namespace: "shop",
            sort: "memory",
          }),
        },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "ranking");
    expect(group.latest.title).toBe("Top pods by memory");
    expect(group.latest.relevance).toBe("producer-related");
    const data = group.latest.data;
    if (data.type !== "ranking") throw new Error("expected a ranking");
    expect(data.rows.map((row) => [row.name, row.target])).toEqual([
      ["worker-1", false],
      ["api-7f6-abc", true],
    ]);
    const html = renderToStaticMarkup(<RankingBody data={data} />);
    expect(html).toContain('data-ranking-row="target"');
    expect(html).toContain("512Mi");
    expect(html).toContain("/600Mi");
    expect(html).toContain("3 restarts");
  });

  it("records a limit, not a card, when live metrics were unavailable", () => {
    const projection = project([
      tool("top", "top_resources", {
        kind: "pods",
        sort: "cpu",
        metricsAvailable: false,
        reason: "metrics.k8s.io is not registered",
      }),
    ]);
    expect(groupsOf(projection.groups, "ranking")).toHaveLength(0);
    expect(projection.limitations).toContainEqual(
      expect.objectContaining({ source: "Live metrics" }),
    );
  });
});

describe("posture cards (audit, upgrade readiness)", () => {
  it("keeps audit findings about the target, and marks them", () => {
    const projection = project([
      tool("diag", "diagnose", targetBundle),
      tool(
        "audit",
        "get_cluster_audit",
        {
          summary: {
            critical: 0,
            high: 1,
            medium: 2,
            low: 0,
            resources: 3,
            categories: {},
          },
          findings: [
            {
              resource: "Deployment/shop/api",
              check: "runAsRoot",
              severity: "high",
              category: "Security",
              message: "Container runs as root.",
              remediation: "Set runAsNonRoot: true.",
            },
            {
              resource: "Deployment/other/web",
              check: "noLimits",
              severity: "medium",
              category: "Efficiency",
              message: "No limits.",
            },
          ],
          totalCount: 2,
        },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "posture");
    expect(group.latest.relevance).toBe("target");
    const data = group.latest.data;
    if (data.type !== "posture") throw new Error("expected posture");
    expect(
      data.findings.map((finding) => [finding.name, finding.target]),
    ).toEqual([["api", true]]);
    const html = renderToStaticMarkup(<PostureBody data={data} />);
    expect(html).toContain('data-posture-finding="target"');
    expect(html).toContain("runAsRoot");
    expect(html).toContain("Set runAsNonRoot: true.");
  });

  it("files a scan with nothing about the target as a receipt", () => {
    const projection = project([
      tool("diag", "diagnose", targetBundle),
      tool(
        "audit",
        "get_cluster_audit",
        {
          summary: {
            critical: 0,
            high: 0,
            medium: 1,
            low: 0,
            resources: 1,
            categories: {},
          },
          findings: [
            {
              resource: "Deployment/other/web",
              check: "noLimits",
              severity: "medium",
              message: "No limits.",
            },
          ],
          totalCount: 1,
        },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);
    expect(groupsOf(projection.groups, "posture")).toHaveLength(0);
    const receipt = groupsOf(projection.groups, "receipt").find((group) =>
      group.latest.title.startsWith("No posture"),
    );
    expect(receipt?.latest.title).toBe("No posture findings on this resource");
    expect(
      receipt?.latest.data.type === "receipt" && receipt.latest.data.message,
    ).toBe("1 finding elsewhere in the scan.");
  });

  it("reads an upgrade check's findings by the resource they name", () => {
    const projection = project([
      tool("diag", "diagnose", targetBundle),
      tool(
        "upgrade",
        "get_cluster_upgrade_readiness",
        {
          currentVersion: "1.36.1",
          targetVersion: "1.37",
          check: {
            id: "removed-apis",
            title: "Removed APIs",
            category: "api",
            status: "fail",
            findings: [
              {
                title: "Uses autoscaling/v2beta2",
                level: "blocker",
                resource: {
                  kind: "HorizontalPodAutoscaler",
                  namespace: "shop",
                  name: "api",
                },
                managedBy: {
                  kind: "Deployment",
                  namespace: "shop",
                  name: "api",
                },
                evidence: {
                  source: "live",
                  path: "apiVersion",
                  detail: "autoscaling/v2beta2",
                },
                impact: "The object stops being served after the upgrade.",
                remediation: "Migrate to autoscaling/v2.",
              },
            ],
          },
        },
        { summary: JSON.stringify({ target: "1.37", check: "removed-apis" }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "posture");
    expect(group.latest.title).toBe("Upgrade findings for 1.37");
    const data = group.latest.data;
    if (data.type !== "posture") throw new Error("expected posture");
    expect(data.findings[0].severity).toBe("blocker");
    expect(data.findings[0].message).toContain("stops being served");
  });
});

describe("listing cards (helm releases, packages, search)", () => {
  it("lists Helm releases with their status and chart", () => {
    const projection = project([
      tool(
        "helm",
        "list_helm_releases",
        [
          {
            name: "podinfo",
            namespace: "shop",
            chart: "podinfo",
            chartVersion: "6.15.0",
            status: "deployed",
            revision: 3,
            updated: "2026-09-07T07:00:00Z",
          },
          {
            name: "redis",
            namespace: "shop",
            chart: "redis",
            chartVersion: "19.0.1",
            status: "failed",
            revision: 2,
            updated: "2026-09-07T07:00:00Z",
            healthIssue: "0/1 pods ready",
          },
        ],
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "inventory");
    expect(group.latest.title).toBe("Helm releases in shop");
    const data = group.latest.data;
    if (data.type !== "inventory") throw new Error("expected inventory");
    expect(data.resources.map((row) => row.status)).toEqual([
      "deployed · podinfo 6.15.0",
      "failed · redis 19.0.1",
    ]);
    expect(data.resources[1].issue).toBe("0/1 pods ready");
  });

  it("lists packages with their version and sources", () => {
    const projection = project([
      tool(
        "pkgs",
        "list_packages",
        {
          packages: [
            {
              chart: "podinfo",
              namespace: "shop",
              releaseName: "podinfo",
              version: "6.15.0",
              health: { status: "healthy" },
              sources: ["H", "F"],
            },
          ],
          sourceLegend: { H: "Helm", F: "Flux" },
        },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "inventory");
    expect(group.latest.title).toBe("Packages in shop");
    const data = group.latest.data;
    if (data.type !== "inventory") throw new Error("expected inventory");
    expect(data.resources[0]).toMatchObject({
      kind: "Package",
      name: "podinfo",
      status: "6.15.0 · Helm, Flux",
    });
  });

  it("lists search hits with the field that matched, and renders it", () => {
    const projection = project([
      tool(
        "search",
        "search",
        {
          hits: [
            {
              kind: "ConfigMap",
              namespace: "shop",
              name: "nginx-config",
              matched: [
                {
                  token: "missing.conf",
                  site: "content:data.nginx.conf",
                  score: 3,
                },
              ],
              snippets: [
                {
                  token: "missing.conf",
                  path: "data.nginx.conf",
                  snippet: "include /etc/nginx/missing.conf;",
                },
              ],
            },
          ],
          total: 1,
          searched: 120,
        },
        { summary: JSON.stringify({ query: "missing.conf" }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "inventory");
    expect(group.latest.title).toBe("Search results for “missing.conf”");
    const data = group.latest.data;
    if (data.type !== "inventory") throw new Error("expected inventory");
    expect(data.resources[0].match).toBe(
      "data.nginx.conf: include /etc/nginx/missing.conf;",
    );
    const html = renderToStaticMarkup(<InventoryBody data={data} />);
    expect(html).toContain("include /etc/nginx/missing.conf;");
  });
});

describe("metric discovery receipt", () => {
  it("files what was searched and how much came back", () => {
    const projection = project([
      tool(
        "disc",
        "discover_metrics",
        {
          match: '{namespace="shop"}',
          count: 42,
          metrics: [],
          truncated: false,
        },
        { summary: JSON.stringify({ match: '{namespace="shop"}' }) },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "receipt");
    expect(group.latest.title).toBe("Metric names");
    expect(group.latest.summary).toBe("42 metrics · last hour");
  });
});
