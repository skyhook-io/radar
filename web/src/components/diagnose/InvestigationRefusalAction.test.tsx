import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { DiagnoseError, investigationRefusal } from "../../api/diagnose";
import { DiagnoseCustomizationProvider } from "../../context/DiagnoseCustomization";
import { InvestigationStartErrorAlert } from "./InvestigationView";

const quota = new DiagnoseError(
  402,
  "Your organization has used this month's investigations.",
  {
    code: "ai_quota_exhausted",
    reason: "allowance_used_free",
    action: "upgrade",
  },
);

describe("investigationRefusal", () => {
  it("carries a host refusal's code, reason and action with its sentence", () => {
    expect(investigationRefusal(quota)).toEqual({
      status: 402,
      message: "Your organization has used this month's investigations.",
      code: "ai_quota_exhausted",
      reason: "allowance_used_free",
      action: "upgrade",
    });
  });

  it("is null for Radar's own errors and for anything that isn't a DiagnoseError", () => {
    expect(
      investigationRefusal(
        new DiagnoseError(409, "Too many investigations running."),
      ),
    ).toBeNull();
    expect(investigationRefusal(new Error("network down"))).toBeNull();
    expect(investigationRefusal(undefined)).toBeNull();
  });
});

describe("InvestigationStartErrorAlert", () => {
  const alert = (refusal = investigationRefusal(quota)) => (
    <InvestigationStartErrorAlert
      error={quota.message}
      refusal={refusal}
      onDismiss={() => {}}
    />
  );

  it("shows the host's action beside the refusal", () => {
    const html = renderToString(
      <DiagnoseCustomizationProvider
        value={undefined}
        renderRefusalAction={(r) => (
          <a href="/plans">{`Upgrade (${r.reason})`}</a>
        )}
      >
        {alert()}
      </DiagnoseCustomizationProvider>,
    );
    expect(html).toContain("this month&#x27;s investigations");
    expect(html).toContain("Upgrade (allowance_used_free)");
  });

  it("shows only the sentence when the host offers no action", () => {
    const html = renderToString(alert());
    expect(html).toContain("this month&#x27;s investigations");
    expect(html).not.toContain("Upgrade");
  });

  it("asks the host for nothing when the error carries no refusal", () => {
    let asked = false;
    renderToString(
      <DiagnoseCustomizationProvider
        value={undefined}
        renderRefusalAction={() => {
          asked = true;
          return null;
        }}
      >
        {alert(null)}
      </DiagnoseCustomizationProvider>,
    );
    expect(asked).toBe(false);
  });
});
