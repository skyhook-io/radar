import { describe, expect, it } from "vitest";
import {
  costSourceApplyLabel,
  prometheusHeadersFromRows,
  pendingIntegrationSections,
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

describe("Integration settings state", () => {
  it.each(['prometheus', 'cost', 'argocd'] as const)("offers discard for a %s draft", (section) => {
    expect(pendingIntegrationSections({ prometheus: false, cost: false, argocd: false, [section]: true })).toEqual([section]);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: false,
        configDirty: false,
        integrationDirty: true,
        hasSaveMessage: false,
      }),
    ).toBe(true);
  });

  it("offers review from other sections and retains the close guard", () => {
    expect(pendingIntegrationSections({ prometheus: true, cost: true, argocd: true })).toEqual(['prometheus', 'cost', 'argocd']);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: true,
        configDirty: false,
        integrationDirty: true,
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
