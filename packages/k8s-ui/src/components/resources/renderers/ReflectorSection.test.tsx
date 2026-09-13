import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ReflectorSection } from "./ReflectorSection";
import { ResourceRendererDispatch } from "../../shared/ResourceRendererDispatch";
import type { Relationships } from "../../../types";

const prefix = "reflector.v1.k8s.emberstack.com/";
const source = { kind: "ConfigMap", namespace: "source", name: "settings" };
const mirror = { kind: "ConfigMap", namespace: "app", name: "mirror" };
function resource(annotations: Record<string, string> = {}) {
  return {
    apiVersion: "v1",
    metadata: {
      namespace: "app",
      name: "mirror",
      annotations: Object.fromEntries(
        Object.entries(annotations).map(([k, v]) => [prefix + k, v]),
      ),
    },
  };
}
function render(
  annotations: Record<string, string>,
  reflection?: Relationships["reflection"],
) {
  return renderToStaticMarkup(
    <ReflectorSection
      data={resource(annotations)}
      reflection={reflection}
      onNavigate={() => {}}
    />,
  );
}

describe("Reflector evidence and copy", () => {
  it("leaves ordinary objects and non-core API groups alone", () => {
    expect(render({})).toBe("");
    expect(render({ unrelated: "true" })).toBe("");
    expect(
      renderToStaticMarkup(
        <ReflectorSection
          data={{
            ...resource({ "reflection-allowed": "true" }),
            apiVersion: "example.io/v1",
          }}
        />,
      ),
    ).toBe("");
  });

  it("shows source settings with OR semantics and bounded visible mirrors", () => {
    const mirrors = Array.from({ length: 12 }, (_, i) => ({
      ...mirror,
      name: `mirror-${i}`,
    }));
    const html = render(
      {
        "reflection-allowed": "true",
        "reflection-auto-enabled": "true",
        "reflection-allowed-namespaces": "prod-.*",
        "reflection-allowed-namespaces-selector": "team=app",
      },
      { mirrors },
    );
    expect(html).toContain("Source");
    expect(html).toContain("Name patterns:");
    expect(html).toContain(">or</span>");
    expect(html).toContain("Label selector:");
    expect(html).toContain("Show all 12 visible mirrors");
    expect(html).toContain("app/mirror-9");
    expect(html).not.toContain("app/mirror-10");
    expect(html).toContain("other mirrors may exist");
  });

  it.each(["True", "TRUE", " true "])(
    "accepts controller boolean %s and raw .NET timestamp",
    (automatic) => {
      const html = render(
        {
          reflects: "source/settings",
          "auto-reflects": automatic,
          "reflected-at": "2026-09-13T12:34:56.1234567+00:00",
        },
        { source },
      );
      expect(html).toContain("Automatic mirror");
      expect(html).toContain("2026-09-13T12:34:56.1234567+00:00");
      expect(html).not.toContain("Check reflection configuration");
    },
  );

  it("keeps manual pre-copy state neutral and offers only observed navigation", () => {
    const html = render({ reflects: "source/settings" }, { source });
    expect(html).toContain("Manual mirror");
    expect(html).toContain("No version recorded");
    expect(html).toContain("source/settings</button>");
    expect(html).not.toContain("Check reflection configuration");
    expect(html).toContain("Edit the source for lasting changes");
  });

  it("does not turn a hidden source into missing, a link, or version evidence", () => {
    const html = render(
      { reflects: "hidden/settings", "reflected-version": "41" },
      { source, sourceResourceVersion: "42" },
    );
    expect(html).toContain("hidden/settings");
    expect(html).toContain("not available in this view");
    expect(html).not.toContain("hidden/settings</button>");
    expect(html).not.toContain("Source version (snapshot)");
    expect(html).not.toContain("missing");
    expect(html).not.toContain("Check reflection configuration");
  });

  it.each([
    ["opaque-x", "opaque-x", "matches the source snapshot"],
    ["opaque-x", "opaque-y", "differs from the source snapshot"],
  ])(
    "reports version equality without contents or health claims",
    (copied, current, message) => {
      const html = render(
        { reflects: "source/settings", "reflected-version": copied },
        { source, sourceResourceVersion: current },
      );
      expect(html).toContain(message);
      expect(html).toContain("do not prove matching contents");
      expect(html).not.toContain("Synced");
      expect(html).not.toContain("Check reflection configuration");
    },
  );

  it.each(["", "missing-namespace", "ns/name/extra"])(
    "flags malformed declarations (%s)",
    (reflects) => {
      expect(render({ reflects })).toContain(
        "must name a source as namespace/name",
      );
    },
  );

  it("flags self reference and contradictory auto-source settings", () => {
    expect(render({ reflects: "app/mirror" })).toContain(
      "points to this object itself",
    );
    expect(render({ "reflection-auto-enabled": "true" })).toContain(
      "requires reflection-allowed to be true",
    );
    expect(render({ "reflection-allowed": "yes" })).toContain(
      "must be true or false",
    );
  });

  it("explains a declared chain without dropping either direction", () => {
    const html = render(
      { reflects: "source/settings", "reflection-allowed": "true" },
      { source, mirrors: [{ ...mirror, namespace: "leaf" }] },
    );
    expect(html).toContain("source/settings</button>");
    expect(html).toContain("leaf/mirror</button>");
    expect(html).toContain("does not propagate its updates");
  });

  it("shows only explicit reflection fields rather than inferring from kind equality", () => {
    const html = renderToStaticMarkup(
      <ResourceRendererDispatch
        resource={{ kind: "configmaps", namespace: "app", name: "mirror" }}
        data={resource()}
        relationships={{ consumers: [mirror] }}
        onCopy={() => {}}
        copied={null}
        showCommonSections={false}
      />,
    );
    expect(html).not.toContain("Reflector");
  });

  it.each(["configmaps", "secrets"])(
    "wires the %s renderer and avoids duplicate generic mirror links",
    (kind) => {
      const ref = {
        ...source,
        kind: kind === "secrets" ? "Secret" : "ConfigMap",
      };
      const html = renderToStaticMarkup(
        <ResourceRendererDispatch
          resource={{ kind, namespace: "app", name: "mirror" }}
          data={{ ...resource({ reflects: "source/settings" }), data: {} }}
          relationships={{ configRefs: [ref], reflection: { source: ref } }}
          onCopy={() => {}}
          copied={null}
          onNavigate={() => {}}
        />,
      );
      expect(html).toContain("Reflector");
      expect(html).toContain("source/settings</button>");
      expect(html).not.toContain("Related Resources");
      expect(html).not.toContain("Check reflection configuration");
    },
  );
});

describe("Reflector configuration diagnostics", () => {
  it("does not describe a malformed reference as an active mirror chain", () => {
    const html = render({
      reflects: "missing-namespace",
      "reflection-allowed": "true",
    });
    expect(html).toContain("Invalid source reference");
    expect(html).not.toContain("does not propagate");
    expect(html).not.toContain("Edit the source for lasting changes");
  });
  it("warns when visible mirrors reference a source that no longer allows reflection", () => {
    const html = render({}, { mirrors: [mirror] });
    expect(html).toContain("Reflection is disabled on this source");
  });
});
