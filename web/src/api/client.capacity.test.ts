import { describe, expect, it } from "vitest";
import {
  ApiError,
  capacityRefetchInterval,
  isCapacityCursorInvalidError,
  shouldRetryCapacityQuery,
} from "./client";

describe("isCapacityCursorInvalidError", () => {
  it("uses the structured capacity error code", () => {
    expect(
      isCapacityCursorInvalidError(
        new ApiError("cursor expired", 400, {
          error_code: "capacity_cursor_invalid",
        }),
      ),
    ).toBe(true);
  });

  it("does not infer cursor failures from status or message text", () => {
    expect(
      isCapacityCursorInvalidError(new ApiError("cursor expired", 400)),
    ).toBe(false);
    expect(
      isCapacityCursorInvalidError(
        new ApiError("unrelated", 400, { code: "another_error" }),
      ),
    ).toBe(false);
  });
});

describe("shouldRetryCapacityQuery", () => {
  it("does not retry stable client or authorization failures", () => {
    expect(
      shouldRetryCapacityQuery(0, new ApiError("invalid since", 400)),
    ).toBe(false);
    expect(shouldRetryCapacityQuery(0, new ApiError("forbidden", 403))).toBe(
      false,
    );
    expect(shouldRetryCapacityQuery(0, new ApiError("not found", 404))).toBe(
      false,
    );
  });

  it("bounds retries for transient failures", () => {
    expect(shouldRetryCapacityQuery(0, new ApiError("unavailable", 503))).toBe(
      true,
    );
    expect(shouldRetryCapacityQuery(3, new ApiError("unavailable", 503))).toBe(
      false,
    );
  });
});

describe("capacityRefetchInterval", () => {
  const withData = (data?: { state?: string }) => ({ state: { data } });

  it("suspends polling while the user holds a page cursor", () => {
    // A background refetch on page 2 races membership changes and yanks the
    // user back to page 1.
    expect(
      capacityRefetchInterval(undefined, true, "cursor-abc")(withData()),
    ).toBe(false);
  });

  it("polls fast while the server reports syncing", () => {
    expect(
      capacityRefetchInterval(undefined, true)(withData({ state: "syncing" })),
    ).toBe(2_500);
  });

  it("arms the normal cadence for a settled, enabled query", () => {
    expect(
      capacityRefetchInterval(
        undefined,
        true,
      )(withData({ state: "available" })),
    ).toBe(30_000);
    expect(
      capacityRefetchInterval(
        { refetchInterval: 5_000 },
        true,
      )(withData({ state: "available" })),
    ).toBe(5_000);
  });

  it("does not poll a disabled query, even while syncing", () => {
    expect(
      capacityRefetchInterval(undefined, false)(withData({ state: "syncing" })),
    ).toBe(false);
  });
});
