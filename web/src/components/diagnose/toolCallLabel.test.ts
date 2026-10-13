import { describe, expect, it } from "vitest";
import { describeToolCall, investigationRunningLabel } from "./toolCallLabel";

describe("describeToolCall", () => {
  it("never invents a singular from a kind", () => {
    expect(
      describeToolCall(
        "get_resource",
        JSON.stringify({ kind: "Ingress", namespace: "web", name: "edge" }),
      ),
    ).toBe("Reading Ingress web/edge");
    expect(
      describeToolCall("list_resources", JSON.stringify({ kind: "ingresses" })),
    ).toBe("Listing Ingresses");
  });

  it("names the target from the call's arguments", () => {
    expect(
      describeToolCall(
        "get_resource",
        JSON.stringify({ kind: "deployment", namespace: "shop", name: "api" }),
      ),
    ).toBe("Reading Deployment shop/api");
    expect(
      describeToolCall(
        "get_pod_logs",
        JSON.stringify({
          namespace: "shop",
          name: "api-7d4",
          container: "api",
          previous: true,
        }),
      ),
    ).toBe("Reading logs for pod shop/api-7d4 / api (previous instance)");
    expect(
      describeToolCall(
        "list_resources",
        JSON.stringify({ kind: "configmaps", namespace: "shop" }),
      ),
    ).toBe("Listing Configmaps in shop");
    expect(
      describeToolCall("search", JSON.stringify({ query: "metrics-server" })),
    ).toBe("Searching for “metrics-server”");
  });
  it("distinguishes deletion previews from confirmed deletes", () => {
    const target = { kind: "Deployment", namespace: "shop", name: "api" };
    expect(describeToolCall("delete_resource", JSON.stringify(target))).toBe("Previewing deletion of Deployment shop/api");
    expect(describeToolCall("delete_resource", JSON.stringify({ ...target, dry_run: true }))).toBe("Previewing deletion of Deployment shop/api");
    expect(describeToolCall("delete_resource", JSON.stringify({ ...target, dry_run: false }))).toBe("Deleting Deployment shop/api");
  });
  it("distinguishes Helm previews from executing actions", () => {
    expect(describeToolCall("manage_helm_release", undefined)).toBe("Managing Helm release");
    const target = { namespace: "store", name: "api", action: "uninstall" };
    expect(describeToolCall("manage_helm_release", JSON.stringify(target))).toBe("Previewing uninstall of store/api");
    expect(describeToolCall("manage_helm_release", JSON.stringify({ ...target, dry_run: false }))).toBe("Uninstalling store/api");
    const rollback = { ...target, action: "rollback", revision: 1 };
    expect(describeToolCall("manage_helm_release", JSON.stringify(rollback))).toBe("Previewing rollback of store/api to revision 1");
    expect(describeToolCall("manage_helm_release", JSON.stringify({ ...rollback, dry_run: false }))).toBe("Rolling back store/api to revision 1");
  });
  it("falls back to the tool name when arguments are missing or malformed", () => {
    expect(describeToolCall("get_changes", "not json")).toBe(
      "Reading recent changes",
    );
    expect(describeToolCall("some_new_tool", undefined)).toBe("Some New Tool");
  });
});

describe("investigationRunningLabel", () => {
  const read = {
    tool: "get_resource",
    status: "done",
    summary: JSON.stringify({
      kind: "deployment",
      namespace: "shop",
      name: "api",
    }),
  };
  it("names the call in flight, keeps a finished one briefly, then thinks", () => {
    expect(
      investigationRunningLabel({
        lastTool: { ...read, status: "running" },
        reads: 1,
        quietFor: 20000,
        elapsedSeconds: 9,
      }),
    ).toEqual({
      current: "Reading Deployment shop/api…",
      meta: "1 read · 9 s",
    });
    expect(
      investigationRunningLabel({
        lastTool: read,
        reads: 3,
        quietFor: 7999,
        elapsedSeconds: 30,
      }).current,
    ).toBe("Reading Deployment shop/api…");
    expect(
      investigationRunningLabel({
        lastTool: read,
        reads: 3,
        quietFor: 8000,
        elapsedSeconds: 30,
      }),
    ).toEqual({ current: "Thinking…", meta: "3 reads · 30 s" });
  });
  it("says Investigating with nothing read yet and omits a timer under two seconds", () => {
    expect(
      investigationRunningLabel({ reads: 0, quietFor: 0, elapsedSeconds: 1 }),
    ).toEqual({ current: "Investigating…", meta: "" });
  });
});
