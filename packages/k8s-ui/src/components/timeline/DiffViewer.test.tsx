import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { DiffViewer } from "./DiffViewer";

describe("DiffViewer", () => {
  it("labels added, removed, and changed values without relying on color", () => {
    const html = renderToStaticMarkup(
      <DiffViewer
        diff={{
          summary: "Three fields changed",
          fields: [
            { path: "spec.added", oldValue: null, newValue: "on" },
            { path: "spec.removed", oldValue: "on", newValue: null },
            { path: "spec.changed", oldValue: "v1", newValue: "v2" },
          ],
        }}
      />,
    );

    expect(html).toContain("Added value:");
    expect(html).toContain("Removed value:");
    expect(html).toContain("Old value:");
    expect(html).toContain("New value:");
  });

  it("renders values kept out of the feed as a badge, not as struck-through text", () => {
    const html = renderToStaticMarkup(
      <DiffViewer
        diff={{
          summary: "pod template annotation checksum/config changed",
          fields: [
            { path: "spec.template.metadata.annotations[checksum/config]", oldValue: "(value hidden)", newValue: "(value hidden)" },
            { path: "spec.template.metadata.annotations[example.com/new]", oldValue: null, newValue: "(set)" },
            { path: "spec.template.metadata.annotations[example.com/old]", oldValue: "(set)", newValue: null },
            { path: "spec.template.spec.affinity", oldValue: "changed", newValue: "changed" },
          ],
        }}
      />,
    );

    expect(html).not.toContain("line-through");
    expect(html).not.toContain("(value hidden)");
    expect(html).not.toContain("(set)");
    expect(html.match(/value hidden/g)).toHaveLength(4);
    expect(html).toContain("Set");
    expect(html).toContain("Removed");
  });
});
