import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import type { RelatedFindingsGroup } from "@skyhook-io/k8s-ui";
import { RelatedFindingsSection } from "./RelatedFindingsSection";

const cached: RelatedFindingsGroup = {
  associatedVia: {
    group: "kafka.strimzi.io",
    kind: "KafkaConnect",
    namespace: "app",
    name: "connect",
  },
  kinds: {
    count: 1,
    names: [{ group: "kafka.strimzi.io", kind: "KafkaConnector" }],
  },
  checked: 1,
  unknown: 0,
  gaps: [],
};

describe("Related findings coverage", () => {
  it("hides absent, empty and assessed groups without findings or limitations", () => {
    for (const data of [undefined, [], [cached]])
      expect(renderToString(<RelatedFindingsSection data={data} />)).toBe("");
  });
  it("explains unassessable snapshots from structured fields alone", () => {
    const html = renderToString(
      <RelatedFindingsSection data={[{ ...cached, unknown: 1 }]} />,
    );
    expect(html).toContain("1 of 1 checked resources");
    expect(html).toContain("could not be assessed");
    expect(html).toContain("KafkaConnector (kafka.strimzi.io)");
    expect(html).toContain("status is unchanged");
    expect(html).not.toContain("Operational issues");
  });
  it.each([
    ["radar_kind_not_collected", "Radar doesn&#x27;t collect it"],
    ["kind_sync_pending", "still loading"],
  ])("explains %s using the existing read vocabulary", (code, explanation) => {
    const html = renderToString(
      <RelatedFindingsSection data={[{ ...cached, gaps: [{ code }] }]} />,
    );
    expect(html).toContain(explanation);
  });
  it("keeps input gaps and output omissions distinct", () => {
    const gap = { ...cached, checked: 0, gaps: [{ code: "list_too_large" }] };
    const html = renderToString(<RelatedFindingsSection data={[gap]} />);
    expect(html).toContain("incomplete related inventory");
    expect(html).not.toContain("Additional related findings were omitted");
    const both = renderToString(
      <RelatedFindingsSection data={[{ ...gap, truncated: true }]} />,
    );
    expect(both).toContain("incomplete related inventory");
    expect(both).toContain("Additional related findings were omitted");
  });
  it("keeps independent anchors and counts separate without connector wording", () => {
    const groups: RelatedFindingsGroup[] = [
      {
        ...cached,
        associatedVia: {
          group: "postgresql.cnpg.io",
          kind: "Cluster",
          namespace: "db",
          name: "orders",
        },
        kinds: {
          count: 1,
          names: [{ group: "postgresql.cnpg.io", kind: "Backup" }],
        },
        checked: 2,
        unknown: 1,
      },
      {
        ...cached,
        associatedVia: {
          group: "external-secrets.io",
          kind: "ExternalSecret",
          namespace: "app",
          name: "credentials",
        },
        kinds: {
          count: 1,
          names: [{ group: "external-secrets.io", kind: "ClusterSecretStore" }],
        },
        checked: 3,
        unknown: 2,
      },
    ];
    const html = renderToString(<RelatedFindingsSection data={groups} />);
    expect(html).toContain("db/orders");
    expect(html).toContain("app/credentials");
    expect(html).toContain("1 of 2 checked");
    expect(html).toContain("2 of 3 checked");
    expect(html).not.toContain("3 of 5");
    expect(html).not.toContain("connector");
  });
});
