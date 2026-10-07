// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, expect, it, vi } from "vitest";
import { MetadataSection } from "./drawer-components";
import {
  customResourceDefinitionRef,
  initNavigationMap,
  resetNavigationMap,
} from "../../utils/navigation";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
afterEach(resetNavigationMap);
const resources = [
  {
    group: "plants.example.io",
    version: "v1",
    kind: "Cactus",
    name: "cacti",
    namespaced: true,
    isCrd: true,
    verbs: ["get"],
  },
  {
    group: "other.example.io",
    version: "v1",
    kind: "Cactus",
    name: "cactuses",
    namespaced: false,
    isCrd: true,
    verbs: ["get"],
  },
  {
    group: "apps",
    version: "v1",
    kind: "Deployment",
    name: "deployments",
    namespaced: true,
    isCrd: false,
    verbs: ["get"],
  },
];

it("uses exact discovered CRD group and plural instead of guessing from the Kind", () => {
  expect(
    customResourceDefinitionRef("plants.example.io/v1", "Cactus"),
  ).toBeNull();
  initNavigationMap(resources);
  expect(customResourceDefinitionRef("plants.example.io/v1", "Cactus")).toEqual(
    {
      kind: "CustomResourceDefinition",
      group: "apiextensions.k8s.io",
      namespace: "",
      name: "cacti.plants.example.io",
    },
  );
  expect(
    customResourceDefinitionRef("other.example.io/v1", "Cactus")?.name,
  ).toBe("cactuses.other.example.io");
  expect(customResourceDefinitionRef("apps/v1", "Deployment")).toBeNull();
  expect(
    customResourceDefinitionRef("unknown.example.io/v1", "Cactus"),
  ).toBeNull();
  expect(customResourceDefinitionRef(undefined, "Cactus")).toBeNull();
  initNavigationMap([]);
  expect(
    customResourceDefinitionRef("plants.example.io/v1", "Cactus"),
  ).toBeNull();
});

it("navigates from instance metadata to its cluster-scoped definition", async () => {
  initNavigationMap(resources);
  const element = document.createElement("div");
  const root = createRoot(element);
  const onNavigate = vi.fn();
  try {
    await act(async () =>
      root.render(
        <MetadataSection
          data={{
            apiVersion: "plants.example.io/v1",
            kind: "Cactus",
            metadata: { name: "live", namespace: "team" },
          }}
          onNavigate={onNavigate}
        />,
      ),
    );
    const button = [...element.querySelectorAll("button")].find(
      (b) => b.textContent === "cacti.plants.example.io",
    );
    expect(button).toBeDefined();
    await act(async () => button!.click());
    expect(onNavigate).toHaveBeenCalledWith({
      kind: "CustomResourceDefinition",
      group: "apiextensions.k8s.io",
      namespace: "",
      name: "cacti.plants.example.io",
    });
  } finally {
    await act(async () => root.unmount());
  }
});

it("does not add a guessed definition to built-in or undiscovered metadata", () => {
  initNavigationMap(resources);
  const html = renderToStaticMarkup(
    <MetadataSection
      data={{
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { name: "live", uid: "native" },
      }}
    />,
  );
  expect(html).toContain("native");
  expect(html).not.toContain("Definition");
});
