// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("../ui/YamlEditor", () => ({ YamlEditor: () => <div />, YamlDiffEditor: () => <div /> }));

const { EditableYamlView } = await import("./EditableYamlView");

vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
afterEach(() => vi.unstubAllGlobals());

function render(phone: boolean) {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: phone, media: query, addEventListener: () => {}, removeEventListener: () => {} }));
  const host = document.createElement("div");
  const root = createRoot(host);
  act(() =>
    root.render(
      <EditableYamlView
        resource={{ kind: "deployments", namespace: "default", name: "web" }}
        data={{ apiVersion: "apps/v1", kind: "Deployment", metadata: { name: "web", namespace: "default" } }}
        onCopy={() => {}}
        copied={false}
        onSave={async () => {}}
        onDuplicate={() => {}}
      />,
    ),
  );
  const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
  act(() => root.unmount());
  return buttons;
}

it("offers Edit and Duplicate off a phone", () => {
  expect(render(false)).toEqual(expect.arrayContaining(["Edit", "Duplicate"]));
});

it("keeps Copy and Download but drops Edit and Duplicate on a phone", () => {
  const buttons = render(true);
  expect(buttons).not.toContain("Edit");
  expect(buttons).not.toContain("Duplicate");
  expect(buttons.some((b) => b?.includes("Download"))).toBe(true);
});
