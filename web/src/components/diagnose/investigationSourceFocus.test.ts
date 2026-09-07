import { describe, expect, it } from "vitest";
import {
  locateSourceExcerpt,
  evidenceSourceExcerpt,
} from "./investigationSourceFocus";

describe("source excerpt targeting", () => {
  it("targets the selected crash field when the same line is repeated in current and previous logs", () => {
    const line = "2026-09-06T07:04:54Z Error: Missing MONGO_PASSWORD";
    const display = JSON.stringify(
      {
        logsCurrent: { lines: [line] },
        logsPrevious: { lines: [line] },
        crashCauses: [{ logLine: line }],
      },
      null,
      2,
    );
    expect(locateSourceExcerpt(display, line)).toBeUndefined();
    const range = locateSourceExcerpt(display, {
      text: line,
      field: "logLine",
    })!;
    expect(display.slice(range.start, range.end)).toBe(JSON.stringify(line));
    expect(range.start).toBeGreaterThan(display.indexOf('"crashCauses"'));
    expect(
      locateSourceExcerpt(
        JSON.stringify({ a: { logLine: line }, b: { logLine: line } }, null, 2),
        { text: line, field: "logLine" },
      ),
    ).toBeUndefined();
    expect(
      locateSourceExcerpt(JSON.stringify({ lines: [line] }, null, 2), {
        text: line,
        field: "logLine",
      }),
    ).toBeUndefined();
  });
  it("locates a single Secret key, never its values or a guess among multiple keys", () => {
    expect(
      evidenceSourceExcerpt({
        type: "resource",
        warnings: [],
        resource: {
          apiVersion: "v1",
          metadata: { name: "api", namespace: "dev" },
          kind: "Secret",
          keys: ["QUALIFIRE_API_KEY"],
          data: { password: "do-not-use" },
        },
      }),
    ).toBe("QUALIFIRE_API_KEY");
    expect(
      evidenceSourceExcerpt({
        type: "resource",
        warnings: [],
        resource: {
          apiVersion: "v1",
          metadata: { name: "api", namespace: "dev" },
          kind: "Secret",
          keys: ["FIRST_KEY", "SECOND_KEY"],
        },
      }),
    ).toBeUndefined();
    expect(
      evidenceSourceExcerpt({
        type: "resource",
        warnings: [],
        resource: {
          apiVersion: "v1",
          metadata: { name: "api", namespace: "dev" },
          kind: "Secret",
          data: { password: "do-not-use" },
        },
      }),
    ).toBeUndefined();
  });
  it("targets a unique original log line", () => {
    const text = "before\nMissing required MONGO_PASSWORD\nafter";
    const range = locateSourceExcerpt(text, "Missing required MONGO_PASSWORD")!;
    expect(text.slice(range.start, range.end)).toBe(
      "Missing required MONGO_PASSWORD",
    );
  });
  it("does not turn one line of a multi-line log collection into the source locator", () => {
    const data = {
      type: "logs" as const,
      pod: "api-abc",
      container: "api",
      previous: false,
      warnings: [],
      logs: {
        lines: ["ERROR missing DATABASE_URL"],
        totalLines: 2,
        matchedLines: 1,
        fallback: false,
      },
    };
    expect(evidenceSourceExcerpt(data)).toBe("ERROR missing DATABASE_URL");
    expect(
      evidenceSourceExcerpt({
        ...data,
        logs: {
          ...data.logs,
          lines: ["ERROR missing DATABASE_URL", "ERROR startup failed"],
        },
      }),
    ).toBeUndefined();
  });
  it("targets JSON-escaped original text, including quotes and newlines", () => {
    const message = 'Missing "secret"\nconfiguration';
    const text = JSON.stringify({ message }, null, 2);
    const range = locateSourceExcerpt(text, message)!;
    expect(text.slice(range.start, range.end)).toBe(JSON.stringify(message));
  });
  it("does not guess for absent, short or repeated content", () => {
    expect(
      locateSourceExcerpt("redacted", "Missing required MONGO_PASSWORD"),
    ).toBeUndefined();
    expect(locateSourceExcerpt("Error", "Error")).toBeUndefined();
    expect(
      locateSourceExcerpt("long message; long message", "long message"),
    ).toBeUndefined();
    expect(
      locateSourceExcerpt(
        '{"a":"long message","b":"long message"}',
        "long message",
      ),
    ).toBeUndefined();
  });
  it("does not invent a locator for aggregate or absence evidence", () => {
    expect(
      evidenceSourceExcerpt({ type: "events", events: [], scope: "namespace" }),
    ).toBeUndefined();
    expect(
      evidenceSourceExcerpt({
        type: "inventory",
        resources: [],
        scope: "namespace",
      }),
    ).toBeUndefined();
  });
});
