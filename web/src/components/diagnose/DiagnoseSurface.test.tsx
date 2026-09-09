import { describe, expect, it, vi } from "vitest";
import {
  DIAGNOSE_SURFACE_FRAME_CLASS,
  MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS,
  MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS,
  MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS,
  INVESTIGATION_HISTORY_MIN_WIDTH,
  MAXIMIZED_RUN_META_VISIBILITY_CLASS,
  canStartNewInvestigation,
  canCopyRunLink,
  investigationHeaderPresentation,
  openInvestigationEvidenceResource,
} from "./DiagnoseSurface";
import {
  canContinueInvestigation,
  canInvestigateFurther,
  canStopInvestigation,
} from "./investigationState";
import type { RunSummary } from "../../api/diagnose";

// The "new investigation" button dispatches an agent and spends the user's own
// tokens, so every one of these clauses is load-bearing rather than cosmetic.
// Each case below is a way it misfired before the gate existed.
function run(status: RunSummary["status"]): RunSummary {
  return {
    id: "r1",
    kind: "Deployment",
    group: "apps",
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

  it("stays hidden on a stale run", () => {
    // A closed session must not start a new investigation against a resource
    // that may not exist in the active cluster.
    expect(canStartNewInvestigation("investigation", run("stale"), false)).toBe(
      false,
    );
  });

  it("blocks fresh starts while a human turn stops, but allows a separate human run from an automatic one", () => {
    expect(
      canStartNewInvestigation("investigation", run("stopping"), false),
    ).toBe(false);
    expect(
      canStartNewInvestigation(
        "investigation",
        { ...run("running"), trigger: "background" },
        false,
      ),
    ).toBe(true);
  });

  it("stays hidden while consent is pending", () => {
    expect(canStartNewInvestigation("investigation", run("done"), true)).toBe(
      false,
    );
  });

  it("stays hidden with no focused run", () => {
    expect(canStartNewInvestigation("investigation", null, false)).toBe(false);
  });

  it("offers a restart for a completed cluster-scoped question", () => {
    expect(
      canStartNewInvestigation(
        "investigation",
        { ...run("done"), kind: "", name: "", question: "Why is it slow?" },
        false,
      ),
    ).toBe(true);
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

describe("investigation history navigation", () => {
  it("restores the docked surface before opening an evidence resource", () => {
    const events: string[] = [];
    const onOpenResource = vi.fn(() => events.push("open"));
    const setMaximized = vi.fn(() => events.push("dock"));
    const closeDiagnose = vi.fn(() => events.push("close"));
    const ref = {
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
    };

    openInvestigationEvidenceResource(
      ref,
      onOpenResource,
      setMaximized,
      closeDiagnose,
      false,
      "run-1",
    );

    expect(events).toEqual(["dock", "open"]);
    expect(setMaximized).toHaveBeenCalledWith(false);
    expect(closeDiagnose).not.toHaveBeenCalled();
    expect(onOpenResource).toHaveBeenCalledWith(ref, "run-1");
  });

  it("closes an overlay before opening an evidence resource", () => {
    const events: string[] = [];
    const onOpenResource = vi.fn(() => events.push("open"));
    const setMaximized = vi.fn(() => events.push("dock"));
    const closeDiagnose = vi.fn(() => events.push("close"));
    const ref = {
      kind: "Pod",
      namespace: "shop",
      name: "api-7d9f",
    };

    openInvestigationEvidenceResource(
      ref,
      onOpenResource,
      setMaximized,
      closeDiagnose,
      true,
    );

    expect(events).toEqual(["close", "open"]);
    expect(setMaximized).not.toHaveBeenCalled();
    expect(closeDiagnose).toHaveBeenCalledOnce();
    expect(onOpenResource).toHaveBeenCalledWith(ref, null);
  });

  it("keeps document overflow out of the bounded Diagnose frame", () => {
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("absolute");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("min-h-0");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("overflow-hidden");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).not.toContain("overflow-y-auto");
  });

  it("reserves the history rail for wider investigation surfaces", () => {
    expect(INVESTIGATION_HISTORY_MIN_WIDTH).toBe(1750);
    expect(MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS).toBe(
      "@min-[1750px]/diagnose-surface:hidden",
    );
    expect(MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS).toBe(
      "hidden @min-[1750px]/diagnose-surface:flex",
    );
    expect(MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS).toBe(
      "hidden @min-[1750px]/diagnose-surface:block",
    );
    expect(MAXIMIZED_RUN_META_VISIBILITY_CLASS).toBe(
      "hidden @min-[1750px]/diagnose-surface:flex",
    );
  });

  it("keeps docked Home generic and removes actions for its retained run", () => {
    expect(
      investigationHeaderPresentation({
        view: "home",
        maximized: false,
        hasVisibleRunDetail: true,
      }),
    ).toEqual({
      genericIdentityClass: "",
      detailIdentityClass: null,
      runActionsClass: null,
    });
  });

  it("swaps generic Home identity for the labeled retained detail at the wide breakpoint", () => {
    expect(
      investigationHeaderPresentation({
        view: "home",
        maximized: true,
        hasVisibleRunDetail: true,
      }),
    ).toEqual({
      genericIdentityClass: MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS,
      detailIdentityClass: MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS,
      runActionsClass: MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS,
    });
  });

  it("labels a direct detail at every size and keeps its run actions with it", () => {
    expect(
      investigationHeaderPresentation({
        view: "investigation",
        maximized: false,
        hasVisibleRunDetail: true,
      }),
    ).toEqual({
      genericIdentityClass: null,
      detailIdentityClass: "",
      runActionsClass: "",
    });
  });

  it("does not invent a retained-detail header or actions without a run", () => {
    expect(
      investigationHeaderPresentation({
        view: "home",
        maximized: true,
        hasVisibleRunDetail: false,
      }),
    ).toEqual({
      genericIdentityClass: "",
      detailIdentityClass: null,
      runActionsClass: null,
    });
  });

  it("removes run actions when another surface replaces the direct detail", () => {
    expect(
      investigationHeaderPresentation({
        view: "investigation",
        maximized: true,
        hasVisibleRunDetail: false,
      }),
    ).toEqual({
      genericIdentityClass: null,
      detailIdentityClass: "",
      runActionsClass: null,
    });
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

  it("does not offer a follow-up after the retained run disappears", () => {
    expect(
      canContinueInvestigation(
        { ...run("running"), canContinue: false },
        "error",
        true,
      ),
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

describe("canInvestigateFurther", () => {
  it("offers a context-preserving next step on a completed automatic investigation", () => {
    expect(
      canInvestigateFurther({
        ...run("done"),
        trigger: "background",
        issueId: "issue-1",
      }),
    ).toBe(true);
  });

  it("does not promise findings for missing, unfinished, stale or human investigations", () => {
    expect(canInvestigateFurther(run("done"))).toBe(false);
    expect(
      canInvestigateFurther({ ...run("done"), trigger: "background" }),
    ).toBe(false);
    for (const status of [
      "running",
      "stopping",
      "error",
      "stopped",
      "stale",
    ] as const) {
      expect(
        canInvestigateFurther({
          ...run(status),
          trigger: "background",
          issueId: "issue-1",
        }),
      ).toBe(false);
    }
    expect(
      canInvestigateFurther(
        { ...run("done"), trigger: "background", issueId: "issue-1" },
        true,
      ),
    ).toBe(false);
  });
});
