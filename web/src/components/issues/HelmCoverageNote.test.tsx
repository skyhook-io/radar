import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  HELM_RESULT_OLD_SECONDS,
  HelmCoverageNote,
  HelmUncheckedEmptyState,
  helmCoverage,
  helmNotCurrent,
} from "./HelmCoverageNote";

const render = (status: Parameters<typeof helmCoverage>[0]) =>
  renderToStaticMarkup(<HelmCoverageNote coverage={helmCoverage(status)} />);

describe("helmCoverage", () => {
  it("is quiet for a full-wait response and a recent current read", () => {
    expect(helmCoverage(undefined)).toEqual({ state: "checked" });
    expect(helmCoverage({ state: "current", age_seconds: 20, reading: true })).toEqual({ state: "checked" });
  });

  it("gives an old current result its age, and says when a refresh is waiting for a slot", () => {
    expect(render({ state: "current", age_seconds: 7200 })).toContain("from a check 2h ago");
    expect(render({ state: "current", age_seconds: 7200, waiting_for_slot: true })).toContain(
      "Radar will check again when its other Helm reads finish",
    );
    expect(render({ state: "current", age_seconds: HELM_RESULT_OLD_SECONDS - 1 })).toBe("");
  });

  it("words failure codes for people", () => {
    expect(render({ state: "failed", error: "timeout", read_timeout_seconds: 60 })).toContain("it took longer than 60 seconds");
    expect(render({ state: "failed", error: "forbidden" })).toContain("Kubernetes denied listing Helm release Secrets");
    expect(render({ state: "failed", error: "error" })).toContain("the read failed");
  });
});

describe("HelmCoverageNote", () => {
  it("says Helm hasn't been checked yet in one sentence, without claiming a read is running", () => {
    const html = render({ state: "not_checked_yet" });
    expect(html).toBe(html.replace(/reading/, ""));
    expect(html).toContain("Helm releases haven&#x27;t been checked yet, so failed or stuck Helm releases aren&#x27;t in this list.");
  });

  it("says where the Helm rows on screen come from after a failed check", () => {
    const html = render({ state: "failed", error: "timeout", read_timeout_seconds: 60, failed_age_seconds: 60, age_seconds: 1800 });
    expect(html).toContain("The latest Helm check failed 1m ago: it took longer than 60 seconds.");
    expect(html).toContain("The Helm issues shown are from the check 30m ago.");
  });

  it("uses amber only for a failure", () => {
    expect(render({ state: "failed", error: "error" })).toContain("text-amber-500");
    expect(render({ state: "not_checked_yet" })).not.toContain("text-amber-500");
  });
});

describe("HelmUncheckedEmptyState", () => {
  const empty = (status: Parameters<typeof helmCoverage>[0]) =>
    renderToStaticMarkup(<HelmUncheckedEmptyState coverage={helmCoverage(status)} />);

  it("replaces the all-clear whenever the Helm part isn't current", () => {
    expect(helmNotCurrent(helmCoverage({ state: "current", age_seconds: 10 }))).toBe(false);
    for (const status of [
      { state: "not_checked_yet" as const },
      { state: "failed" as const, error: "forbidden" },
      { state: "failed" as const, error: "error", age_seconds: 60 },
      { state: "current" as const, age_seconds: 7200 },
    ]) {
      expect(helmNotCurrent(helmCoverage(status))).toBe(true);
      const html = empty(status);
      expect(html).toContain("No issues found");
      expect(html).not.toContain("Nothing broken");
    }
  });

  it("says what is known in each case", () => {
    expect(empty({ state: "not_checked_yet" })).toContain("haven&#x27;t been checked yet");
    expect(empty({ state: "failed", error: "forbidden" })).toContain("couldn&#x27;t be checked");
    expect(empty({ state: "failed", error: "timeout", read_timeout_seconds: 60, failed_age_seconds: 120, age_seconds: 3600 })).toContain(
      "It took longer than 60 seconds (2m ago). Helm releases were last checked 1h ago.",
    );
    expect(empty({ state: "current", age_seconds: 7200 })).toContain("Helm releases were last checked 2h ago.");
  });
});

describe("unavailable", () => {
  it("never reads as checked when Helm couldn't be read at all", () => {
    const coverage = helmCoverage({ state: "unavailable" });
    expect(helmNotCurrent(coverage)).toBe(true);
    expect(renderToStaticMarkup(<HelmCoverageNote coverage={coverage} />)).toContain("couldn&#x27;t be checked");
    const empty = renderToStaticMarkup(<HelmUncheckedEmptyState coverage={coverage} />);
    expect(empty).toContain("No issues found, but Helm releases couldn&#x27;t be checked");
    expect(empty).not.toContain("Nothing broken");
  });
});
