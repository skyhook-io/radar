import { describe, expect, it } from "vitest";
import { minorsBehind } from "./ClusterHealthCard";

describe("minorsBehind", () => {
  it("counts minors between plain versions", () => {
    expect(minorsBehind("1.33", "1.37")).toBe(4);
    expect(minorsBehind("1.36", "1.37")).toBe(1);
  });

  it("returns 0 when current matches or exceeds the catalog", () => {
    expect(minorsBehind("1.37", "1.37")).toBe(0);
    expect(minorsBehind("1.38", "1.37")).toBe(0);
  });

  it("handles distribution version suffixes", () => {
    expect(minorsBehind("v1.33.6+k3s1", "1.37")).toBe(4);
    expect(minorsBehind("1.31.5-gke.1023000", "1.37")).toBe(6);
    expect(minorsBehind("v1.32.4-eks-abc123", "1.37")).toBe(5);
  });

  it("stays quiet on cross-major or unparseable input", () => {
    expect(minorsBehind("2.0.0", "1.37")).toBe(0);
    expect(minorsBehind("unknown", "1.37")).toBe(0);
    expect(minorsBehind("", "1.37")).toBe(0);
  });

  it("stays quiet when the catalog version is missing", () => {
    expect(minorsBehind("1.33", undefined)).toBe(0);
    expect(minorsBehind("1.33", "")).toBe(0);
  });
});
