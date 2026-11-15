import { describe, expect, it } from "vitest";
import { capabilitiesRefetchInterval } from "./client";

const query = (checksIncomplete?: boolean) => ({ state: { data: checksIncomplete === undefined ? undefined : { checksIncomplete } } });

describe("capabilitiesRefetchInterval", () => {
  it("asks again in 5s while an access check errored, so a timed-out check doesn't hide controls for a minute", () => {
    expect(capabilitiesRefetchInterval(query(true), 60_000)).toBe(5000);
    expect(capabilitiesRefetchInterval(query(true), false)).toBe(5000);
  });

  it("keeps the settled cadence once every check answered, and for older Radars that never send the flag", () => {
    expect(capabilitiesRefetchInterval(query(false), 60_000)).toBe(60_000);
    expect(capabilitiesRefetchInterval(query(), false)).toBe(false);
  });
});
