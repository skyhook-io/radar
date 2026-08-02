import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import type { CapacityQuantityObservation } from "@skyhook-io/k8s-ui";
import {
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
