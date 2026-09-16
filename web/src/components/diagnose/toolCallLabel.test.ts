import { describe, expect, it } from "vitest";
import { describeToolCall } from "./toolCallLabel";

describe("describeToolCall", () => {
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
  it("falls back to the tool name when arguments are missing or malformed", () => {
    expect(describeToolCall("get_changes", "not json")).toBe(
      "Reading recent changes",
    );
    expect(describeToolCall("some_new_tool", undefined)).toBe("Some New Tool");
  });
});
