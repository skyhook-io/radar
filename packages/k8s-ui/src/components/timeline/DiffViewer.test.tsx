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
});
