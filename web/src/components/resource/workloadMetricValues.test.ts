import { describe, expect, it } from "vitest";
import { latestWorkloadValue, workloadPodValues } from "./workloadMetricValues";

describe("workload latest samples", () => {
  it("keeps zero but does not walk back across a trailing gap", () => {
    expect(
      latestWorkloadValue(
        { labels: {}, dataPoints: [{ timestamp: 100, value: 0 }] },
        100,
        60,
      ),
    ).toBe(0);
    expect(
      latestWorkloadValue(
        {
          labels: {},
          dataPoints: [
            { timestamp: 90, value: 5 },
            { timestamp: 100, value: null },
          ],
        },
        100,
        60,
      ),
    ).toBeUndefined();
  });
  it("does not display historical samples as current", () => {
    expect(
      latestWorkloadValue(
        { labels: {}, dataPoints: [{ timestamp: 100, value: 9 }] },
        1000,
        60,
      ),
    ).toBeUndefined();
  });
  it("preserves unavailable pods instead of silently dropping them", () => {
    const values = workloadPodValues(
      [
        {
          labels: { pod: "api-0" },
          dataPoints: [{ timestamp: 100, value: null }],
        },
      ],
      100,
      60,
    );
    expect(values.has("api-0")).toBe(true);
    expect(values.get("api-0")).toBeUndefined();
  });
  it("does not treat a large gap between samples as the evaluation interval", () => {
    expect(
      latestWorkloadValue(
        {
          labels: {},
          dataPoints: [
            { timestamp: 1, value: 1 },
            { timestamp: 1000, value: 2 },
          ],
        },
        1500,
        60,
      ),
    ).toBeUndefined();
  });
});
