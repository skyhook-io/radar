import { describe, expect, it } from "vitest";
import type { Issue } from "@skyhook-io/k8s-ui";

import { suggestionsFromIssues } from "./questionSuggestions";

function issue(patch: Partial<Issue>): Issue {
  return {
    id: patch.name ?? "x",
    severity: "warning",
    source: "problem",
    category: "crashloop",
    category_group: "runtime",
    grouping_scope: "workload",
    kind: "Deployment",
    group: "apps",
    namespace: "shop",
    name: "checkout",
    reason: "CrashLoopBackOff",
    ...patch,
  } as Issue;
}

describe("suggestionsFromIssues", () => {
  it("offers nothing when Radar sees nothing", () => {
    expect(suggestionsFromIssues([])).toEqual([]);
  });

  it("pins a problem on one resource and scopes a shared one to its namespace", () => {
    const suggestions = suggestionsFromIssues([
      issue({ name: "checkout" }),
      issue({ category: "image_pull_failed", namespace: "web", name: "a" }),
      issue({ category: "image_pull_failed", namespace: "web", name: "b" }),
    ]);
    const one = suggestions.find((s) => s.pin);
    expect(one?.pin).toEqual({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "checkout",
    });
    expect(one?.title).toBe("Crash loop");
    expect(one?.where).toBe("Deployment shop/checkout");
    expect(one?.question).toBe("What's causing “Crash loop”?");
    const shared = suggestions.find((s) => !s.pin);
    expect(shared?.namespaces).toEqual(["web"]);
    expect(shared?.question).toContain("on 2 resources in web");
    expect(shared?.where).toBe("2 resources · web");
  });

  it("counts resources, not issues, and keeps different problems apart", () => {
    const suggestions = suggestionsFromIssues([
      issue({
        id: "a",
        name: "pg",
        kind: "Cluster",
        group: "postgresql.cnpg.io",
        reason: "WALArchivingFailing",
      }),
      issue({
        id: "b",
        name: "pg",
        kind: "Cluster",
        group: "postgresql.cnpg.io",
        reason: "WALArchivingFailing",
      }),
      issue({
        id: "c",
        name: "pg",
        kind: "Cluster",
        group: "postgresql.cnpg.io",
        reason: "BackupFailed",
      }),
    ]);
    expect(suggestions.length).toBeGreaterThan(0);
    for (const suggestion of suggestions) {
      expect(suggestion.pin).toMatchObject({ kind: "Cluster", name: "pg" });
      expect(suggestion.where).not.toMatch(/resources/);
    }
  });

  it("offers different kinds of problem before more of the same", () => {
    const suggestions = suggestionsFromIssues(
      [
        issue({ category: "crashloop", namespace: "a", name: "a" }),
        issue({ category: "crashloop", namespace: "b", name: "b" }),
        issue({ category: "image_pull_failed", namespace: "c", name: "c" }),
      ],
      2,
    );
    expect(suggestions.map((s) => s.pin?.name)).toEqual(["a", "c"]);
  });

  it("puts the most severe problem first and stops at the cap", () => {
    const suggestions = suggestionsFromIssues(
      [
        issue({ category: "a", name: "a" }),
        issue({ category: "b", name: "b", severity: "critical" }),
        issue({ category: "c", name: "c" }),
        issue({ category: "d", name: "d" }),
      ],
      3,
    );
    expect(suggestions).toHaveLength(3);
    expect(suggestions[0].pin?.name).toBe("b");
    expect(suggestions.map((s) => s.severity)).toEqual([
      "critical",
      "warning",
      "warning",
    ]);
  });
});
