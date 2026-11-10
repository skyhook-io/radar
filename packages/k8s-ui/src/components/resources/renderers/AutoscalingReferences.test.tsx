// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it, vi } from "vitest";
import { HPARenderer } from "./HPARenderer";
import { VPARenderer } from "./VPARenderer";
import { KedaScaledObjectRenderer } from "./KedaScaledObjectRenderer";
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
      expect([...element.querySelectorAll("button")].filter(b => b.textContent?.startsWith("Deployment/same-name"))).toHaveLength(1);
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

it.each(["v1", "", undefined])("keeps Object metric resources separate from the scale target and measures its own Namespace (apiVersion %s)", async (namespaceAPIVersion) => {
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
              apiVersion: "networking.k8s.io/v1",
              kind: "Ingress",
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
              apiVersion: namespaceAPIVersion,
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
        "Ingress/frontend",
        { kind: "Ingress", group: "networking.k8s.io", namespace: "team", name: "frontend" },
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

it.each([
  [
    { name: "worker" },
    { kind: "Deployment", group: "apps", namespace: "team", name: "worker" },
  ],
  [
    { name: "worker", apiVersion: "", kind: "" },
    { kind: "Deployment", group: "apps", namespace: "team", name: "worker" },
  ],
  [
    { apiVersion: "custom.example.io/v1", kind: "Deployment", name: "worker" },
    {
      kind: "Deployment",
      group: "custom.example.io",
      namespace: "team",
      name: "worker",
    },
  ],
])(
  "preserves KEDA documented defaults and explicit custom API identity",
  async (target, expected) => {
    const element = document.createElement("div");
    const root = createRoot(element);
    const onNavigate = vi.fn();
    try {
      await act(async () =>
        root.render(
          <KedaScaledObjectRenderer
            data={{
              metadata: { namespace: "team" },
              spec: { scaleTargetRef: target },
            }}
            onNavigate={onNavigate}
          />,
        ),
      );
      const button = [...element.querySelectorAll("button")].find(
        (b) => b.textContent === "Deployment/worker",
      );
      expect(button).toBeDefined();
      await act(async () => button!.click());
      expect(onNavigate).toHaveBeenCalledWith(expected);
    } finally {
      await act(async () => root.unmount());
    }
  },
);

it.each([
  ["Deployment", "apps"], ["StatefulSet", "apps"], ["DaemonSet", "apps"], ["ReplicaSet", "apps"],
  ["Job", "batch"], ["CronJob", "batch"], ["ReplicationController", ""],
])("keeps versionless VPA %s target navigation", async (kind, group) => {
  const element = document.createElement("div");
  const root = createRoot(element);
  const onNavigate = vi.fn();
  try {
    await act(async () => root.render(<VPARenderer data={{ metadata: { namespace: "team" }, spec: { targetRef: { kind, name: "workload" } } }} onNavigate={onNavigate} />));
    const link = [...element.querySelectorAll("button")].find(button => button.textContent === `${kind}/workload`);
    expect(link).toBeDefined();
    await act(async () => link!.click());
    expect(onNavigate).toHaveBeenLastCalledWith({ kind, group, namespace: "team", name: "workload" });
  } finally {
    await act(async () => root.unmount());
  }
});

it("pairs each diagnosis metric card with its declared Object source once", async () => {
  const element = document.createElement("div");
  const root = createRoot(element);
  const onNavigate = vi.fn();
  const objects = [
    { metric: { name: "requests" }, describedObject: { apiVersion: "v1", kind: "Endpoints", name: "input" } },
    { metric: { name: "requests" }, describedObject: { apiVersion: "custom.example.io/v1", kind: "Endpoints", name: "input" } },
    { metric: { name: "queue_depth" }, describedObject: { kind: "Namespace", name: "ignored" } },
  ];
  try {
    await act(async () => root.render(<HPARenderer
      data={{ metadata: { namespace: "team" }, spec: { metrics: objects.map(object => ({ type: "Object", object })) } }}
      hpaDiagnosis={{ state: "stabilized", summary: "Stabilized", target: {}, bounds: { min: 1, max: 3, current: 2, desired: 2 }, metrics: [
        { type: "Object", name: "Endpoints/input/requests", current: "10", status: "ok" },
        { type: "Object", name: "Endpoints (custom.example.io)/input/requests", current: "20", status: "ok" },
        { type: "Object", name: "Namespace/team/queue_depth", current: "7", status: "ok" },
        { type: "Object", name: "Service/stale/requests", current: "3", status: "status_only" },
      ] }} onNavigate={onNavigate} />));
    expect(element.textContent).not.toContain("Object metric sources");
    expect(element.textContent).not.toContain("ignored");
    const endpoints = [...element.querySelectorAll("button")].filter(button => button.textContent === "Endpoints/input");
    expect(endpoints).toHaveLength(2);
    await act(async () => endpoints[0].click());
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: "Endpoints", group: "", namespace: "team", name: "input" });
    await act(async () => endpoints[1].click());
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: "Endpoints", group: "custom.example.io", namespace: "team", name: "input" });
    const namespace = [...element.querySelectorAll("button")].find(button => button.textContent === "Namespace/team");
    expect(namespace).toBeDefined();
    await act(async () => namespace!.click());
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: "Namespace", group: "", namespace: "", name: "team" });
    expect([...element.querySelectorAll("button")].some(button => button.textContent === "Service/stale")).toBe(false);
    for (const value of ["Current 10", "Current 20", "Current 7"]) expect(element.textContent).toContain(value);
  } finally {
    await act(async () => root.unmount());
  }
});

it("shows the documented default Target API for KEDA", () => {
  const html = renderToStaticMarkup(<KedaScaledObjectRenderer data={{ metadata: { namespace: "team" }, spec: { scaleTargetRef: { name: "worker" } } }} />);
  expect(html).toContain("Target API");
  expect(html).toContain("apps/v1");
});
