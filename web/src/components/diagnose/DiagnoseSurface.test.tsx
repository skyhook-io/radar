import { describe, expect, it } from "vitest";
import { canCopyRunLink, canStartNewInvestigation } from "./DiagnoseSurface";
import {
  canContinueInvestigation,
  canStopInvestigation,
} from "./InvestigationView";
import type { RunSummary } from "../../api/diagnose";

// The "new investigation" button dispatches an agent and spends the user's own
// tokens, so every one of these clauses is load-bearing rather than cosmetic.
// Each case below is a way it misfired before the gate existed.
function run(status: RunSummary["status"]): RunSummary {
  return {
    id: "r1",
    kind: "Deployment",
    namespace: "prod",
    name: "payments",
    context: "prod-cluster",
    status,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
}

describe("canStartNewInvestigation", () => {
  it("offers a new investigation on a finished run", () => {
    expect(canStartNewInvestigation("investigation", run("done"), false)).toBe(
      true,
    );
  });

  it("stays hidden on the investigations list", () => {
    // goHome() leaves activeRunId set, so the header still has a run to read.
    // Without the view check the click starts an agent on a resource the user
    // navigated away from, over a list of unrelated investigations.
    expect(canStartNewInvestigation("home", run("done"), false)).toBe(false);
  });

  it("stays hidden while a turn is in flight", () => {
    // A start would be handed back the live run, so the button does nothing.
    expect(
      canStartNewInvestigation("investigation", run("running"), false),
    ).toBe(false);
  });

  it("stays hidden while a stopped turn is draining", () => {
    expect(
      canStartNewInvestigation("investigation", run("stopping"), false),
    ).toBe(false);
  });

  it("offers a separate human investigation while an automatic run is in flight", () => {
    expect(
      canStartNewInvestigation(
        "investigation",
        { ...run("running"), trigger: "background" },
        false,
      ),
    ).toBe(true);
  });

  it("stays hidden on a stale run", () => {
    // The body offers "Re-run on current cluster" WITH the context-changed
    // warning. A bare + carries none of it, at a resource that may not exist in
    // the context it would now run against.
    expect(canStartNewInvestigation("investigation", run("stale"), false)).toBe(
      false,
    );
  });

  it("stays hidden while consent is pending", () => {
    expect(canStartNewInvestigation("investigation", run("done"), true)).toBe(
      false,
    );
  });

  it("stays hidden with no focused run", () => {
    expect(canStartNewInvestigation("investigation", null, false)).toBe(false);
  });

  it("offers one on an errored or stopped run", () => {
    // Those are the runs a person most wants to start over from.
    expect(canStartNewInvestigation("investigation", run("error"), false)).toBe(
      true,
    );
    expect(
      canStartNewInvestigation("investigation", run("stopped"), false),
    ).toBe(true);
  });
});

describe("canStopInvestigation", () => {
  it("lets the terminal transcript outrank a lagging running summary", () => {
    expect(canStopInvestigation(run("running"), false, false, "done")).toBe(
      false,
    );
    expect(canStopInvestigation(run("running"), false, false, "error")).toBe(
      false,
    );
  });

  it("keeps Stop available for an active human turn", () => {
    expect(canStopInvestigation(run("running"), true, false, "running")).toBe(
      true,
    );
  });

  it("does not offer another Stop while the server drains the turn", () => {
    expect(canStopInvestigation(run("stopping"), true, false, "running")).toBe(
      false,
    );
  });

  it("never lets a missing or automatic run expose Stop", () => {
    expect(canStopInvestigation(run("running"), true, true, "running")).toBe(
      false,
    );
    expect(
      canStopInvestigation(
        { ...run("running"), trigger: "background" },
        true,
        false,
        "running",
      ),
    ).toBe(false);
  });
});

describe("canContinueInvestigation", () => {
  it("lets a terminal transcript outrank only a lagging running human summary", () => {
    expect(
      canContinueInvestigation(
        { ...run("running"), canContinue: false },
        "done",
      ),
    ).toBe(true);
  });

  it("keeps genuinely read-only and sessionless investigations read-only", () => {
    expect(
      canContinueInvestigation(
        {
          ...run("running"),
          trigger: "background",
          canContinue: false,
        },
        "done",
      ),
    ).toBe(false);
    expect(
      canContinueInvestigation({ ...run("done"), canContinue: false }, "done"),
    ).toBe(false);
  });
});

describe("canCopyRunLink", () => {
  it("does not expose collaboration UI for an OSS run", () => {
    expect(canCopyRunLink(run("done"))).toBe(false);
  });

  it("exposes the copy action only for a canonical hosted URL", () => {
    expect(
      canCopyRunLink({
        ...run("done"),
        radarUrl: "/c/cluster-1?org=org-1&ai-run=r1",
      }),
    ).toBe(true);
  });

  it("treats an empty hosted URL as unavailable", () => {
    expect(canCopyRunLink({ ...run("done"), radarUrl: "" })).toBe(false);
  });
});
