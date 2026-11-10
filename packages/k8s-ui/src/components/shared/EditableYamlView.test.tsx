// @vitest-environment jsdom
//
// Applying a reviewed edit can be refused because the resource changed after
// the review. The review then refreshes against the latest version and says
// so, once. That notice belongs to one attempt: a retry that fails for another
// reason shows its own error.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { resourceToYaml } from "../../utils/yaml";

vi.mock("../ui/YamlEditor", async () => {
  const { useEffect } = await import("react");
  return {
    YamlEditor: () => <div />,
    YamlDiffEditor: ({
      onReadyChange,
    }: {
      onReadyChange?: (ready: boolean) => void;
    }) => {
      useEffect(() => onReadyChange?.(true), [onReadyChange]);
      return <div />;
    },
  };
});

const { EditableYamlView } = await import("./EditableYamlView");

vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
vi.stubGlobal(
  "ResizeObserver",
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
);
let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const resource = { kind: "ConfigMap", namespace: "shop", name: "app" };
const data = {
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "app", namespace: "shop" },
  data: { a: "1" },
};
const reviewed = (rv: string) => ({
  documents: [
    {
      index: 0,
      kind: "ConfigMap",
      name: "app",
      namespace: "shop",
      status: "accepted" as const,
      action: "update" as const,
      reviewedResourceVersion: rv,
    },
  ],
  nonAtomic: false,
});

function render(props: {
  onSave: () => Promise<void>;
  onPreview: () => Promise<ReturnType<typeof reviewed>>;
  saveError?: string | null;
}) {
  act(() =>
    root.render(
      <EditableYamlView
        resource={resource as never}
        data={data}
        onCopy={() => {}}
        copied={false}
        onSave={props.onSave}
        onPreview={props.onPreview}
        saveError={props.saveError ?? null}
      />,
    ),
  );
}

const click = async (label: RegExp) => {
  const b = [...document.querySelectorAll("button")].find((x) =>
    label.test(x.textContent ?? ""),
  );
  if (!b) throw new Error(`no button ${label} in ${document.body.textContent}`);
  await act(async () => {
    b.click();
    await new Promise((r) => setTimeout(r, 0));
  });
};
const text = () => document.body.textContent ?? "";

it("says the resource changed after review, and only for the attempt that found it", async () => {
  const onPreview = vi
    .fn()
    .mockResolvedValueOnce(reviewed("1")) // the review
    .mockResolvedValueOnce(reviewed("2")) // refresh after the conflict: a newer version
    .mockRejectedValueOnce(new Error("preview unavailable")); // refresh after the second failure
  const onSave = vi
    .fn()
    .mockRejectedValueOnce(new Error("conflict"))
    .mockRejectedValueOnce(new Error("forbidden"));
  render({ onSave, onPreview });

  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  render({
    onSave,
    onPreview,
    saveError:
      "resource changed after review; review the latest version before applying",
  });
  expect(text()).toContain("This resource changed after your review");
  expect(text()).not.toContain("review the latest version before applying");

  await click(/Apply reviewed changes/);
  render({
    onSave,
    onPreview,
    saveError: "forbidden: cannot patch configmaps",
  });
  expect(text()).not.toContain("This resource changed after your review");
  expect(text()).toContain("forbidden: cannot patch configmaps");
  expect(onSave).toHaveBeenCalledTimes(2);
  expect(onPreview).toHaveBeenCalledTimes(3);
});

it("says nothing extra when the refreshed review found the same version", async () => {
  const onPreview = vi.fn().mockResolvedValue(reviewed("1"));
  const onSave = vi.fn().mockRejectedValueOnce(new Error("webhook denied"));
  render({ onSave, onPreview });
  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  render({
    onSave,
    onPreview,
    saveError: "admission webhook denied the request",
  });
  expect(text()).not.toContain("This resource changed after your review");
  expect(text()).toContain("admission webhook denied the request");
  expect(onPreview).toHaveBeenCalledTimes(2);
});

