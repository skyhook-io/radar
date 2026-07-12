import { describe, expect, it } from "vitest";
import {
  buildWorkflowExecutionModel,
  flattenWorkflowExecution,
} from "./workflow-execution";

describe("workflow execution model", () => {
  it("builds execution edges and counts pods/nodes", () => {
    const model = buildWorkflowExecutionModel({
      metadata: {
        namespace: "demo",
        annotations: {
          "workflows.argoproj.io/scheduled-time": "2026-07-05T10:00:00Z",
        },
      },
      spec: {
        workflowTemplateRef: { name: "main-template" },
      },
      status: {
        phase: "Failed",
        startedAt: "2026-07-05T10:00:05Z",
        finishedAt: "2026-07-05T10:01:00Z",
        nodes: {
          root: {
            id: "root",
            displayName: "root",
            type: "DAG",
            phase: "Failed",
            startedAt: "2026-07-05T10:00:05Z",
            finishedAt: "2026-07-05T10:01:00Z",
            children: ["step-a", "step-b"],
          },
          "step-a": {
            id: "step-a",
            displayName: "step-a",
            type: "Pod",
            phase: "Succeeded",
            startedAt: "2026-07-05T10:00:10Z",
            finishedAt: "2026-07-05T10:00:20Z",
          },
          "step-b": {
            id: "step-b",
            displayName: "step-b",
            type: "Pod",
            phase: "Failed",
            message: "exit code 1",
            startedAt: "2026-07-05T10:00:30Z",
            finishedAt: "2026-07-05T10:00:40Z",
          },
        },
      },
    });

    expect(model.counts.podTotal).toBe(2);
    expect(model.counts.podSucceeded).toBe(1);
    expect(model.counts.podFailed).toBe(1);
    expect(model.counts.nodeTotal).toBe(3);
    expect(model.focusPaths[0].nodes.map((node) => node.id)).toEqual([
      "root",
      "step-b",
    ]);
    expect(model.templateRefs).toMatchObject([
      {
        name: "main-template",
        resourceKind: "workflowtemplates",
        namespace: "demo",
      },
    ]);
    expect(model.activity.map((item) => item.id)).toContain(
      "workflow-scheduled",
    );
  });

  it("uses boundaryID as a fallback parent when children are missing", () => {
    const model = buildWorkflowExecutionModel({
      status: {
        nodes: {
          group: { displayName: "group", type: "Steps", phase: "Failed" },
          child: {
            displayName: "child",
            type: "Pod",
            phase: "Failed",
            boundaryID: "group",
          },
        },
      },
    });

    expect(model.edges).toEqual([{ source: "group", target: "child" }]);
    expect(model.focusPaths[0].nodes.map((node) => node.id)).toEqual([
      "group",
      "child",
    ]);
  });

  it("collects task-level ClusterWorkflowTemplate refs", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { namespace: "demo" },
      spec: {
        templates: [
          {
            name: "main",
            dag: {
              tasks: [
                {
                  name: "one",
                  templateRef: {
                    name: "cluster-lib",
                    template: "worker",
                    clusterScope: true,
                  },
                },
              ],
            },
          },
        ],
      },
    });

    expect(model.templateRefs).toEqual([
      {
        name: "cluster-lib",
        kind: "ClusterWorkflowTemplate",
        resourceKind: "clusterworkflowtemplates",
        namespace: "",
        clusterScope: true,
        source: "task",
        template: "worker",
        taskName: "one",
      },
    ]);
  });

  it("uses exact status-node template refs and stored workflow specs", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { namespace: "demo" },
      spec: { workflowTemplateRef: { name: "main-definition" } },
      status: {
        storedWorkflowTemplateSpec: {
          templates: [
            {
              name: "stored",
              dag: {
                tasks: [
                  {
                    name: "library",
                    templateRef: { name: "stored-lib", template: "run" },
                  },
                ],
              },
            },
          ],
        },
        nodes: {
          task: {
            displayName: "library(0)",
            type: "Pod",
            phase: "Succeeded",
            templateRef: {
              name: "exact-lib",
              template: "run",
              clusterScope: true,
            },
          },
        },
      },
    });

    expect(model.nodes[0].templateRef).toMatchObject({
      name: "exact-lib",
      resourceKind: "clusterworkflowtemplates",
    });
    expect(model.templateRefs.map((ref) => ref.name)).toEqual(
      expect.arrayContaining(["main-definition", "stored-lib", "exact-lib"]),
    );
  });

  it("contracts sequential StepGroups while preserving authored step order", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { name: "workflow-abc" },
      status: {
        phase: "Succeeded",
        startedAt: "2026-07-05T10:00:00Z",
        finishedAt: "2026-07-05T10:01:00Z",
        nodes: {
          root: {
            name: "workflow-abc",
            displayName: "workflow-abc",
            templateName: "main",
            type: "Steps",
            phase: "Succeeded",
            children: ["group-0"],
          },
          "group-0": {
            name: "workflow-abc[0]",
            displayName: "[0]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["prepare"],
          },
          prepare: {
            name: "workflow-abc[0].prepare",
            displayName: "prepare",
            type: "Pod",
            phase: "Succeeded",
            children: ["group-1"],
            startedAt: "2026-07-05T10:00:01Z",
            finishedAt: "2026-07-05T10:00:20Z",
          },
          "group-1": {
            name: "workflow-abc[1]",
            displayName: "[1]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["process"],
          },
          process: {
            name: "workflow-abc[1].process",
            displayName: "process",
            type: "Pod",
            phase: "Succeeded",
            startedAt: "2026-07-05T10:00:21Z",
            finishedAt: "2026-07-05T10:00:40Z",
          },
        },
      },
    });

    expect(model.nodes).toHaveLength(5);
    expect(model.executionNodes).toHaveLength(3);
    expect(
      flattenWorkflowExecution(model).map(({ node, depth }) => [
        node.displayLabel,
        node.displayType,
        depth,
      ]),
    ).toEqual([
      ["main", "Steps template", 0],
      ["prepare", "Step 1 · Pod", 1],
      ["process", "Step 2 · Pod", 2],
    ]);
    expect(model.activity.map((item) => item.label)).toEqual([
      "Workflow started",
      "prepare started",
      "prepare succeeded",
      "process started",
      "process succeeded",
      "Workflow succeeded",
    ]);
  });

  it("represents parallel steps and loop fan-out without controller indexes", () => {
    const parallel = buildWorkflowExecutionModel({
      metadata: { name: "parallel-wf" },
      status: {
        nodes: {
          root: {
            name: "parallel-wf",
            displayName: "parallel-wf",
            templateName: "main",
            type: "Steps",
            phase: "Succeeded",
            children: ["group"],
          },
          group: {
            name: "parallel-wf[0]",
            displayName: "[0]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["a", "b"],
          },
          a: { displayName: "download", type: "Pod", phase: "Succeeded" },
          b: { displayName: "validate", type: "Pod", phase: "Succeeded" },
        },
      },
    });
    expect(
      parallel.executionNodes.find((node) => node.id === "group"),
    ).toMatchObject({
      displayLabel: "Step 1",
      displayType: "Parallel · 2 steps",
    });

    const fanOut = buildWorkflowExecutionModel({
      metadata: { name: "fanout-wf" },
      status: {
        nodes: {
          root: {
            name: "fanout-wf",
            displayName: "fanout-wf",
            templateName: "main",
            type: "Steps",
            phase: "Succeeded",
            children: ["group"],
          },
          group: {
            name: "fanout-wf[0]",
            displayName: "[0]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["a", "b"],
          },
          a: {
            displayName: "migrate(0:tenant-a)",
            type: "Pod",
            phase: "Succeeded",
          },
          b: {
            displayName: "migrate(1:tenant-b)",
            type: "Pod",
            phase: "Succeeded",
          },
        },
      },
    });
    expect(
      fanOut.executionNodes.find((node) => node.id === "group"),
    ).toMatchObject({
      displayLabel: "migrate",
      displayType: "Step 1 · Fan-out · 2 runs",
    });
    expect(
      fanOut.executionNodes
        .filter((node) => node.type === "Pod")
        .map((node) => [node.displayLabel, node.displayType]),
    ).toEqual([
      ["tenant-a", "Run 1 · Pod"],
      ["tenant-b", "Run 2 · Pod"],
    ]);
  });

  it("keeps mixed StepGroup children parallel and names retry attempts from suffixes", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { name: "retry-wf" },
      status: {
        nodes: {
          retry: {
            name: "retry-wf",
            displayName: "retry-wf",
            templateName: "main",
            type: "Retry",
            phase: "Failed",
            children: ["attempt-1", "attempt-0"],
          },
          "attempt-1": {
            displayName: "retry-wf(1)",
            type: "Pod",
            phase: "Failed",
          },
          "attempt-0": {
            displayName: "retry-wf(0)",
            type: "Pod",
            phase: "Failed",
          },
        },
      },
    });
    expect(
      model.executionNodes.find((node) => node.id === "retry"),
    ).toMatchObject({ displayLabel: "main", displayType: "Retry strategy" });
    expect(
      Object.fromEntries(
        model.executionNodes
          .filter((node) => node.type === "Pod")
          .map((node) => [node.id, node.displayLabel]),
      ),
    ).toEqual({
      "attempt-0": "Attempt 1",
      "attempt-1": "Attempt 2",
    });

    const mixed = buildWorkflowExecutionModel({
      status: {
        nodes: {
          group: {
            name: "wf[3]",
            displayName: "[3]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["a", "b"],
          },
          a: {
            displayName: "migrate(0:tenant-a)",
            type: "Pod",
            phase: "Succeeded",
          },
          b: { displayName: "notify", type: "Pod", phase: "Succeeded" },
        },
      },
    });
    expect(
      mixed.executionNodes.find((node) => node.id === "group"),
    ).toMatchObject({
      displayLabel: "Step 4",
      displayType: "Parallel · 2 steps",
    });
  });

  it("keeps named DAG fan-out and hides its aggregate lifecycle from activity", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { name: "dag-wf" },
      status: {
        phase: "Failed",
        startedAt: "2026-07-05T10:00:00Z",
        finishedAt: "2026-07-05T10:01:00Z",
        nodes: {
          root: {
            name: "dag-wf",
            displayName: "dag-wf",
            templateName: "main-dag",
            type: "DAG",
            phase: "Failed",
            message: "child failed",
            children: ["group"],
          },
          group: {
            displayName: "tenants",
            type: "TaskGroup",
            phase: "Failed",
            message: "child failed",
            children: ["a", "b"],
          },
          a: {
            displayName: "tenants(0:a)",
            type: "Pod",
            phase: "Succeeded",
            startedAt: "2026-07-05T10:00:01Z",
            finishedAt: "2026-07-05T10:00:10Z",
          },
          b: {
            displayName: "tenants(1:b)",
            type: "Pod",
            phase: "Failed",
            message: "exit code 1",
            startedAt: "2026-07-05T10:00:01Z",
            finishedAt: "2026-07-05T10:00:11Z",
          },
        },
      },
    });
    expect(
      model.executionNodes.find((node) => node.id === "group"),
    ).toMatchObject({
      displayLabel: "tenants",
      displayType: "Fan-out · 2 runs",
    });
    expect(model.activity.map((item) => item.label)).not.toContain(
      "tenants failed",
    );
    expect(model.activity.map((item) => item.label)).toContain("b failed");
    expect(model.counts.podFailed).toBe(1);
    expect(model.counts.nodeFailed).toBe(3);
  });

  it("retains structural-only failures, exit handlers, and failing container diagnostics", () => {
    const model = buildWorkflowExecutionModel({
      metadata: { name: "hook-wf" },
      status: {
        nodes: {
          root: {
            name: "hook-wf",
            displayName: "hook-wf",
            templateName: "main",
            type: "DAG",
            phase: "Failed",
          },
          group: {
            name: "hook-wf[2]",
            displayName: "[2]",
            type: "StepGroup",
            phase: "Error",
            message: "could not resolve template",
            boundaryID: "root",
          },
          exit: {
            name: "hook-wf.onExit",
            displayName: "hook-wf.onExit",
            templateName: "cleanup",
            type: "Steps",
            phase: "Failed",
          },
          container: {
            displayName: "sidecar",
            type: "Container",
            phase: "Error",
            message: "sidecar crashed",
            boundaryID: "exit",
            startedAt: "2026-07-05T10:00:01Z",
            finishedAt: "2026-07-05T10:00:02Z",
          },
        },
      },
    });
    expect(
      model.executionNodes.find((node) => node.id === "group"),
    ).toMatchObject({ displayLabel: "Step 3", displayType: "Step group" });
    expect(
      model.executionNodes.find((node) => node.id === "exit"),
    ).toMatchObject({ displayLabel: "Exit handler" });
    expect(model.activity.map((item) => item.label)).toContain(
      "sidecar errored",
    );
  });

  it("contracts hidden chains without losing nodes across multiple parents", () => {
    const model = buildWorkflowExecutionModel({
      status: {
        nodes: {
          left: {
            displayName: "left",
            type: "DAG",
            phase: "Succeeded",
            children: ["group-a"],
          },
          right: {
            displayName: "right",
            type: "DAG",
            phase: "Succeeded",
            children: ["group-a"],
          },
          "group-a": {
            name: "wf[0]",
            displayName: "[0]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["group-b"],
          },
          "group-b": {
            name: "wf[1]",
            displayName: "[1]",
            type: "StepGroup",
            phase: "Succeeded",
            children: ["leaf"],
          },
          leaf: { displayName: "leaf", type: "Pod", phase: "Succeeded" },
        },
      },
    });
    expect(model.nodes.map((node) => node.id)).toEqual(
      expect.arrayContaining(["left", "right", "group-a", "group-b", "leaf"]),
    );
    expect(model.executionNodes.map((node) => node.id).sort()).toEqual([
      "leaf",
      "left",
      "right",
    ]);
    expect(
      model.executionNodes.find((node) => node.id === "leaf")?.parentIds.sort(),
    ).toEqual(["left", "right"]);
    expect(
      flattenWorkflowExecution(model)
        .map(({ node }) => node.id)
        .sort(),
    ).toEqual(["leaf", "left", "right"]);
  });
});
