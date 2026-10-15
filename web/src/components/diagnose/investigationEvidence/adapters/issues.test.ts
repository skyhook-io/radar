import { describe, expect, it } from "vitest";
import { criticalIssue, groupsOf, project, tool } from "../evidenceFixtures";

const helmUnchecked = [{ source: "helm", reason: "read_failed" }];

describe("issues evidence with unchecked sources", () => {
  it("reports an empty answer with an unchecked source as a limit, not a clean search", () => {
    const result = project([
      tool("issues", "issues", {
        issues: [],
        total: 0,
        total_matched: 0,
        unchecked_sources: helmUnchecked,
      }),
    ]);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.limitations).toEqual([
      expect.objectContaining({
        source: "Issues",
        kind: "unknown",
        message: "Helm releases couldn't be checked, so failed or stuck releases may be missing.",
      }),
    ]);
  });

  it("keeps the issues it found and adds the limit", () => {
    const result = project([
      tool("issues", "issues", {
        issues: [criticalIssue],
        total: 1,
        total_matched: 1,
        unchecked_sources: helmUnchecked,
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(1);
    expect(result.limitations).toEqual([
      expect.objectContaining({ source: "Issues", kind: "unknown" }),
    ]);
  });

  it("names a source this version doesn't know generically", () => {
    const result = project([
      tool("issues", "issues", {
        issues: [],
        total: 0,
        total_matched: 0,
        unchecked_sources: [{ source: "argo", reason: "read_failed" }],
      }),
    ]);
    expect(result.limitations).toEqual([
      expect.objectContaining({
        message: "Some issue checks couldn't run, so issues may be missing.",
      }),
    ]);
  });

  it("treats malformed unchecked_sources as unknown coverage, never as clean", () => {
    for (const unchecked_sources of [
      { source: "helm", reason: "read_failed" },
      [{ source: { toString: null } }],
    ]) {
      const result = project([
        tool("issues", "issues", { issues: [], total: 0, total_matched: 0, unchecked_sources }),
      ]);
      expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
      expect(result.limitations).toEqual([
        expect.objectContaining({ source: "Issues", kind: "unknown" }),
      ]);
    }
  });

  it("still records a clean search when every source was checked", () => {
    const result = project([
      tool("issues", "issues", { issues: [], total: 0, total_matched: 0 }),
    ]);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(1);
    expect(result.limitations).toHaveLength(0);
  });
});
