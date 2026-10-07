// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it, vi } from "vitest";
import { HPARenderer } from "./HPARenderer";
import { VPARenderer } from "./VPARenderer";
import {
  initNavigationMap,
  resetNavigationMap,
} from "../../../utils/navigation";
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

it.each(["HPA", "VPA"])(
  "retains a %s custom scale target API group",
  async (kind) => {
    initNavigationMap([
      {
        group: "custom.example.io",
        version: "v1",
        kind: "Deployment",
        name: "deployments",
        namespaced: true,
        isCrd: true,
        verbs: ["get"],
      },
    ]);
    const element = document.createElement("div");
    const root = createRoot(element);
    const onNavigate = vi.fn();
    const target = {
      apiVersion: "custom.example.io/v1",
      kind: "Deployment",
      name: "same-name",
    };
    const data = {
      metadata: { namespace: "team" },
      spec: kind === "HPA" ? { scaleTargetRef: target } : { targetRef: target },
    };
    try {
      await act(async () =>
        root.render(
          kind === "HPA" ? (
            <HPARenderer data={data} onNavigate={onNavigate} />
          ) : (
            <VPARenderer data={data} onNavigate={onNavigate} />
          ),
        ),
      );
      const button = [...element.querySelectorAll("button")].find(
        (b) => b.textContent === "Deployment/same-name",
      );
      expect(button).toBeDefined();
      await act(async () => button!.click());
      expect(onNavigate).toHaveBeenCalledWith({
        kind: "Deployment",
        namespace: "team",
        name: "same-name",
        group: "custom.example.io",
      });
    } finally {
      await act(async () => root.unmount());
      resetNavigationMap();
    }
  },
);

it("keeps Object metric resources separate from the scale target and measures its own Namespace", async () => {
  const element = document.createElement("div");
  const root = createRoot(element);
  const onNavigate = vi.fn();
  const data = {
    metadata: { namespace: "team" },
    spec: {
      scaleTargetRef: {
        apiVersion: "apps/v1",
        kind: "Deployment",
        name: "workload",
      },
      metrics: [
        {
          type: "Object",
          object: {
            metric: { name: "requests" },
            describedObject: {
              apiVersion: "v1",
              kind: "Service",
              name: "frontend",
            },
          },
        },
        {
          type: "Object",
          object: {
            metric: { name: "queue" },
            describedObject: {
              apiVersion: "custom.example.io/v1",
              kind: "Widget",
              name: "input",
            },
          },
        },
        {
          type: "Object",
          object: {
            metric: { name: "namespace-metric" },
            describedObject: {
              apiVersion: "v1",
              kind: "Namespace",
              name: "cannot-escape",
            },
          },
        },
      ],
    },
  };
  try {
    await act(async () =>
      root.render(<HPARenderer data={data} onNavigate={onNavigate} />),
    );
    for (const [label, expected] of [
      [
        "Service/frontend",
        { kind: "Service", group: "", namespace: "team", name: "frontend" },
      ],
      [
        "Widget/input",
        {
          kind: "Widget",
          group: "custom.example.io",
          namespace: "team",
          name: "input",
        },
      ],
      [
        "Namespace/team",
        { kind: "Namespace", group: "", namespace: "", name: "team" },
      ],
    ] as const) {
      const button = [...element.querySelectorAll("button")].find(
        (b) => b.textContent === label,
      );
      expect(button).toBeDefined();
      await act(async () => button!.click());
      expect(onNavigate).toHaveBeenLastCalledWith(expected);
    }
    expect(element.textContent).toContain("Object metric sources");
    expect(element.textContent).toContain("This HPA's namespace");
    expect(element.textContent).not.toContain("Namespace/cannot-escape");
  } finally {
    await act(async () => root.unmount());
  }
});

it.each(["HPA", "VPA"])("does not infer missing %s target identity", (kind) => {
  const target = { name: "ambiguous" };
  const data = {
    metadata: { namespace: "team" },
    spec: kind === "HPA" ? { scaleTargetRef: target } : { targetRef: target },
  };
  const html = renderToStaticMarkup(
    kind === "HPA" ? (
      <HPARenderer data={data} onNavigate={() => {}} />
    ) : (
      <VPARenderer data={data} onNavigate={() => {}} />
    ),
  );
  const doc = new DOMParser().parseFromString(html, "text/html");
  expect(
    [...doc.querySelectorAll("button")].some((b) =>
      b.textContent?.includes("ambiguous"),
    ),
  ).toBe(false);
  expect(html).not.toContain("Deployment/ambiguous");
});
