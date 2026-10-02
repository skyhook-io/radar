// @vitest-environment jsdom
//
// Applying a reviewed edit can be refused because the resource changed after
// the review. The review then refreshes against the latest version and says
// so, once. That notice belongs to one attempt: a retry that fails for another
// reason shows its own error.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

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
