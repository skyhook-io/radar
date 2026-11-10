import { describe, expect, it } from "vitest";
import {
  costSourceApplyLabel,
  prometheusHeadersFromRows,
  pendingSections,
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
    expect(pendingSections({ prometheus: false, cost: false, argocd: false, ai: false, [section]: true })).toEqual([section]);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: false,
        configDirty: false,
        integrationDirty: true,
        aiDirtyElsewhere: false,
        hasSaveMessage: false,
      }),
    ).toBe(true);
  });

  it("offers review from other sections and retains the close guard", () => {
    expect(pendingSections({ prometheus: true, cost: true, argocd: true, ai: true })).toEqual(['prometheus', 'cost', 'argocd', 'ai']);
    expect(
      shouldShowSettingsFooter({
        canEditConfig: true,
        confirmingClose: true,
        configDirty: false,
        integrationDirty: true,
        aiDirtyElsewhere: false,
        hasSaveMessage: false,
      }),
    ).toBe(true);
  });

  it("shows an AI draft to users without owner access, but not their startup or integration drafts", () => {
    const base = { canEditConfig: false, confirmingClose: false, configDirty: true, integrationDirty: true, aiDirtyElsewhere: false, hasSaveMessage: true };
    expect(shouldShowSettingsFooter(base)).toBe(false);
    expect(shouldShowSettingsFooter({ ...base, aiDirtyElsewhere: true })).toBe(true);
    expect(shouldShowSettingsFooter({ ...base, confirmingClose: true })).toBe(true);
  });

  it("only claims to test sources that the backend probes", () => {
    expect(costSourceApplyLabel("auto")).toBe("Test & apply source");
    expect(costSourceApplyLabel("kubecost")).toBe("Test & apply source");
    expect(costSourceApplyLabel("prometheus")).toBe("Apply source");
  });
});
