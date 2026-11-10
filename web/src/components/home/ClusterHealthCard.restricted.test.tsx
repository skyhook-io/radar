// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { renderToString } from "react-dom/server";
import type { DashboardResourceCounts } from "../../api/client";
import { ClusterHealthCard } from "./ClusterHealthCard";

// The MCP block needs the Diagnose provider and is unrelated to the counts.
vi.mock("./HomeAgentBlock", () => ({ HomeAgentBlock: () => null }));

function counts(overrides: Partial<DashboardResourceCounts>): DashboardResourceCounts {
  return {
    pods: { total: 0, running: 0, pending: 0, failed: 0, succeeded: 0 },
    deployments: { total: 0, available: 0, unavailable: 0 },
    statefulSets: { total: 0, ready: 0, unready: 0 },
    daemonSets: { total: 0, ready: 0, unready: 0 },
    services: 0,
    ingresses: 0,
    nodes: { total: 0, ready: 0, notReady: 0, cordoned: 0 },
    namespaces: 0,
    jobs: { total: 0, active: 0, succeeded: 0, failed: 0 },
    cronJobs: { total: 0, active: 0, suspended: 0 },
    configMaps: 0,
    secrets: 0,
    pvcs: { total: 0, bound: 0, pending: 0, unbound: 0 },
    ...overrides,
  };
}

function render(c: DashboardResourceCounts): string {
  return renderToString(
    <ClusterHealthCard
      health={{ healthy: 0, warning: 0, error: 0, warningEvents: 0 }}
      counts={c}
      cluster={{ name: "test", platform: "", version: "", connected: true }}
      metrics={null}
      metricsServerAvailable={false}
      issueCount={0}
      hasCriticalIssues={false}
      nodeVersionSkew={null}
      onNavigateToKind={() => {}}
      onNavigateToView={() => {}}
    />,
  );
}

describe("ClusterHealthCard restricted counts", () => {
  it("shows the restricted Nodes ring instead of a zero ring", () => {
    const html = render(counts({ restricted: ["nodes"] }));
    expect(html).toMatch(/Nodes<\/span><span[^>]*>Restricted/);
    expect(html).not.toContain("0 ready");
  });

  it("blames Node access, not metrics-server, for missing CPU and memory", () => {
    expect(render(counts({ restricted: ["nodes"] }))).toContain("CPU and memory need permission to list Nodes");
    expect(render(counts({ restricted: ["nodes"] }))).not.toContain("metrics-server");
    expect(render(counts({}))).toContain("metrics-server");
  });

  it("marks a namespace count Radar could not list as a lower bound", () => {
    const html = render(counts({ namespaces: 15, restricted: ["namespaces"] }));
    expect(html).toContain("≥");
    expect(html).toMatch(/>15<\/span> (<!-- -->)?namespaces/);
  });

  it("says restricted rather than zero when no namespace is known", () => {
    const html = render(counts({ restricted: ["namespaces"] }));
    expect(html).toContain("Namespaces restricted");
    expect(html).not.toMatch(/>0<\/span> (<!-- -->)?namespaces/);
  });

  it("keeps an exact namespace count unmarked", () => {
    const html = render(counts({ namespaces: 111 }));
    expect(html).toMatch(/>111<\/span> namespaces/);
    expect(html).not.toContain("≥");
  });
});
