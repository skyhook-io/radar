import { describe, expect, it } from "vitest";
import { ApiError, backoffRefetchInterval, capabilitiesRefetchInterval, namespacesRefetchInterval } from "./client";

describe("backoffRefetchInterval", () => {
  it("doubles from 5s to a 60s cap across consecutive failed answers, and resets on a good one", () => {
    const query = {};
    const seq = (answers: Array<[number, boolean]>) => answers.map(([at, failing]) => backoffRefetchInterval(query, at, failing, false));
    expect(seq([[1, true], [2, true], [3, true], [4, true], [5, true], [6, true]])).toEqual([5000, 10_000, 20_000, 40_000, 60_000, 60_000]);
    expect(seq([[7, false], [8, true]])).toEqual([false, 5000]);
  });

  it("counts an answer once, however often React Query asks for the interval", () => {
    const query = {};
    expect([1, 1, 1].map((at) => backoffRefetchInterval(query, at, true, false))).toEqual([5000, 5000, 5000]);
  });

  it("keeps a separate count per query", () => {
    const a = {};
    const b = {};
    backoffRefetchInterval(a, 1, true, false);
    backoffRefetchInterval(a, 2, true, false);
    expect(backoffRefetchInterval(b, 1, true, false)).toBe(5000);
  });
});

const caps = (dataUpdatedAt: number, checksIncomplete?: boolean) => ({ state: { dataUpdatedAt, data: { checksIncomplete } } });

describe("capabilitiesRefetchInterval", () => {
  it("backs off while access checks keep erroring, so a struggling authorizer isn't asked 12x as often", () => {
    const query = caps(1, true);
    expect(capabilitiesRefetchInterval(query, 60_000)).toBe(5000);
    query.state.dataUpdatedAt = 2;
    expect(capabilitiesRefetchInterval(query, 60_000)).toBe(10_000);
  });

  it("keeps the settled cadence once every check answered, and for older Radars that never send the flag", () => {
    expect(capabilitiesRefetchInterval(caps(1, false), 60_000)).toBe(60_000);
    expect(capabilitiesRefetchInterval(caps(1), false)).toBe(false);
  });
});

const ns = (status: string, error: unknown, errorUpdatedAt: number, dataUpdatedAt = 0) => ({ state: { status, error, errorUpdatedAt, dataUpdatedAt } });

describe("namespacesRefetchInterval", () => {
  it("asks again with back-off while Radar couldn't read the list", () => {
    const query = ns("error", new ApiError("Radar couldn't list namespaces", 500), 1);
    expect(namespacesRefetchInterval(query)).toBe(5000);
    query.state.errorUpdatedAt = 2;
    query.state.error = new ApiError("permission check failed", 503);
    expect(namespacesRefetchInterval(query)).toBe(10_000);
  });

  it("doesn't poll a denial or a list it has", () => {
    expect(namespacesRefetchInterval(ns("error", new ApiError("insufficient permissions", 403), 1))).toBe(false);
    expect(namespacesRefetchInterval(ns("success", null, 0, 1))).toBe(false);
  });
});
