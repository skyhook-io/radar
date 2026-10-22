import { describe, expect, it } from "vitest";
import {
  ApiError,
  gitopsInsightsRefetchInterval,
  gitopsTreeRefetchInterval,
} from "./client";

type TreeQuery = Parameters<typeof gitopsTreeRefetchInterval>[0];
type InsightQuery = Parameters<typeof gitopsInsightsRefetchInterval>[0];
const outage = new TypeError("Failed to fetch");
const tree = (status: string, data: TreeQuery["state"]["data"], error: unknown = outage): TreeQuery => ({
  state: { status, data, error: status === "error" ? error : null },
});
const insight = (status: string, data: InsightQuery["state"]["data"], error: unknown = outage): InsightQuery => ({
  state: { status, data, error: status === "error" ? error : null },
});
const gone = new ApiError("Application not found", 404);

describe("gitopsTreeRefetchInterval", () => {
  it("asks every 2s while Radar connects to argocd-server", () => {
    expect(gitopsTreeRefetchInterval(tree("success", { healthApiPending: true }))).toBe(2000);
  });

  it("asks every 30s while Radar retries, no faster than the server re-checks", () => {
    expect(gitopsTreeRefetchInterval(tree("success", { healthApiRetrying: true }))).toBe(30_000);
  });

  it("stops once health is in", () => {
    expect(gitopsTreeRefetchInterval(tree("success", {}))).toBe(false);
  });

  it("keeps asking slowly after a failed fetch, so a brief outage doesn't strand the page", () => {
    expect(gitopsTreeRefetchInterval(tree("error", { healthApiRetrying: true }))).toBe(30_000);
    expect(gitopsTreeRefetchInterval(tree("error", { healthApiPending: true }))).toBe(30_000);
  });

  it("keeps asking through a long Radar or proxy outage", () => {
    expect(gitopsTreeRefetchInterval(tree("error", { healthApiRetrying: true }, new ApiError("Bad Gateway", 502)))).toBe(30_000);
  });

  it("stops once Radar answers that the Application is gone or can't be read", () => {
    expect(gitopsTreeRefetchInterval(tree("error", { healthApiRetrying: true }, gone))).toBe(false);
    expect(gitopsTreeRefetchInterval(tree("error", { healthApiPending: true }, new ApiError("Forbidden", 403)))).toBe(false);
  });

  it("does not start polling after a failed fetch of a settled page", () => {
    expect(gitopsTreeRefetchInterval(tree("error", {}))).toBe(false);
  });
});

describe("gitopsInsightsRefetchInterval", () => {
  it("asks every 2s while a sync runs", () => {
    expect(
      gitopsInsightsRefetchInterval(
        insight("success", { summary: { operationPhase: "Running" } }),
      ),
    ).toBe(2000);
  });

  it("asks every 2s while Radar connects", () => {
    expect(
      gitopsInsightsRefetchInterval(
        insight("success", { capabilities: { argoConnecting: true } }),
      ),
    ).toBe(2000);
  });

  it("asks every 30s while Radar retries", () => {
    expect(
      gitopsInsightsRefetchInterval(
        insight("success", { capabilities: { argoRetrying: true } }),
      ),
    ).toBe(30_000);
    expect(
      gitopsInsightsRefetchInterval(
        insight("success", { summary: { resourceHealthApiRetrying: true } }),
      ),
    ).toBe(30_000);
  });

  it("keeps asking slowly after a failed fetch, including during a running sync", () => {
    expect(
      gitopsInsightsRefetchInterval(insight("error", { capabilities: { argoRetrying: true } })),
    ).toBe(30_000);
    expect(
      gitopsInsightsRefetchInterval(insight("error", { summary: { operationPhase: "Running" } })),
    ).toBe(30_000);
  });

  it("stops once Radar answers that the Application is gone", () => {
    expect(
      gitopsInsightsRefetchInterval(insight("error", { capabilities: { argoRetrying: true } }, gone)),
    ).toBe(false);
  });

  it("does not poll a settled page", () => {
    expect(gitopsInsightsRefetchInterval(insight("success", {}))).toBe(false);
  });
});
