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
    expect(html).toContain("5/10 if staged");
    expect(html).toContain("Uncovered workloads");
    expect(html).toContain("5 if staged");
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
});

it("keeps unknown selection separate from uncovered and marks coverage as a lower bound", () => {
 const html=render(<NetworkPolicyCoverageCard data={{totalPolicies:2,coveredWorkloads:1,unknownWorkloads:2,totalWorkloads:4}} onNavigate={()=>{}} />)
 expect(html).toContain("≥25%")
 expect(html).toMatch(/Coverage unknown[\s\S]*?2/)
 expect(html).toMatch(/Uncovered workloads[\s\S]*?1/)
})

it("does not call unreadable policy scopes unconfigured", () => {
 const html=render(<NetworkPolicyCoverageCard data={{totalPolicies:0,coveredWorkloads:0,unknownWorkloads:4,totalWorkloads:4}} onNavigate={()=>{}} />)
 expect(html).not.toContain("No network policies configured")
 expect(html).toContain("Coverage unknown")
 expect(html).toContain("≥0%")
})
it("keeps staged uncertainty in a single row and preserves the Home grid height",()=>{
 const html=render(<NetworkPolicyCoverageCard data={{totalPolicies:2,stagedPolicies:1,coveredWorkloads:1,unknownWorkloads:8,coveredWorkloadsIfStaged:0,unknownWorkloadsIfStaged:9,totalWorkloads:10}} onNavigate={()=>{}}/>)
 expect(html).toContain("h-[260px]")
 expect(html).toContain("(9 if staged)")
 expect(html).toContain("(0/10 if staged)")
 expect(html).not.toContain("Covered if staged")
 expect(html).not.toContain("Uncovered if staged")
 expect(html).not.toContain("text-red-500")
})
