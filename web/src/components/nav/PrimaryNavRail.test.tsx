import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { PrimaryNavRail } from "./PrimaryNavRail";

function renderRail(whatsNew?: { unread: boolean; onOpen: () => void }) {
  return renderToString(
    <PrimaryNavRail
      activeView="home"
      onNavigate={() => {}}
      pinned
      onTogglePinned={() => {}}
      whatsNew={whatsNew}
    />,
  );
}

describe("PrimaryNavRail", () => {
  it("always surfaces Capacity — the view reads cluster capacity across every node manager, not just Karpenter", () => {
    expect(renderRail()).toContain("Capacity");
  });

  it("shows What's new only when the host has notes, and marks it while unread", () => {
    expect(renderRail()).not.toContain("What&#x27;s new");
    const read = renderRail({ unread: false, onOpen: () => {} });
    expect(read).toContain("What&#x27;s new");
    expect(read).not.toContain("(unread)");
    expect(renderRail({ unread: true, onOpen: () => {} })).toContain("(unread)");
  });
});
