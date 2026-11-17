import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { TopologyFilterSidebar } from "./TopologyFilterSidebar";

describe("TopologyFilterSidebar Calico policy category", () => {
  it.each([
    "CalicoNetworkPolicy",
    "CalicoGlobalNetworkPolicy",
    "CalicoStagedNetworkPolicy",
    "CalicoStagedGlobalNetworkPolicy",
    "CalicoStagedKubernetesNetworkPolicy",
  ])("places %s under Custom Resources", (kind) => {
    const html = renderToString(
      <TopologyFilterSidebar
        nodes={[
          { id: "policy", kind, name: "policy", status: "healthy", data: {} },
        ]}
        visibleKinds={new Set([kind])}
        onToggleKind={() => {}}
        onShowAll={() => {}}
        onHideAll={() => {}}
      />,
    );

    expect(html).toContain("Custom Resources");
    expect(html).not.toContain(">Networking<");
  });
});

describe("TopologyFilterSidebar kind visibility", () => {
  const render = (visible: boolean) =>
    renderToString(
      <TopologyFilterSidebar
        nodes={[{ id: "pod", kind: "Pod", name: "pod", status: "healthy", data: {} }]}
        visibleKinds={new Set(visible ? ["Pod"] : [])}
        onToggleKind={() => {}}
        onShowAll={() => {}}
        onHideAll={() => {}}
      />,
    );

  it("marks a shown kind as pressed with the accent icon", () => {
    const html = render(true);
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain("text-accent");
  });

  it("marks a hidden kind as not pressed and dims it", () => {
    const html = render(false);
    expect(html).toContain('aria-pressed="false"');
    expect(html).not.toContain("text-accent");
    expect(html).toContain("text-theme-text-tertiary");
  });
});
