import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Diagnosis } from "../../api/diagnose";
import { NextStepJump, recommendedStepHeadline } from "./NextStepJump";

function diagnosis(steps: string[], recommendedIndex = 1): Diagnosis {
  return {
    rootCause: "cause",
    remediation: steps,
    steps: steps.map((text) => ({ text, kind: "mitigate" })),
    recommendedIndex,
  } as Diagnosis;
}

const long =
  "Confirm with the owner, or in the manifest source, what `read-a/api` is supposed to route to, because the live route points at a Service that no longer exists. Then fix it.";

describe("recommendedStepHeadline", () => {
  it("keeps a short first sentence and drops the dangling colon", () => {
    expect(
      recommendedStepHeadline(
        diagnosis(["Move web off the broken node:\n`kubectl drain node-1`"]),
      ),
    ).toBe("Move web off the broken node");
  });

  it("keeps a ~60-character sentence whole instead of cutting it", () => {
    expect(
      recommendedStepHeadline(
        diagnosis([
          "Stop new pods landing on the broken node and move this one: `kubectl cordon n1`",
        ]),
      ),
    ).toBe("Stop new pods landing on the broken node and move this one");
  });

  it("keeps a very long first sentence whole, without its markdown or later sentences", () => {
    const headline = recommendedStepHeadline(diagnosis([long]));
    expect(headline).toBe(
      "Confirm with the owner, or in the manifest source, what read-a/api is supposed to route to, because the live route points at a Service that no longer exists.",
    );
    expect(headline).not.toContain("…");
  });

  it("names the recommended step, not the first", () => {
    expect(
      recommendedStepHeadline(
        diagnosis(
          ["Read the AWS CNI logs on the node.", "Restart aws-node."],
          2,
        ),
      ),
    ).toBe("Restart aws-node.");
  });

  it("has nothing to name without a step", () => {
    expect(recommendedStepHeadline(undefined)).toBeUndefined();
    expect(recommendedStepHeadline(diagnosis([]))).toBeUndefined();
  });
});

describe("NextStepJump", () => {
  it("renders the whole sentence under a two-line clamp with the arrow outside it", () => {
    const headline = recommendedStepHeadline(diagnosis([long]))!;
    const html = renderToStaticMarkup(
      createElement(NextStepJump, { headline, onClick: () => {} }),
    );
    expect(html).toContain(`Next: ${headline}`);
    expect(html).toMatch(/class="line-clamp-2[^"]*">Next: /);
    expect(html).toMatch(/aria-hidden="true"[^>]*>↓<\/span><\/button>/);
  });

  it("uses the given label for an earlier plan", () => {
    const html = renderToStaticMarkup(
      createElement(NextStepJump, {
        label: "Earlier proposed steps",
        headline: "Move web off the broken node",
        onClick: () => {},
      }),
    );
    expect(html).toContain("Earlier proposed steps");
    expect(html).not.toContain("Move web");
  });

  it("falls back to the section name without a headline", () => {
    const html = renderToStaticMarkup(
      createElement(NextStepJump, { onClick: () => {} }),
    );
    expect(html).toContain(">Next steps<");
  });
});
