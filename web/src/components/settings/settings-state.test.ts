import { describe, expect, it } from "vitest";
import {
  costSourceApplyLabel,
  prometheusHeadersFromRows,
  shouldOfferCostReview,
  shouldShowSettingsFooter,
} from "./settings-state";

describe("Prometheus header edits", () => {
  it("distinguishes unchanged, replacement and explicit clear", () => {
    expect(prometheusHeadersFromRows(null)).toBeUndefined();
    expect(prometheusHeadersFromRows([{ key: "", value: "" }])).toBeUndefined();
    expect(prometheusHeadersFromRows([])).toEqual({});
    expect(prometheusHeadersFromRows([{ key: " Authorization ", value: "Bearer new" }])).toEqual({ Authorization: "Bearer new" });
  });
  it("does not silently discard incomplete or duplicate rows", () => {
    expect(() => prometheusHeadersFromRows([{ key: "Authorization", value: "" }])).toThrow("both a name and value");
    expect(() => prometheusHeadersFromRows([{ key: "", value: "secret" }])).toThrow("both a name and value");
    expect(() => prometheusHeadersFromRows([{ key: "Authorization", value: "a" }, { key: "authorization", value: "b" }])).toThrow("more than once");
  });
  it("preserves valid names that coincide with object properties", () => {
    expect(JSON.stringify(prometheusHeadersFromRows([{ key: "__proto__", value: "value" }]))).toBe('{"__proto__":"value"}');
  });
});

describe("Cost settings state", () => {
  it("keeps source drafts inline while the Cost section is open", () => {
    expect(shouldOfferCostReview(true, "cost")).toBe(false);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: false,
        configDirty: false,
        costIntegrationDirty: true,
        section: "cost",
        hasSaveMessage: false,
      }),
    ).toBe(false);
  });

  it("offers review from other sections and retains the close guard", () => {
    expect(shouldOfferCostReview(true, "overview")).toBe(true);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: true,
        configDirty: false,
        costIntegrationDirty: true,
        section: "cost",
        hasSaveMessage: false,
      }),
    ).toBe(true);
  });

  it("only claims to test sources that the backend probes", () => {
    expect(costSourceApplyLabel("auto")).toBe("Test & apply source");
    expect(costSourceApplyLabel("kubecost")).toBe("Test & apply source");
    expect(costSourceApplyLabel("prometheus")).toBe("Apply source");
  });
});
