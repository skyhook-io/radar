import { describe, expect, it } from "vitest";
import { UNKNOWN_SOURCE_LINE, uncheckedSourceLines } from "./uncheckedSources";

describe("uncheckedSourceLines", () => {
  it("names one list-only kind", () => {
    expect(uncheckedSourceLines([{ source: "absence", reason: "list_only", resource: "secrets" }])).toEqual([
      "Radar doesn't report missing Secrets: it can list Secrets but not watch them, so it can't confirm one is missing. Grant Radar watch access to Secrets and restart Radar to restore these checks.",
    ]);
  });

  it("merges several kinds into one line after Helm", () => {
    expect(
      uncheckedSourceLines([
        { source: "absence", reason: "stale", resource: "secrets" },
        { source: "helm", reason: "read_failed" },
        { source: "absence", reason: "list_only", resource: "pods" },
        { source: "absence", reason: "list_only", resource: "serviceaccounts" },
      ]),
    ).toEqual([
      "Helm releases couldn't be checked, so failed or stuck releases may be missing.",
      "Radar doesn't report missing Secrets, Pods and ServiceAccounts: it can list these kinds but not watch them, so it can't confirm one is missing. Grant Radar watch access to these kinds and restart Radar to restore these checks. Radar's copy has not refreshed for several minutes.",
    ]);
  });

  it("treats an absence entry without a resource as unknown", () => {
    expect(uncheckedSourceLines([{ source: "absence", reason: "list_only" }])).toEqual([UNKNOWN_SOURCE_LINE]);
  });
});
