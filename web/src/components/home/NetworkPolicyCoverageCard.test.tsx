import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { NetworkPolicyCoverageCard } from "./NetworkPolicyCoverageCard";

describe("NetworkPolicyCoverageCard", () => {
  it("shows enforced and staged preview coverage separately", () => {
    const html = renderToString(
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
    expect(html).toContain("50");
    expect(html).toContain("% if staged applied)");
    expect(html).toContain("Covered workloads");
    expect(html).toContain("Covered if staged");
    expect(html).toContain("Uncovered workloads");
    expect(html).toContain("Uncovered if staged");
    expect(html).toContain("repeating-linear-gradient");
    expect(html).toContain(
      "2 additional workloads if staged policies are applied",
    );
  });

  it("keeps the original enforced-only presentation without staged policies", () => {
    const html = renderToString(
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
