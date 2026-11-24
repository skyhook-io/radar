// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ArgoSyncDetails } from "./ArgoSyncDetails";
import { RenderedChanges } from "./RenderedChanges";
import type { RenderedFieldChange, RenderedResourceChange } from "../../api/client";

const hooks = vi.hoisted(() => ({
  useArgoStatus: vi.fn(), useArgoSyncDiff: vi.fn(), useGitOpsInsights: vi.fn(),
}));
vi.mock("../../api/client", () => hooks);
const source = { type: "gitops", kind: "Application", namespace: "argo", name: "checkout" } as const;
const git = { repoURL: "https://git.example/repo", historyID: "5", previousRevision: "a".repeat(40) };
const revision = "b".repeat(40);
const template = ["spec", "template"];
const annotation = (key: string, prefix: string[] = []): RenderedFieldChange => ({
  path: [...prefix, "metadata", "annotations", key], before: "old", after: "new",
});
const resource = (fields: RenderedFieldChange[]): RenderedResourceChange => ({
  group: "apps", kind: "Deployment", namespace: "prod", name: "checkout", operation: "modified", fields,
});

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  root = createRoot(container);
  hooks.useArgoStatus.mockReturnValue({ data: { connected: true } });
  hooks.useGitOpsInsights.mockReturnValue({});
});
afterEach(async () => {
  await act(async () => root.unmount());
  vi.resetAllMocks();
  vi.unstubAllGlobals();
});
async function render(node: ReactNode) { await act(async () => root.render(node)); }
async function open(disclosure: HTMLDetailsElement) {
  await act(async () => { disclosure.open = true; disclosure.dispatchEvent(new Event("toggle")); });
}

