import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import type {
  CapacityActivityState,
  CapacityQuantityObservation,
} from "@skyhook-io/k8s-ui";
import {
  DeniedCapacityState,
  ActivityStateBadge,
  CertaintyGlyph,
  InventoryQuantityCell,
  QuantityInline,
  certaintyGlyph,
  managerStatusRank,
} from "./shared";

function observation(
  certainty: CapacityQuantityObservation["certainty"],
  resources: Record<string, string> = { cpu: "2" },
): CapacityQuantityObservation {
  return {
    resources,
    certainty,
    sources: ["test"],
    asOf: "2026-07-15T00:00:00Z",
    granularity: "aggregate",
  };
}

describe("certainty glyph mapping", () => {
  // The =/≥/≤/? vocabulary is the feature's trust contract — pin every arm.
  it("maps each certainty to its glyph", () => {
    expect(certaintyGlyph("exact")).toBe("=");
    expect(certaintyGlyph("lower_bound")).toBe("≥");
    expect(certaintyGlyph("upper_bound")).toBe("≤");
    expect(certaintyGlyph("unknown")).toBe("?");
  });

  it("renders a keyboard-reachable trigger", () => {
    const html = renderToString(<CertaintyGlyph certainty="lower_bound" />);
    expect(html).toContain('tabindex="0"');
    expect(html).toContain("≥");
  });
});

describe("QuantityInline certainty", () => {
  it("renders exact values without a hedging glyph", () => {
    const html = renderToString(
      <QuantityInline observation={observation("exact")} empty="0" />,
    );
    expect(html).toContain("2 cores");
    expect(html).not.toContain("≥");
    expect(html).not.toContain("?");
  });

  it("hedges non-exact values with their glyph", () => {
    const html = renderToString(
      <QuantityInline observation={observation("lower_bound")} empty="0" />,
    );
    expect(html).toContain("≥");
  });

  it("never renders an empty unknown observation as the zero label", () => {
    const html = renderToString(
      <QuantityInline observation={observation("unknown", {})} empty="0" />,
    );
    expect(html).toContain("Unknown");
    expect(html).not.toContain(">0<");
  });

  it("hedges an empty lower-bound observation instead of a bare zero", () => {
    const html = renderToString(
      <QuantityInline observation={observation("lower_bound", {})} empty="0" />,
    );
    expect(html).toContain("≥");
  });
});

describe("observed zero vs unobserved source", () => {
  // "Not observed" is this page's RBAC-gap wording. A pool scaled to zero is a
  // measured zero and must not borrow it.
  it("renders an exact empty observation as 0, not the unobserved label", () => {
    const html = renderToString(
      <InventoryQuantityCell
        observation={observation("exact", {})}
        empty="Not observed"
      />,
    );
    expect(html).toContain(">0<");
    expect(html).not.toContain("Not observed");
  });

  it("renders an absent observation as the unobserved label", () => {
    const html = renderToString(
      <InventoryQuantityCell observation={undefined} empty="Not observed" />,
    );
    expect(html).toContain("Not observed");
    expect(html).not.toContain(">0<");
  });

  it("keeps an empty unknown observation as Unknown", () => {
    const html = renderToString(
      <InventoryQuantityCell
        observation={observation("unknown", {})}
        empty="Not observed"
      />,
    );
    expect(html).toContain("Unknown");
    expect(html).not.toContain(">0<");
  });

  it("keeps an empty lower-bound observation hedged, not zeroed", () => {
    const html = renderToString(
      <InventoryQuantityCell
        observation={observation("lower_bound", {})}
        empty="Not observed"
      />,
    );
    expect(html).toContain("Not observed");
    expect(html).toContain("≥");
  });

  it("applies the same rule to QuantityInline", () => {
    expect(
      renderToString(
        <QuantityInline observation={observation("exact", {})} empty="—" />,
      ),
    ).toContain(">0<");
    expect(
      renderToString(<QuantityInline observation={undefined} empty="—" />),
    ).toContain("—");
  });
});

describe("managerStatusRank", () => {
  it("ranks degraded worst and healthy best", () => {
    expect(managerStatusRank("degraded")).toBeLessThan(
      managerStatusRank("unknown"),
    );
    expect(managerStatusRank("unknown")).toBeLessThan(
      managerStatusRank("healthy"),
    );
  });

  it("ranks a status this build does not know as unknown", () => {
    // A newer server adding a rollup status must not produce NaN comparisons
    // that silently misrank a real degradation.
    expect(managerStatusRank("suspended_for_maintenance")).toBe(
      managerStatusRank("unknown"),
    );
  });
});

describe("ActivityStateBadge", () => {
  const render = (state: CapacityActivityState) =>
    renderToString(<ActivityStateBadge state={state} />);

  it("renders `ended` without claiming success or failure", () => {
    const html = render("ended");
    expect(html).toContain("ended");
    // Terminalized with no cause recorded — the success and error palettes
    // would both assert evidence Radar never saw.
    expect(html).not.toContain("emerald");
    expect(html).not.toContain("red-");
    expect(html).toContain("text-theme-text-secondary");
  });

  it("keeps completed and failed distinctly toned", () => {
    expect(render("completed")).toContain("emerald");
    expect(render("failed")).toContain("red-");
  });

  it("falls back to a neutral badge for a state this build does not know", () => {
    const html = render("superseded" as CapacityActivityState);
    expect(html).toContain("superseded");
    expect(html).toContain("text-theme-text-secondary");
  });
});

describe("DeniedCapacityState", () => {
  it("converts the 403 into a next step: fallback guidance plus the exact grant", () => {
    const html = renderToString(
      <DeniedCapacityState detail="no access to NodePools (cluster-scoped resource requires explicit RBAC)" />,
    );
    expect(html).toContain("Capacity access denied");
    expect(html).toContain("Pod drawer and Issues");
    expect(html).toContain("Permissions to request");
    expect(html).toContain("radar-capacity-viewer");
    expect(html).toContain("nodepools");
  });
});
