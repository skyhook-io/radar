import { describe, expect, it } from "vitest";
import { ApiError, namespacesRefetchInterval } from "./client";

const failed = (error: unknown) => ({ state: { status: "error" as const, error } });

describe("namespacesRefetchInterval", () => {
  it("asks again while Radar couldn't read the list", () => {
    expect(namespacesRefetchInterval(failed(new ApiError("Radar couldn't list namespaces", 500)))).toBe(10_000);
    expect(namespacesRefetchInterval(failed(new ApiError("permission check failed", 503)))).toBe(10_000);
  });

  it("doesn't poll a denial or a list it has", () => {
    expect(namespacesRefetchInterval(failed(new ApiError("insufficient permissions", 403)))).toBe(false);
    expect(namespacesRefetchInterval({ state: { status: "success" as const, error: null } })).toBe(false);
  });
});
