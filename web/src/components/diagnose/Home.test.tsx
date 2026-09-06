import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { RunSummary } from "../../api/diagnose";
import { RecentList } from "./Home";

const NOW = new Date(2026, 8, 2, 14, 30);
const time = (day: number, hour: number) =>
  new Date(2026, 8, day, hour).toISOString();
const visible = (html: string) => html.replace(/<[^>]*>/g, "");

function run(patch: Partial<RunSummary> = {}): RunSummary {
  return {
    id: "run-1",
    kind: "Deployment",
    group: "apps",
    namespace: "shop",
    name: "checkout",
    context: "gke_project-one_us-east1-b_nonprod",
    status: "done",
    createdAt: time(2, 13),
    updatedAt: time(2, 14),
    ...patch,
  };
}

function render(runs: RunSummary[], selectedId?: string): string {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  return renderToStaticMarkup(
    <RecentList
      agentLabel="Codex"
      runs={runs}
      selectedId={selectedId}
      onSelect={() => {}}
    />,
  );
}

afterEach(() => vi.useRealTimers());

describe("RecentList", () => {
  it("keeps the whole row clickable without a portal tooltip and labels individual identity fields", () => {
    const html = render([run()]);
    expect(html).not.toContain("inline-flex max-w-full");
    expect(html).toContain('title="checkout"');
    expect(html).toContain('title="gke_project-one_us-east1-b_nonprod"');
    expect(html).toContain('aria-label="Deployment.apps shop/checkout');
  });
  it("uses cluster identity instead of lifecycle locks and deterministic initial-issue text", () => {
    const current = run({ health: { topReason: "CrashLoopBackOff" } });
    const html = renderToStaticMarkup(
      <RecentList
        agentLabel="Codex"
        runs={[
          current,
          run({ id: "stale", status: "stale", context: "kind-demo" }),
        ]}
        currentContext={current.context}
        onSelect={() => {}}
      />,
    );
    expect(html).toContain('aria-label="Current cluster: nonprod"');
    expect(html).toContain('aria-label="Cluster: kind-demo"');
    expect(html).toContain("Started with CrashLoopBackOff");
    expect(html).not.toContain("lucide-lock");
    expect(html).not.toContain("lucide-check");
  });
  it("uses one stable start time per run, sorted into local date groups without previews", () => {
    const recentTime = time(2, 13);
    const olderTime = time(1, 12);
    const html = render([
      run({ id: "older", name: "older", createdAt: olderTime }),
      run({
        id: "recent",
        name: "recent",
        createdAt: recentTime,
        preview: "MONGO_PASSWORD **broken**",
      }),
    ]);
    expect(html.match(/<time /g)).toHaveLength(2);
    expect(html).toContain(`dateTime="${recentTime}"`);
    expect(html).toContain(`dateTime="${olderTime}"`);
    expect(html).not.toContain(`dateTime="${time(2, 14)}"`);
    expect(html).not.toContain("ago");
    expect(html).not.toContain("MONGO_PASSWORD");
    expect(html).toContain(">Today</h3>");
    expect(html).toContain(">Yesterday</h3>");
    expect(visible(html).indexOf("recent")).toBeLessThan(
      visible(html).indexOf("older"),
    );
  });

  it("states every outcome in text and retains foreign-cluster identity", () => {
    const html = render([
      run({ id: "running", status: "running" }),
      run({ id: "done", status: "done" }),
      run({ id: "failed", status: "error" }),
      run({ id: "stopped", status: "stopped" }),
      run({
        id: "foreign",
        status: "stale",
        context: "gke_prod-us-east1",
      }),
    ]);

    expect(html).toContain("Running");
    expect(html).toContain("Failed");
    expect(html).toContain("Stopped");
    expect(html).not.toContain("Different cluster");
    expect(html).toContain("Read-only investigation");
    expect(html).toContain("Investigation failed");
    expect(html).toContain("text-semantic-error");
    expect(html).toContain("Completed");
    expect(visible(html)).not.toContain("Completed");
    expect(html).not.toContain("text-amber");
    expect(html).toContain("gke_prod-us-east1");
  });

  it("marks the selected history row without adding another visual label", () => {
    const html = render([run({ id: "selected" })], "selected");

    expect(html).toContain('aria-current="true"');
    expect(html).toContain("border-accent bg-accent-muted");
  });

  it("leads with resource name and keeps full identity accessible", () => {
    const html = render([run()]);
    const text = visible(html);
    expect(text).toContain("shop · Deployment");
    expect(text).toContain("nonprod");
    expect(text).not.toContain("Deployment.apps");
    expect(text).not.toContain("gke_");
    expect(text.indexOf("checkout")).toBeLessThan(
      text.indexOf("shop · Deployment"),
    );
    expect(html).toContain("Deployment.apps shop/checkout");
  });

  it("does not change displayed time when cluster-switch bookkeeping updates runs", () => {
    const before = visible(render([run()]));
    const after = visible(
      render([run({ status: "stale", updatedAt: time(2, 16) })]),
    );
    expect(after).toBe(before);
  });

  it("disambiguates matching cluster names by project or region", () => {
    const text = visible(
      render([
        run(),
        run({ id: "two", context: "gke_project-two_us-east1-b_nonprod" }),
      ]),
    );
    expect(text).toContain("project-one");
    expect(text).toContain("project-two");
    expect(text).not.toContain("gke_");
    const regions = visible(
      render([
        run(),
        run({ id: "two", context: "gke_project-one_us-west1-b_nonprod" }),
      ]),
    );
    expect(regions).toContain("project-one · us-east1-b");
    expect(regions).toContain("project-one · us-west1-b");
  });

  it("retains raw identity when parsed qualifiers still collide", () => {
    const text = visible(
      render([
        run({ context: "clusterUser_rg_nonprod" }),
        run({ id: "admin", context: "clusterAdmin_rg_nonprod" }),
      ]),
    );
    expect(text).toContain("clusterUser_rg_nonprod");
    expect(text).toContain("clusterAdmin_rg_nonprod");
  });

  it("qualifies ambiguous kinds and handles cluster-scoped resources", () => {
    const text = visible(
      render([
        run({ kind: "Service", group: "" }),
        run({ id: "knative", kind: "Service", group: "serving.knative.dev" }),
        run({
          id: "node",
          kind: "Node",
          group: "",
          namespace: "",
          name: "worker",
        }),
      ]),
    );
    expect(text).toContain("Service · core");
    expect(text).toContain("Service · serving.knative.dev");
    expect(text).toContain("worker");
    expect(text).toContain("Node");
    expect(text).not.toContain("shop · Node");
  });

  it("retains full long names and custom contexts", () => {
    const name =
      "a-very-long-workload-name-that-needs-two-lines-to-be-recognizable";
    const html = render([run({ name, context: "my-custom-context" })]);
    expect(html).toContain(name);
    expect(html).toContain("line-clamp-2 break-words");
    expect(visible(html)).toContain("my-custom-context");
  });

  it("uses Radar's readable kind labels and omits built-in group display noise", () => {
    const text = visible(
      render([run(), run({ id: "plural", kind: "deployments", group: "" })]),
    );
    expect(text.match(/shop · Deployment/g)).toHaveLength(2);
    expect(text).not.toContain("Deployment · apps");
    expect(text).not.toContain("Deployment · core");
  });

  it("does not duplicate an unparsed cluster name when it collides with a parsed name", () => {
    const html = render([run(), run({ id: "alias", context: "nonprod" })]);
    const aliasButton = html.match(
      /<button[^>]*aria-label="[^"]* · nonprod ·[^>]*>[\s\S]*?<\/button>/,
    )?.[0];
    expect(aliasButton).toBeDefined();
    expect(visible(aliasButton!).match(/nonprod/g)).toHaveLength(1);
  });

  it("distinguishes prior years and handles yesterday across month boundaries", () => {
    expect(
      visible(
        render([run({ createdAt: new Date(2025, 8, 2, 9).toISOString() })]),
      ),
    ).toContain("2025");
    vi.setSystemTime(new Date(2026, 8, 1, 0, 30));
    const html = renderToStaticMarkup(
      <RecentList
        agentLabel="Codex"
        runs={[run({ createdAt: new Date(2026, 7, 31, 23).toISOString() })]}
        onSelect={() => {}}
      />,
    );
    expect(html).toContain(">Yesterday</h3>");
  });

  it("retains empty-state guidance and persistence warnings", () => {
    expect(visible(render([]))).toContain("No investigations yet");
    const html = renderToStaticMarkup(
      <RecentList
        agentLabel="Codex"
        runs={[]}
        onSelect={() => {}}
        historyDegraded
      />,
    );
    expect(visible(html)).toContain("disk error");
  });
});
