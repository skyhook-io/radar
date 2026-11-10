import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { DiagnoseError, investigationRefusal } from "../../api/diagnose";
import { DiagnoseCustomizationProvider } from "../../context/DiagnoseCustomization";
import {
  InvestigationFailureNotice,
  InvestigationStartErrorAlert,
  InvestigationStatusCheckNotice,
} from "./InvestigationView";

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

const withHost = (node: React.ReactNode) =>
  renderToString(
    <DiagnoseCustomizationProvider
      value={undefined}
      renderRefusalAction={(r) => (
        <a href="/plans">{`Upgrade (${r.reason})`}</a>
      )}
    >
      {node}
    </DiagnoseCustomizationProvider>,
  );

describe("InvestigationStatusCheckNotice (Findings)", () => {
  it("says a refused status check couldn't run, with the host's action", () => {
    const html = withHost(
      <InvestigationStatusCheckNotice
        message={quota.message}
        uncertainOnly={false}
        refusal={investigationRefusal(quota)}
        onCheck={() => {}}
        disabled={false}
      />,
    );
    expect(html).toContain("Couldn&#x27;t check status:");
    expect(html).not.toContain("Verification did not complete");
    expect(html).toContain("Upgrade (allowance_used_free)");
  });

  it("keeps the verification wording, and no action, for a check that ran and failed", () => {
    const html = withHost(
      <InvestigationStatusCheckNotice
        message="The agent stopped before checking."
        uncertainOnly={false}
        refusal={null}
        onCheck={() => {}}
        disabled={false}
      />,
    );
    expect(html).toContain("Verification did not complete:");
    expect(html).not.toContain("Upgrade");
  });
});

describe("InvestigationFailureNotice (Activity)", () => {
  it("keeps a status warning first and shows a newer refused follow-up, with its action, beside it", () => {
    const html = withHost(
      <InvestigationFailureNotice
        message="Radar couldn't confirm whether the apply request completed."
        refusal={null}
        statusCheck={{
          label: "Check current status",
          onCheck: () => {},
          disabled: false,
        }}
        followUp={{
          message: quota.message,
          refusal: investigationRefusal(quota),
        }}
      />,
    );
    expect(
      html.indexOf("couldn&#x27;t confirm whether the apply"),
    ).toBeLessThan(html.indexOf("this month&#x27;s investigations"));
    expect(html).toContain("Check current status");
    expect(html).toContain("Upgrade (allowance_used_free)");
  });
});