describe("rendered resource changes", () => {
  it("shows the requested image and functional changes ahead of exact, inspectable deployment annotations", async () => {
    const image = { path: [...template, "spec", "containers", "[name=checkout]", "image"], before: "checkout:02", after: "checkout:01" };
    const changes = [resource([
      annotation("deployment-id"), annotation("deployment-timestamp", template),
      { ...annotation("build.run.url"), after: "https://ci.example/build/4" },
      annotation("checksum/config", template), image, { path: ["spec", "replicas"], before: 2, after: 3 },
    ])];
    await render(<RenderedChanges diff={{ changes }} />);
    expect(container.textContent).toContain("Container image");
    expect(container.textContent).toContain("checkout:02");
    expect(container.textContent).toContain("checkout:01");
    expect(container.textContent).toContain("checksum/config");
    expect(container.textContent).toContain("spec.replicas");
    expect(container.textContent).toContain("Deployment metadata (3 changes)");
    expect(container.textContent).not.toContain("deployment-timestamp");
    expect(container.querySelector("a")?.href).toBe("https://ci.example/build/4");
    await open(container.querySelector("details")!);
    expect(container.textContent).toContain("deployment-id");
    expect(container.textContent).toContain("deployment-timestamp");
  });

  it("groups only repeated version label transitions and preserves all their resource paths", async () => {
    const version = (prefix: string[] = [], before = "v1"): RenderedFieldChange => ({
      path: [...prefix, "metadata", "labels", "app.kubernetes.io/version"], before, after: "v2",
    });
    const changes = [
      resource([
        version(), version(template), { path: ["spec", "replicas"], before: 4, after: 5 },
        { path: ["spec", "selector", "matchLabels", "app.kubernetes.io/version"], before: "v1", after: "v2" },
      ]),
      { ...resource([version()]), kind: "Service", group: "" },
      { ...resource([version([], "v0")]), kind: "ConfigMap", group: "" },
    ];
    await render(<RenderedChanges diff={{ changes }} />);
    expect(container.textContent).toContain("spec.replicas");
    expect(container.textContent).toContain('spec.selector.matchLabels["app.kubernetes.io/version"]');
    expect(container.textContent).toContain("v0");
    const group = [...container.querySelectorAll("details")].find(d => d.querySelector("summary")?.textContent?.includes("Version label"))!;
    expect(group.querySelector("summary")?.textContent).toBe("Version label v1 → v2 on 2 resources");
    await open(group);
    expect(group.querySelectorAll("li")).toHaveLength(3);
    expect(group.textContent).toContain('spec.template.metadata.labels["app.kubernetes.io/version"]');
    expect(group.textContent).toContain("Service prod/checkout");
  });

  it("names existing containers and distinguishes adding a sidecar from changing an existing image", async () => {
    const path = (name: string) => [...template, "spec", "containers", `[name=${name}]`, "image"];
    await render(<RenderedChanges diff={{ changes: [resource([
      { path: path("checkout"), before: "shop:v1", after: "shop:v2" },
      { path: path("proxy"), after: "envoy:v1" },
      { path: [...template, "spec", "containers", "[order]"], before: ["checkout"], after: ["proxy", "checkout"] },
    ])] }} />);
    const imageRows = container.querySelectorAll("table")[0].querySelectorAll("tbody tr");
    expect(imageRows[0].textContent).toBe("checkoutshop:v1shop:v2");
    expect(imageRows[1].textContent).toBe("proxyImage addedAbsentenvoy:v1");
    expect(container.textContent).toContain("spec.template.spec.containers[order]");
  });

  it("keeps ConfigMap data, unrelated CR fields and annotation-like keys out of deployment metadata", async () => {
    const changes = [
      { ...resource([{ path: ["data", "deployment-id"], after: "set" }, { path: ["data", "config"], before: "3", after: 3 }]), kind: "ConfigMap", group: "" },
      { ...resource([annotation("deployment-id", template)]), kind: "Custom", group: "example.io" },
      resource([{ path: ["metadata/annotations", "deployment-id"], before: null }, annotation("deployment-id-extra")]),
    ];
    await render(<RenderedChanges diff={{ changes }} />);
    expect(container.textContent).not.toContain("Deployment metadata (");
    expect(container.textContent).toContain('data["deployment-id"]');
    expect(container.textContent).toContain('"3"');
    expect(container.textContent).toContain("Absent");
    expect(container.textContent).toContain("null");
  });

  it("groups all three CronJob metadata locations and makes metadata-only and partial results distinct", async () => {
    const resourceChange = { ...resource([
      annotation("deployment-id"), annotation("deployment-timestamp", ["spec", "jobTemplate"]),
      { ...annotation("build.run.url", ["spec", "jobTemplate", "spec", "template"]), after: "javascript:alert(1)" },
    ]), kind: "CronJob", group: "batch" };
    await render(<RenderedChanges diff={{ changes: [resourceChange] }} />);
    expect(container.textContent).toContain("Only deployment metadata changed.");
    expect(container.textContent).toContain("Deployment metadata (3 changes)");
    expect(container.querySelector("a")).toBeNull();
    await render(<RenderedChanges diff={{ changes: [resourceChange], partial: true }} />);
    expect(container.textContent).toContain("Partial comparison");
    expect(container.textContent).not.toContain("Only deployment metadata changed.");
    await render(<RenderedChanges diff={{ changes: [], secretsMasked: true }} />);
    expect(container.textContent).toContain("Secret values are masked");
    expect(container.textContent).toContain("No visible field changes");
    await render(<RenderedChanges diff={{ changes: [resource([annotation("deployment-id", template)])] }} />);
    expect(container.textContent).toContain("Pod template annotations changed and can trigger a rollout.");
    await render(<RenderedChanges diff={{ changes: [resource([annotation("deployment-id")])] }} />);
    expect(container.textContent).not.toContain("can trigger a rollout");
    await render(<RenderedChanges diff={{ changes: [] }} />);
    expect(container.textContent).toContain("No rendered resource changes.");
  });

  it("offers Argo settings when disconnected, keeps stale or denied results hidden and skips same-revision comparisons", async () => {
    hooks.useArgoStatus.mockReturnValue({ data: { connected: false } });
    await render(<ArgoSyncDetails source={source} git={git} revision={revision} />);
    const settings = vi.fn();
    window.addEventListener("radar:open-settings", settings, { once: true });
    container.querySelector("button")!.click();
    expect(settings.mock.calls[0][0].detail.section).toBe("argocd");
    expect(hooks.useArgoSyncDiff).not.toHaveBeenCalled();

    hooks.useArgoStatus.mockReturnValue({ data: { connected: true } });
    hooks.useArgoSyncDiff.mockReturnValue({ data: { changes: [] }, isError: true, error: Error("Access denied.") });
    await render(<ArgoSyncDetails source={source} git={git} revision={revision} />);
    expect(hooks.useArgoSyncDiff).toHaveBeenCalledWith("argo", "checkout", "5", git.previousRevision, revision);
    expect(container.textContent).toContain("Access denied");
    expect(container.textContent).not.toContain("No rendered resource changes");
    hooks.useArgoSyncDiff.mockClear();
    await render(<ArgoSyncDetails source={source} git={{ ...git, previousRevision: revision }} revision={revision} />);
    expect(hooks.useArgoSyncDiff).not.toHaveBeenCalled();
  });
});
