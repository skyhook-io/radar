import type React from "react";
import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { NetworkPolicyCoverageCard } from "./NetworkPolicyCoverageCard";

// React separates adjacent text nodes with comment markers. Strip them so an
// assertion matches the text a reader sees instead of a style-attribute value
// that happens to contain the same characters.
const render = (element: React.ReactElement) =>
  renderToString(element).replaceAll("<!-- -->", "");

// The card's own accent turns red at low coverage, so an assertion has to look
// at the striped staged segment rather than at the whole card.
const stagedSegmentClass = (html: string) => {
  const match = html.match(/class="([^"]*)"[^>]*repeating-linear-gradient/);
  if (!match) throw new Error("no staged segment rendered");
  return match[1];
};

describe("NetworkPolicyCoverageCard", () => {
  it("shows enforced and staged preview coverage separately", () => {
    const html = render(
      <NetworkPolicyCoverageCard
        data={{
          totalPolicies: 6,
          stagedPolicies: 2,
          coveredWorkloads: 3,
          coveredWorkloadsIfStaged: 5,
          totalWorkloads: 10,
        }}
        onNavigate={() => {}}
      />,
    );

    expect(html).toContain("30%");
    expect(html).toContain("(50% if staged applied)");
    expect(html).toContain("Covered workloads");
    expect(html).toContain("Covered if staged");
    expect(html).toContain("Uncovered workloads");
    expect(html).toContain("Uncovered if staged");
    // The delta segment's tone carries the direction: amber for coverage the
    // staged set would add. Its wording lives in a Tooltip portal, which server
    // rendering does not emit, so the class is what an assertion can reach.
    expect(stagedSegmentClass(html)).toContain("text-yellow-500");
  });

  it("shows coverage going down when a staged policy stages a deletion", () => {
    const html = render(
      <NetworkPolicyCoverageCard
        data={{
          totalPolicies: 6,
          stagedPolicies: 1,
          coveredWorkloads: 8,
          coveredWorkloadsIfStaged: 5,
          totalWorkloads: 10,
        }}
        onNavigate={() => {}}
      />,
    );

    expect(html).toContain("80%");
    expect(html).toContain("(50% if staged applied)");
    // Red, not amber: this staged set takes coverage away.
    expect(stagedSegmentClass(html)).toContain("text-red-500");
    // The projected figure must not be clamped up to today's coverage.
    expect(html).toContain("5/10");
  });

  it("keeps the original enforced-only presentation without staged policies", () => {
    const html = render(
      <NetworkPolicyCoverageCard
        data={{ totalPolicies: 2, coveredWorkloads: 2, totalWorkloads: 4 }}
        onNavigate={() => {}}
      />,
    );

    expect(html).toContain("50%");
    expect(html).not.toContain("if staged");
    expect(html).not.toContain("repeating-linear-gradient");
  });

  it("says which workloads the totals leave out", () => {
    const html = render(
      <NetworkPolicyCoverageCard
        data={{
          totalPolicies: 1,
          coveredWorkloads: 1,
          totalWorkloads: 3,
          ignoredNamespaceWorkloads: 6,
          hostNetworkWorkloads: 1,
        }}
        onNavigate={() => {}}
      />,
    );
    expect(html).toContain("7 not counted");

    const none = render(
      <NetworkPolicyCoverageCard
        data={{ totalPolicies: 1, coveredWorkloads: 1, totalWorkloads: 3 }}
        onNavigate={() => {}}
      />,
    );
    expect(none).not.toContain("not counted");
  });

  it("does not show 0% coverage when no workloads are counted", () => {
    const html = render(
      <NetworkPolicyCoverageCard
        data={{ totalPolicies: 2, coveredWorkloads: 0, totalWorkloads: 0, ignoredNamespaceWorkloads: 3 }}
        onNavigate={() => {}}
      />,
    );
    expect(html).toContain("No workloads counted");
    expect(html).toContain("3 not counted");
    expect(html).not.toContain("0%");
    expect(html).not.toContain("text-red-500");
  });
});
