import { describe, expect, it, vi } from "vitest";
import {
  DIAGNOSE_SURFACE_FRAME_CLASS,
  MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS,
  MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS,
  MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS,
  MAXIMIZED_HISTORY_VISIBILITY_CLASS,
  MAXIMIZED_RUN_META_VISIBILITY_CLASS,
  canStartNewInvestigation,
  investigationBreadcrumbVisibilityClass,
  investigationHeaderPresentation,
  openInvestigationEvidenceResource,
} from "./DiagnoseSurface";
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
    // The body offers "Investigate current cluster" WITH the context-changed
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
    );

    expect(events).toEqual(["dock", "open"]);
    expect(setMaximized).toHaveBeenCalledWith(false);
    expect(closeDiagnose).not.toHaveBeenCalled();
    expect(onOpenResource).toHaveBeenCalledWith(ref);
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
    expect(onOpenResource).toHaveBeenCalledWith(ref);
  });

  it("keeps document overflow out of the bounded Diagnose frame", () => {
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("absolute");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("min-h-0");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).toContain("overflow-hidden");
    expect(DIAGNOSE_SURFACE_FRAME_CLASS).not.toContain("overflow-y-auto");
  });

  it("swaps the maximized breadcrumb for the master list at one container breakpoint", () => {
    expect(investigationBreadcrumbVisibilityClass(false)).toBe("");
    expect(investigationBreadcrumbVisibilityClass(true)).toBe(
      "@min-[1500px]/diagnose-surface:hidden",
    );
    expect(MAXIMIZED_HISTORY_VISIBILITY_CLASS).toBe(
      "hidden @min-[1500px]/diagnose-surface:block",
    );
    expect(MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS).toBe(
      "@min-[1500px]/diagnose-surface:hidden",
    );
    expect(MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS).toBe(
      "hidden @min-[1500px]/diagnose-surface:flex",
    );
    expect(MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS).toBe(
      "hidden @min-[1500px]/diagnose-surface:block",
    );
    expect(MAXIMIZED_RUN_META_VISIBILITY_CLASS).toBe(
      "hidden @min-[1500px]/diagnose-surface:flex",
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
