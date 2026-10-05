import { describe, it, expect } from "vitest";
import {
  buildDatumHostnames,
  datumLeaseFact,
  type DatumWorkspace,
} from "./workspace";
import { getDatumStatus } from "../resources/resource-utils-datum";
const proxy = (ns = "p") => ({
  apiVersion: "networking.datumapis.com/v1alpha",
  kind: "HTTPProxy",
  metadata: { name: "web", namespace: ns },
  spec: {
    hostnames: ["www.example.test"],
    rules: [{ backends: [{ instance: { name: "origin" } }] }],
  },
  status: {
    conditions: [
      { type: "Accepted", status: "True" },
      { type: "Programmed", status: "True" },
    ],
    hostnameStatuses: [
      {
        hostname: "www.example.test",
        conditions: [
          { type: "Available", status: "True" },
          { type: "CertificateReady", status: "False", reason: "IssuerFailed" },
        ],
      },
    ],
  },
});
const ws = (objects: Record<string, any[]>): DatumWorkspace => ({
  installed: true,
  context: "test",
  namespaces: null,
  coverage: Object.fromEntries(
    Object.keys(objects).map((k) => [k, { state: "full" }]),
  ),
  objects,
  issues: [],
});
describe("Datum evidence", () => {
  it("keeps hostname certificate failure separate from programmed aggregate", () => {
    const row = buildDatumHostnames(ws({ httpproxies: [proxy()] }))[0];
    expect(row.proxy.tone).toBe("healthy");
    expect(row.tls.tone).toBe("degraded");
    expect(row.tls.source).toContain("CertificateReady");
    expect(row.problem).toBe("IssuerFailed");
    expect(row.verification.tone).toBe("unknown");
  });
  it("joins instance backend to EndpointSlice by exact namespace, with unknown readiness preserved", () => {
    const slice = {
      apiVersion: "discovery.k8s.io/v1",
      kind: "EndpointSlice",
      metadata: { name: "origin", namespace: "p" },
      endpoints: [{ addresses: ["192.0.2.1"], conditions: {} }],
    };
    const row = buildDatumHostnames(
      ws({
        httpproxies: [proxy()],
        instances: [{ metadata: { name: "origin", namespace: "p" } }],
        endpointslices: [slice],
      }),
    )[0];
    expect(row.backends).toEqual([slice]);
    expect(row.backend.tone).toBe("unknown");
    expect(row.backend.source).toContain("EndpointSlice");
  });
  it("never joins a domain in another namespace", () => {
    const row = buildDatumHostnames(
      ws({
        httpproxies: [proxy()],
        domains: [
          {
            kind: "Domain",
            metadata: { name: "example", namespace: "other" },
            spec: { domainName: "example.test" },
          },
        ],
      }),
    )[0];
    expect(row.domains).toEqual([]);
  });
  it("acceptance alone is unknown and alternate Domain verification is not a failure", () => {
    expect(
      getDatumStatus({
        kind: "DNSZone",
        status: { conditions: [{ type: "Accepted", status: "True" }] },
      }).color,
    ).toBe("unknown");
    expect(
      getDatumStatus({
        kind: "Domain",
        status: {
          conditions: [
            { type: "Verified", status: "True" },
            { type: "ValidDomain", status: "True" },
            { type: "VerifiedHTTP", status: "False", reason: "RecordNotFound" },
          ],
        },
      }).color,
    ).toBe("healthy");
  });
});

it("lease expiry uses observed duration and unknown inventory does not imply absence", () => {
  const connector = {
    metadata: { name: "edge", namespace: "p" },
    status: { leaseRef: { name: "edge" } },
  };
  const now = Date.parse("2026-10-05T00:00:00Z");
  expect(datumLeaseFact(ws({}), connector, now).text).toBe("Leases not read");
  expect(datumLeaseFact(ws({ leases: [] }), connector, now).text).toBe(
    "Referenced lease not observed",
  );
  expect(
    datumLeaseFact(
      ws({
        leases: [
          {
            metadata: { name: "edge", namespace: "p" },
            spec: {
              renewTime: "2026-10-04T23:58:00Z",
              leaseDurationSeconds: 60,
            },
          },
        ],
      }),
      connector,
      now,
    ).text,
  ).toBe("Lease expired");
  expect(
    datumLeaseFact(
      ws({
        leases: [
          {
            metadata: { name: "edge", namespace: "p" },
            spec: { renewTime: "2026-10-05T00:00:00Z" },
          },
        ],
      }),
      connector,
      now,
    ).tone,
  ).toBe("unknown");
});

it("uses per-hostname verification without requiring a Domain observation", () => {
  const p = proxy();
  p.status.hostnameStatuses[0].conditions.push({
    type: "Verified",
    status: "False",
    reason: "VerificationFailed",
  });
  const row = buildDatumHostnames(ws({ httpproxies: [p] }))[0];
  expect(row.verification.tone).toBe("degraded");
  expect(row.verification.source).toContain("Verified");
});
it("keeps stale workload availability reconciling using the reported top-level generation", () => {
  expect(
    getDatumStatus({
      kind: "Workload",
      metadata: { generation: 2 },
      status: {
        observedGeneration: 1,
        conditions: [
          { type: "Available", status: "False", reason: "ProvisioningFailed" },
        ],
      },
    }).color,
  ).toBe("neutral");
});
