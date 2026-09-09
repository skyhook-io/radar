import { describe, expect, it } from "vitest";
import {
  investigationWorkspaceNavigationState,
  investigationWorkspacePath,
  investigationWorkspaceSearch,
  isInvestigationWorkspacePath,
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
      investigationRestoreHistorySteps: "1",
      investigationReturnHistorySteps: "2",
    });
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