it("doesn't call a deleted resource changed", async () => {
  const gone = {
    documents: [
      {
        index: 0,
        kind: "ConfigMap",
        name: "app",
        namespace: "shop",
        status: "rejected" as const,
        message: 'configmaps "app" not found',
      },
    ],
    nonAtomic: false,
  };
  const onPreview = vi
    .fn()
    .mockResolvedValueOnce(reviewed("1"))
    .mockResolvedValueOnce(gone);
  const onSave = vi.fn().mockRejectedValueOnce(new Error("not found"));
  render({ onSave, onPreview });
  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  render({ onSave, onPreview, saveError: 'configmaps "app" not found' });
  expect(text()).not.toContain("This resource changed after your review");
  expect(text()).toContain('configmaps "app" not found');
});

it("doesn't call a switched cluster a changed resource", async () => {
  const onPreview = vi
    .fn()
    .mockResolvedValueOnce({ ...reviewed("1"), context: "kind-a" })
    .mockResolvedValueOnce({ ...reviewed("2"), context: "kind-b" });
  const onSave = vi.fn().mockRejectedValueOnce(new Error("cluster changed"));
  render({ onSave, onPreview });
  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  render({
    onSave,
    onPreview,
    saveError: "the active cluster changed after review",
  });
  expect(text()).not.toContain("This resource changed after your review");
  expect(text()).toContain("the active cluster changed after review");
});

it("ignores an earlier attempt's refresh that lands after a newer attempt", async () => {
  let finishFirstRefresh: (v: ReturnType<typeof reviewed>) => void = () => {};
  const onPreview = vi
    .fn()
    .mockResolvedValueOnce(reviewed("1"))
    .mockReturnValueOnce(
      new Promise<ReturnType<typeof reviewed>>((r) => {
        finishFirstRefresh = r;
      }),
    )
    .mockRejectedValueOnce(new Error("preview unavailable"));
  const onSave = vi
    .fn()
    .mockRejectedValueOnce(new Error("conflict"))
    .mockRejectedValueOnce(new Error("forbidden"));
  render({ onSave, onPreview });
  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  await click(/Apply reviewed changes/);
  await act(async () => {
    finishFirstRefresh(reviewed("2"));
    await new Promise((r) => setTimeout(r, 0));
  });
  render({
    onSave,
    onPreview,
    saveError: "forbidden: cannot patch configmaps",
  });
  expect(text()).not.toContain("This resource changed after your review");
  expect(text()).toContain("forbidden: cannot patch configmaps");
});

it("drops a refresh from an apply made before going back to a newer review", async () => {
  let finishFirstRefresh: (v: ReturnType<typeof reviewed>) => void = () => {};
  const onPreview = vi
    .fn()
    .mockResolvedValueOnce(reviewed("1"))
    .mockReturnValueOnce(
      new Promise<ReturnType<typeof reviewed>>((r) => {
        finishFirstRefresh = r;
      }),
    )
    .mockResolvedValueOnce(reviewed("3"));
  const onSave = vi
    .fn()
    .mockRejectedValueOnce(new Error("conflict"))
    .mockResolvedValueOnce(undefined);
  render({ onSave, onPreview });
  await click(/^Edit$/);
  await click(/Review changes/);
  await click(/Apply reviewed changes/);
  await click(/Back to edit/);
  await click(/Review changes/);
  await act(async () => {
    finishFirstRefresh(reviewed("2"));
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(text()).not.toContain("This resource changed after your review");
  await click(/Apply reviewed changes/);
  expect(onSave).toHaveBeenLastCalledWith(
    expect.objectContaining({ reviewedResourceVersion: "3" }),
  );
});

it("uses the accent token for a generic ConfigMap Edit control", () => {
  render({ onSave: async () => {}, onPreview: async () => reviewed("1") });
  const edit = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Edit")!;
  expect(edit.classList.contains("text-accent-text")).toBe(true);
  expect(edit.className).not.toContain("text-blue-");
});


it("keeps the SSA omission caveat in a direct editor save with a known paused baseline", () => {
  const target = { kind: "TaskBucket", namespace: "shop", name: "archive" };
  const baseline = { apiVersion: "example.test/v1", kind: "TaskBucket", metadata: { name: "archive", namespace: "shop", annotations: { "crossplane.io/paused": "true" } }, spec: { providerConfigRef: { name: "default" } } };
  sessionStorage.setItem("radar_yaml_draft:TaskBucket/shop/archive", "apiVersion: example.test/v1\nkind: TaskBucket\nmetadata:\n  name: archive\n  namespace: shop\nspec:\n  providerConfigRef:\n    name: default\n");
  act(() => root.render(<EditableYamlView resource={target as never} data={baseline} onCopy={() => {}} copied={false} onSave={async () => {}} />));
  expect(text()).toContain("Pause affects reconciliation");
  expect(text()).toContain("Omitting the annotation from server-side apply YAML does not guarantee its removal");
});

it("does not carry a paused baseline to a different API group in a direct editor save", async () => {
  const target = { kind: "TaskBucket", namespace: "shop", name: "archive" };
  const baseline = { apiVersion: "example.test/v1", kind: "TaskBucket", metadata: { name: "archive", namespace: "shop", annotations: { "crossplane.io/paused": "true" } }, spec: { providerConfigRef: { name: "default" } } };
  const yaml = "apiVersion: another.example.test/v1\nkind: TaskBucket\nmetadata:\n  name: archive\n  namespace: shop\nspec: {}\n";
  const onSave = vi.fn(async () => {});
  sessionStorage.setItem("radar_yaml_draft:TaskBucket/shop/archive", yaml);
  act(() => root.render(<EditableYamlView resource={target as never} data={baseline} onCopy={() => {}} copied={false} onSave={onSave} />));
  expect(text()).not.toContain("Pause affects reconciliation");
  expect(text()).not.toContain("Existing pause state is unknown");
  await click(/^Save$/);
  expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ kind: "TaskBucket", namespace: "shop", name: "archive", yaml }));
});

