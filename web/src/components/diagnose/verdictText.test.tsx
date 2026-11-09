import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { VerdictText } from "./AssessmentCard";
import {
  assessmentStateLabel,
  plainVerdictText,
  verdictSentence,
} from "./verdictText";

describe("VerdictText", () => {
  it("renders code spans and nothing else as markup", () => {
    const html = renderToStaticMarkup(
      <VerdictText text="ConfigMap `checkout-settings` is **missing**" />,
    );
    expect(html).toBe(
      'ConfigMap <code class="inline-code">checkout-settings</code> is missing',
    );
  });

  it("keeps code contents exactly as written", () => {
    expect(
      renderToStaticMarkup(
        <VerdictText text="`__init__` fails on `*.svc.*` and `[a-z](x)`" />,
      ),
    ).toBe(
      '<code class="inline-code">__init__</code> fails on <code class="inline-code">*.svc.*</code> and <code class="inline-code">[a-z](x)</code>',
    );
    expect(plainVerdictText("`__init__` fails on `*.svc.*`")).toBe(
      "__init__ fails on *.svc.*",
    );
  });

  it("keeps a sentence that looks like markdown structure as text", () => {
    expect(
      renderToStaticMarkup(<VerdictText text="1. pod restarts # times" />),
    ).toBe("1. pod restarts # times");
  });
});

describe("verdictSentence", () => {
  it("is the headline, as one plain line", () => {
    expect(
      verdictSentence({
        summary: "The `api` container\nexits; see [logs](http://x)",
        rootCause: "ignored",
      }),
    ).toBe("The api container exits; see logs");
  });

  it("keeps doubled underscores in prose identifiers", () => {
    expect(plainVerdictText("checkout__config is unset")).toBe(
      "checkout__config is unset",
    );
  });

  it("falls back to the cause, and is empty for an answer", () => {
    expect(
      verdictSentence({ summary: "  ", rootCause: "DB_PORT is bad" }),
    ).toBe("DB_PORT is bad");
    expect(verdictSentence({ rootCause: "DB_PORT is `abc`" })).toBe(
      "DB_PORT is abc",
    );
    expect(verdictSentence({})).toBe("");
    expect(plainVerdictText(undefined)).toBe("");
  });
});

describe("assessmentStateLabel", () => {
  it("says how sure the assessment is, as Findings does", () => {
    expect(
      assessmentStateLabel({ rootCause: "x", certainty: "suspected" })?.text,
    ).toBe("Suspected");
    expect(assessmentStateLabel({ inconclusive: true })?.text).toBe(
      "Couldn't determine",
    );
    expect(assessmentStateLabel({ healthy: true })).toEqual({
      text: "Healthy",
      warn: false,
    });
    expect(assessmentStateLabel({ healthy: true }, true)).toEqual({
      text: "Conflicts with captured evidence",
      warn: true,
    });
    expect(assessmentStateLabel({ rootCause: "x" })).toBeUndefined();
  });
});
