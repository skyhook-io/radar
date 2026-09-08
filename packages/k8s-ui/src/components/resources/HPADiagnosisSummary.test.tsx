import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import {
  HPADiagnosisSummary,
  hpaBadgeSeverity,
  hpaReasonGroups,
  isHPAReasonRedundant,
} from "./HPADiagnosisSummary";
import type { HPADiagnosisState, HPADiagnosisView } from "../../types";

// React marks interpolation boundaries with empty comments; strip them so
// assertions can quote the sentence a reader actually sees.
const render = (node: Parameters<typeof renderToString>[0]) =>
  renderToString(node).replace(/<!-- -->/g, "");

const maxed: HPADiagnosisView = {
  state: "limited_max",
  summary: "HPA wants more replicas but is capped at maxReplicas=10",
  bounds: { min: 1, max: 10, current: 10, desired: 10 },
  reasons: [
    {
      id: "limited_max",
      message: "HPA is capped at maxReplicas=10",
      detail:
        "the desired replica count is more than the maximum replica count",
      conditionType: "ScalingLimited",
      conditionReason: "TooManyReplicas",
    },
    {
      id: "missing_current_metric",
      message: "HPA is missing current metric values",
      detail: "cpu",
    },
  ],
};

describe("hpaBadgeSeverity", () => {
  const expected: Record<HPADiagnosisState, string> = {
    ok: "success",
    scaling_up: "warning",
    scaling_down: "warning",
    limited_max: "warning",
    metrics_incomplete: "warning",
    limited_min: "info",
    scaled_to_zero: "info",
    disabled: "info",
    pinned: "info",
    stabilized: "info",
    metrics_unavailable: "error",
    unable_to_scale: "error",
    stale: "neutral",
    unknown: "neutral",
  };

  it.each(Object.entries(expected))(
    "maps %s to the %s badge",
    (state, severity) => {
      expect(hpaBadgeSeverity(state as HPADiagnosisState)).toBe(severity);
    },
  );
});

describe("isHPAReasonRedundant", () => {
  it("treats the reason that named the state as a restatement", () => {
    expect(isHPAReasonRedundant(maxed, maxed.reasons![0])).toBe(true);
  });

  it("treats a reason whose message repeats the summary as a restatement", () => {
    const echo = { id: "other", message: maxed.summary };
    expect(isHPAReasonRedundant(maxed, echo)).toBe(true);
  });

  it("keeps a reason that adds something the headline does not", () => {
    expect(isHPAReasonRedundant(maxed, maxed.reasons![1])).toBe(false);
  });

  it("splits reasons into restatements and additions", () => {
    const groups = hpaReasonGroups(maxed);
    expect(groups.redundant.map((reason) => reason.id)).toEqual([
      "limited_max",
    ]);
    expect(groups.additional.map((reason) => reason.id)).toEqual([
      "missing_current_metric",
    ]);
  });
});

describe("HPADiagnosisSummary", () => {
  it("shows the detail variant evidence without repeating the headline", () => {
    const html = render(
      <HPADiagnosisSummary diagnosis={maxed} variant="detail" />,
    );

    expect(html).toContain(
      "HPA wants more replicas but is capped at maxReplicas=10",
    );
    expect(html).toContain("10/10 replicas, bounds 1-10");
    expect(html).toContain("Maxed");
    expect(html).toContain("Evidence");
    expect(html).toContain("limited max");
    expect(html).toContain("ScalingLimited");
    expect(html).toContain("TooManyReplicas");
    expect(html).not.toContain("HPA is capped at maxReplicas=10");
    expect(html).toContain("HPA is missing current metric values");
  });

  it("attributes the controller condition in the inline variant and lists the rest", () => {
    const html = render(
      <HPADiagnosisSummary
        diagnosis={maxed}
        variant="inline"
        header={
          <span data-testid="host-header">HorizontalPodAutoscaler api-hpa</span>
        }
      />,
    );

    expect(html).toContain("HorizontalPodAutoscaler api-hpa");
    expect(html).toContain("Maxed");
    expect(html).toContain("10/10 replicas · bounds 1-10");
    expect(html).toContain(
      "HPA wants more replicas but is capped at maxReplicas=10",
    );
    expect(html).not.toContain("HPA is capped at maxReplicas=10");
    expect(html).toContain(
      "Kubernetes ScalingLimited · TooManyReplicas: “the desired replica count is more than the maximum replica count”",
    );
    expect(html).toContain("HPA is missing current metric values");
    expect(html).toContain("· cpu");
  });

  it("omits the bounds line when the producer withheld bounds", () => {
    const html = render(
      <HPADiagnosisSummary
        diagnosis={{ state: "ok", summary: "HPA is tracking its target" }}
        variant="inline"
      />,
    );

    expect(html).toContain("HPA is tracking its target");
    expect(html).not.toContain("replicas");
  });
});