it("does not infer annotation omission before the first direct-edit debounce settles", () => {
  vi.useFakeTimers();
  try {
    const target = { kind: "TaskBucket", namespace: "shop", name: "archive" };
    const baseline = { apiVersion: "example.test/v1", kind: "TaskBucket", metadata: { name: "archive", namespace: "shop", annotations: { "crossplane.io/paused": "true" } }, spec: { providerConfigRef: { name: "default" } } };
    act(() => root.render(<EditableYamlView resource={target as never} data={baseline} onCopy={() => {}} copied={false} onSave={async () => {}} />));
    const edit = [...host.querySelectorAll("button")].find(button => button.textContent?.trim() === "Edit")!;
    act(() => edit.click());
    expect(text()).not.toContain("Omitting the annotation");
    act(() => vi.advanceTimersByTime(200));
    expect(text()).toContain("Pause affects reconciliation");
    expect(text()).not.toContain("Omitting the annotation");
  } finally {
    vi.useRealTimers();
  }
});


it("retains raw acknowledged ref-less pause evidence through editor and status-free review", async () => {
  const target = { kind: "TaskClaim", namespace: "shop", name: "ref-less" };
  const raw = { apiVersion: "example.test/v1", kind: "TaskClaim", metadata: { name: "ref-less", namespace: "shop", annotations: { "crossplane.io/paused": "true" } }, spec: {}, status: { conditions: [{ type: "Synced", status: "False", reason: "ReconcilePaused" }] } };
  const clean = resourceToYaml(raw);
  expect(clean).not.toContain("status:");
  expect(clean).not.toContain("ReconcilePaused");
  const onPreview = vi.fn(async () => ({ documents: [{ index: 0, status: "accepted" as const, action: "update" as const, baselineYaml: clean, predictedYaml: clean, reviewedResourceVersion: "1" }], nonAtomic: false }));
  act(() => root.render(<EditableYamlView resource={target as never} data={raw} onCopy={() => {}} copied={false} onSave={async () => {}} onPreview={onPreview} />));
  await click(/^Edit$/);
  await click(/Review changes/);
  expect(text()).toContain("Pause affects reconciliation");
  expect(text()).toContain("Server preview keeps the pause request on TaskClaim shop/ref-less");
  expect(onPreview).toHaveBeenCalledWith(expect.objectContaining({ yaml: clean }));
  expect(raw.status.conditions[0].reason).toBe("ReconcilePaused");
});
