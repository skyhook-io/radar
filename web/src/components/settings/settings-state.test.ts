import { describe, expect, it } from "vitest";
import {
  costSourceApplyLabel,
  shouldOfferCostReview,
  shouldShowSettingsFooter,
} from "./settings-state";

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
