import { describe, expect, it } from "vitest";
import type { Issue } from "@skyhook-io/k8s-ui";
import { capacityHrefForIssue, issueSubjectFromParams, issueSubjectState } from "./IssuesPane";

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
  it('reads the subject a link narrows to, with its API group', () => {
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&group=postgresql.cnpg.io&resource=pg%2Fpg-main'))).toEqual({ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'pg-main' })
    expect(issueSubjectFromParams(new URLSearchParams('kind=Pod&resource=pg%2Fpg-main-1'))).toEqual({ kind: 'Pod', group: '', namespace: 'pg', name: 'pg-main-1' })
    expect(issueSubjectFromParams(new URLSearchParams('kind=ClusterImageCatalog&group=postgresql.cnpg.io&resource=pg'))).toEqual({ kind: 'ClusterImageCatalog', group: 'postgresql.cnpg.io', namespace: '', name: 'pg' })
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster'))).toBeNull()
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&resource=pg%2F'))).toBeNull()
  })
  it('never reads the view filter as the subject namespace', () => {
    expect(issueSubjectFromParams(new URLSearchParams('kind=Cluster&namespace=pg&name=pg-main'))).toBeNull()
  })
})

describe('issueSubjectState', () => {
  const subject = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'main' }
  const loaded = (data: Issue[]) => ({ isLoading: false, error: null, data })
  it('says the view filter hides the subject rather than none', () => {
    expect(issueSubjectState(subject, ['app'], loaded([]), undefined)).toEqual({ state: 'hidden' })
    expect(issueSubjectState(subject, ['app', 'pg'], loaded([]), undefined)).toEqual({ state: 'none' })
    expect(issueSubjectState(subject, [], loaded([]), undefined)).toEqual({ state: 'none' })
    expect(issueSubjectState({ ...subject, kind: 'ClusterImageCatalog', namespace: '' }, ['app'], loaded([]), undefined)).toEqual({ state: 'none' })
  })
  it('reads an empty answer under limited visibility as unconfirmed, never none', () => {
    expect(issueSubjectState(subject, [], loaded([]), { state: 'degraded', impact: 'Pods are not readable.' })).toEqual({ state: 'unconfirmed', why: "some evidence isn't readable (Pods are not readable)" })
    expect(issueSubjectState(subject, [], loaded([]), { state: 'limited' })).toEqual({ state: 'unconfirmed', why: "some evidence isn't readable" })
  })
  it('reports a failed lookup as unconfirmed and a pending one as checking', () => {
    expect(issueSubjectState(subject, [], { isLoading: false, error: new Error('forbidden'), data: undefined }, undefined)).toEqual({ state: 'unconfirmed', why: 'forbidden' })
    expect(issueSubjectState(subject, [], { isLoading: true, error: null, data: undefined }, undefined)).toEqual({ state: 'checking' })
  })
  it('takes the per-resource answer, which covers members past the inline cap', () => {
    const grouped = issue({ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'main', members: [], members_truncated: true })
    expect(issueSubjectState(subject, [], loaded([grouped]), { state: 'limited' })).toEqual({ state: 'found', issues: [grouped] })
  })
})
