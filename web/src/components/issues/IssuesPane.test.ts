import { describe, expect, it } from "vitest";
import type { Issue } from "@skyhook-io/k8s-ui";
import { capacityHrefForIssue, issueMatchesSubject, issueSubjectCoverage, issueSubjectFromParams } from "./IssuesPane";

function issue(partial: Partial<Issue>): Issue {
  return {
    id: "1",
    severity: "critical",
    source: "problem",
    category: "x",
    category_group: "x",
    grouping_scope: "x",
    kind: "Pod",
    name: "p",
    reason: "r",
    ...partial,
  } as Issue;
}

describe("capacityHrefForIssue", () => {
  it("returns null when Karpenter is not detected", () => {
    expect(capacityHrefForIssue(issue({ source: "scheduling" }), false)).toBeNull();
    expect(
      capacityHrefForIssue(
        issue({ kind: "NodePool", group: "karpenter.sh", name: "core" }),
        false),
    ).toBeNull();
  });

  it("links a backend-flagged capacity-relevant pod to the Demand queue", () => {
    expect(
      capacityHrefForIssue(
        issue({ source: "scheduling", capacity_relevant: true }),
        true),
    ).toBe("/capacity/demand");
  });

  it("preserves namespace scope on the Demand link", () => {
    expect(
      capacityHrefForIssue(
        issue({
          source: "scheduling",
          capacity_relevant: true,
          namespace: "payments",
          owner: { kind: "Deployment", name: "api" },
        }),
        true),
    ).toBe(`/capacity/demand?owner=${encodeURIComponent("payments/Deployment/api")}`);
  });

  it("does NOT link a scheduling failure the backend did not flag", () => {
    // Generic unschedulable (insufficient cpu, node-pinned, zonal, non-Karpenter
    // node group): the backend leaves capacity_relevant unset → no link, even in
    // a Karpenter cluster. No message parsing is involved.
    expect(
      capacityHrefForIssue(
        issue({
          source: "scheduling",
          message: "Unschedulable — Insufficient cpu (0/9 nodes available)",
        }),
        true),
    ).toBeNull();
    expect(
      capacityHrefForIssue(
        issue({ source: "scheduling", capacity_relevant: false }),
        true),
    ).toBeNull();
  });

  it("links a NodePool-subject issue to its pool detail", () => {
    expect(
      capacityHrefForIssue(
        issue({
          kind: "NodePool",
          group: "karpenter.sh",
          name: "core-on-demand",
          source: "condition",
        }),
        true),
    ).toBe("/capacity/pools/core-on-demand");
  });

  it("does not link non-Karpenter issues even when Karpenter is present", () => {
    expect(
      capacityHrefForIssue(issue({ source: "problem", kind: "Service" }), true),
    ).toBeNull();
    // A NodePool from a different API group must not be treated as Karpenter's.
    expect(
      capacityHrefForIssue(
        issue({ kind: "NodePool", group: "example.com", name: "x" }),
        true),
    ).toBeNull();
  });
});

describe("capacityHrefForIssue subject carry", () => {
  it("carries the grouped subject (the workload) into a filtered Demand", () => {
    expect(
      capacityHrefForIssue(
        issue({
          source: "scheduling",
          capacity_relevant: true,
          kind: "Deployment",
          namespace: "shop",
          name: "web",
        }),
        true,
      ),
    ).toBe("/capacity/demand?owner=shop%2FDeployment%2Fweb");
  });

  it("prefers the flat row's owner over its Pod subject", () => {
    expect(
      capacityHrefForIssue(
        issue({
          source: "scheduling",
          capacity_relevant: true,
          kind: "Pod",
          namespace: "shop",
          name: "web-abc12",
          owner: { kind: "Deployment", name: "web" },
        }),
        true,
      ),
    ).toBe("/capacity/demand?owner=shop%2FDeployment%2Fweb");
  });

  it("fails closed to the unfiltered link when no complete subject exists", () => {
    expect(
      capacityHrefForIssue(
        issue({
          source: "scheduling",
          capacity_relevant: true,
          kind: "Pod",
          namespace: "shop",
          name: "orphan-abc12",
        }),
        true,
      ),
    ).toBe("/capacity/demand");
  });
});

describe('issue subject links', () => {
  it('reads the subject a link narrows to', () => {
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&resource=pg%2Fpg-main'))).toEqual({ kind: 'Cluster', namespace: 'pg', name: 'pg-main' })
    expect(issueSubjectFromParams(new URLSearchParams('kind=ClusterImageCatalog&resource=pg'))).toEqual({ kind: 'ClusterImageCatalog', namespace: '', name: 'pg' })
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster'))).toBeNull()
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&resource=pg%2F'))).toBeNull()
  })
  it('never reads the view filter as the subject namespace', () => {
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&namespace=pg&name=pg-main'))).toBeNull()
  })
  it('treats a subject outside the view filter, or a capped list, as unknown rather than none', () => {
    const subject = { kind: 'Cluster', namespace: 'pg', name: 'main' }
    expect(issueSubjectCoverage(subject, ['app'], false)).toBe('hidden')
    expect(issueSubjectCoverage(subject, ['app'], true)).toBe('hidden')
    expect(issueSubjectCoverage(subject, ['app', 'pg'], false)).toBe('complete')
    expect(issueSubjectCoverage(subject, [], false)).toBe('complete')
    expect(issueSubjectCoverage(subject, [], true)).toBe('capped')
    expect(issueSubjectCoverage({ kind: 'ClusterImageCatalog', namespace: '', name: 'pg' }, ['app'], false)).toBe('complete')
  })
  it('matches an issue by its own subject or a grouped member', () => {
    const subject = { kind: 'Pod', namespace: 'pg', name: 'pg-main-1' }
    expect(issueMatchesSubject({ kind: 'Pod', namespace: 'pg', name: 'pg-main-1' } as never, subject)).toBe(true)
    expect(issueMatchesSubject({ kind: 'Cluster', namespace: 'pg', name: 'pg-main', members: [{ kind: 'Pod', namespace: 'pg', name: 'pg-main-1' }] } as never, subject)).toBe(true)
    expect(issueMatchesSubject({ kind: 'Pod', namespace: 'other', name: 'pg-main-1' } as never, subject)).toBe(false)
  })
})
