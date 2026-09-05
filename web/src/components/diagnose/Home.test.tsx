import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { RunSummary } from "../../api/diagnose";
import { RecentList } from "./Home";

const NOW = new Date("2026-09-02T14:30:00Z");

function run(patch: Partial<RunSummary> = {}): RunSummary {
  return {
    id: "run-1",
    kind: "Deployment",
    group: "apps",
    namespace: "shop",
    name: "checkout",
    context: "nonprod",
    status: "done",
    createdAt: "2026-09-02T13:00:00Z",
    updatedAt: "2026-09-02T14:00:00Z",
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
  it("distinguishes repeated completed runs by age, exact time, and a readable finding preview", () => {
    const recentTime = "2026-09-02T14:00:00Z";
    const olderTime = "2026-09-01T14:00:00Z";
    const recentPreview =
      "The MONGO_PASSWORD secret is stale, so every newly started API pod fails authentication before it can become ready.";
    const olderPreview =
      "The workload references a deleted image tag, which leaves the replacement pod in ImagePullBackOff.";
    const html = render([
      run({ id: "recent", updatedAt: recentTime, preview: recentPreview }),
      run({ id: "older", updatedAt: olderTime, preview: olderPreview }),
    ]);

    expect(html.match(/Deployment\.apps shop\/checkout/g)).toHaveLength(2);
    expect(html.match(/Completed/g)).toHaveLength(2);
    expect(html).toContain("30m ago");
    expect(html).toContain("1d ago");
    expect(
      html.match(new RegExp(`dateTime="${recentTime}"`, "g")),
    ).toHaveLength(2);
    expect(html.match(new RegExp(`dateTime="${olderTime}"`, "g"))).toHaveLength(
      2,
    );
    expect(html).toContain(recentPreview);
    expect(html).toContain(olderPreview);
    expect(html.match(/line-clamp-2/g)).toHaveLength(2);
    expect(html).not.toContain('class="truncate pl-3.5 text-xs');
  });

  it("states every outcome in text and retains foreign-cluster identity", () => {
    const html = render([
      run({ id: "running", status: "running" }),
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
    expect(html).toContain("Different cluster");
    expect(html).toContain("gke_prod-us-east1");
  });

  it("marks the selected history row without adding another visual label", () => {
    const html = render([run({ id: "selected" })], "selected");

    expect(html).toContain('aria-current="true"');
    expect(html).toContain("border-accent/50");
  });
});
