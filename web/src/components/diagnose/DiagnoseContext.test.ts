import { describe, expect, it } from "vitest";
import {
  investigationStartView,
  investigationWorkspaceNavigationState,
  investigationWorkspacePath,
  investigationWorkspaceRestorePath,
  investigationWorkspaceSearch,
  isInvestigationWorkspacePath,
  shouldExitUnavailableWorkspace,
  workspaceRunIDFromPath,
} from "./DiagnoseContext";

describe("investigation workspace routes", () => {
  it("recognizes the base workspace and selected-run routes", () => {
    expect(isInvestigationWorkspacePath("/investigations")).toBe(true);
    expect(isInvestigationWorkspacePath("/investigations/run_123")).toBe(true);
    expect(workspaceRunIDFromPath("/investigations/run_123")).toBe("run_123");
    expect(workspaceRunIDFromPath("/investigations/run%20one")).toBe("run one");
  });

  it("keeps only cross-view scope in workspace URLs", () => {
    expect(
      investigationWorkspaceSearch(
        "?org=o1&namespaces=prod%2Cstage&ai-run=r1&resource=pod",
      ),
    ).toBe("?org=o1&namespaces=prod%2Cstage");
  });

  it("uses Home for global entry and an explicit route for drawer expansion", () => {
    expect(investigationWorkspacePath(null)).toBe("/investigations");
    expect(investigationWorkspacePath("run 123")).toBe(
      "/investigations/run%20123",
    );
  });

  it("builds opaque navigation state for a synthetic drawer return", () => {
    expect(
      investigationWorkspaceNavigationState({
        returnPath: "/?ai-run=r1",
        drawerOrigin: true,
        closeHistorySteps: 2,
      }),
    ).toEqual({
      investigationWorkspaceReturn: "/?ai-run=r1",
      investigationReturnHistorySteps: "2",
    });
  });

  it("restores the currently focused run into the original dock location", () => {
    expect(
      investigationWorkspaceRestorePath(
        { investigationWorkspaceReturn: "/resources?kind=pods&ai-run=old#row" },
        "new run",
        "https://radar.local",
      ),
    ).toBe("/resources?kind=pods&ai-run=new+run#row");
  });

  it("falls back safely when a malformed return path normalizes cross-origin", () => {
    expect(
      investigationWorkspaceRestorePath(
        { investigationWorkspaceReturn: "/\\\\evil.example" },
        "run-1",
        "https://radar.local",
      ),
    ).toBe("/?ai-run=run-1");
  });

  it("exits only resolved AI-off workspace routes", () => {
    expect(
      shouldExitUnavailableWorkspace("/investigations/run-1", true, false),
    ).toBe(true);
    expect(
      shouldExitUnavailableWorkspace("/investigations/run-1", false, false),
    ).toBe(false);
    expect(shouldExitUnavailableWorkspace("/?ai-run=run-1", true, false)).toBe(
      false,
    );
  });

  it("keeps a question re-run focused while fresh questions start on Home", () => {
    const question = { question: "Why is it slow?" };
    expect(investigationStartView(question, null)).toBe("home");
    expect(investigationStartView(question, "run-1")).toBe("investigation");
  });

  it("builds history-only return state for cross-tree workspace entry", () => {
    expect(
      investigationWorkspaceNavigationState({ closeHistorySteps: 1 }),
    ).toEqual({ investigationReturnHistorySteps: "1" });
  });

  it("does not claim unrelated paths and rejects malformed run ids", () => {
    expect(isInvestigationWorkspacePath("/resources/pods")).toBe(false);
    expect(isInvestigationWorkspacePath("/investigations/run/extra")).toBe(
      true,
    );
    expect(workspaceRunIDFromPath("/investigations")).toBeNull();
    expect(workspaceRunIDFromPath("/investigations/%2Fbad")).toBeNull();
    expect(workspaceRunIDFromPath("/investigations/%ZZ")).toBeNull();
  });
});
