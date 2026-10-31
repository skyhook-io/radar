import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { QuestionFocus } from "./QuestionFocus";

const focus = [
  { kind: "Deployment", group: "apps", namespace: "shop", name: "api" },
];

describe("QuestionFocus", () => {
  it("offers to investigate the resource or choose another", () => {
    const html = renderToStaticMarkup(
      <QuestionFocus
        focus={focus}
        onOpenResource={vi.fn()}
        onInvestigate={vi.fn()}
        onChooseResource={vi.fn()}
      />,
    );
    expect(html).toContain("Investigate this resource");
    expect(html).toContain("Choose another resource");
  });

  it("offers nothing to act on for a run about another cluster", () => {
    const html = renderToStaticMarkup(
      <QuestionFocus
        focus={focus}
        stale
        onOpenResource={vi.fn()}
        onInvestigate={vi.fn()}
        onChooseResource={vi.fn()}
      />,
    );
    expect(html).toContain("Deployment shop/api");
    expect(html).not.toContain("<button");
    expect(html).toContain("connect to that cluster and find");
  });
});
