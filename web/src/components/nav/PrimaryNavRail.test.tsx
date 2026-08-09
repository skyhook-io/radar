import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { PrimaryNavRail } from "./PrimaryNavRail";

function renderRail() {
  return renderToString(
    <PrimaryNavRail
      activeView="home"
      onNavigate={() => {}}
      pinned
      onTogglePinned={() => {}}
    />,
  );
}

describe("PrimaryNavRail", () => {
  it("always surfaces Capacity — the view reads cluster capacity across every node manager, not just Karpenter", () => {
    expect(renderRail()).toContain("Capacity");
  });
});
