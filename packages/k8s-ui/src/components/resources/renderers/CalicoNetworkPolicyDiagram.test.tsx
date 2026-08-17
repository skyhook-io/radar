import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { CalicoNetworkPolicyDiagram } from "./CalicoNetworkPolicyDiagram";

describe("CalicoNetworkPolicyDiagram", () => {
  it("shows ordered actions and Calico match fields in both directions", () => {
    const html = renderToString(
      <CalicoNetworkPolicyDiagram
        spec={{
          selector: "app == 'api'",
          namespaceSelector: "project == 'prod'",
          serviceAccountSelector: "role == 'api'",
          types: ["Ingress", "Egress"],
          ingress: [
            {
              action: "Allow",
              protocol: "TCP",
              source: {
                selector: "role == 'frontend'",
                nets: ["10.0.0.0/8"],
                serviceAccounts: { names: ["frontend"] },
              },
              destination: { ports: [80] },
            },
            { action: "Deny", source: { notSelector: "app == 'debug'" } },
          ],
          egress: [
            {
              action: "Log",
              protocol: "UDP",
              source: { serviceAccounts: { selector: "role == 'api'" } },
              destination: {
                selector: "app == 'db'",
                nets: ["192.168.0.0/16"],
                ports: [{ port: 5432, endPort: 5433 }],
              },
            },
            { action: "Pass", destination: { notNets: ["0.0.0.0/0"] } },
          ],
        }}
      />,
    );

    for (const value of [
      "app == &#x27;api&#x27;",
      "role == &#x27;api&#x27;",
      "Ingress",
      "Egress",
      "Deny",
      "Log",
      "Pass",
      "TCP",
      "UDP",
      "role == &#x27;frontend&#x27;",
      "10.0.0.0/8",
      "frontend",
      "TCP/80",
      "app == &#x27;db&#x27;",
      "192.168.0.0/16",
      "UDP/5432-5433",
      "Not Nets",
      "0.0.0.0/0",
    ]) {
      expect(html).toContain(value);
    }

    expect(html).not.toContain("Policy target");
    expect(html).not.toContain("Source");
    expect(html).not.toContain("Destination");
    expect(html).not.toContain("Rule 1");
    expect(html).toContain("Allow");
    expect(html).not.toMatch(/badge-sm[^>]*>TCP<\/span>/);
    expect(html).not.toMatch(/badge-sm[^>]*>UDP<\/span>/);
    expect(html).toContain("text-warning-text");
    expect(html).toContain("line-through");
  });

  it("keeps port constraints readable and distinguishes source exclusions", () => {
    const html = renderToString(
      <CalicoNetworkPolicyDiagram
        spec={{
          types: ["Egress"],
          egress: [
            {
              protocol: "UDP",
              source: {
                ports: [8080],
                notPorts: [{ protocol: "UDP", port: 5353 }],
              },
              destination: {
                ports: [
                  { protocol: "TCP", port: 8080 },
                  { protocol: "TCP", port: 8443 },
                ],
                notPorts: [443],
              },
            },
          ],
        }}
      />,
    );

    for (const value of [
      "src:UDP/8080",
      "src:!UDP/5353",
      "TCP/8080",
      "TCP/8443",
      "!UDP/443",
    ]) {
      expect(html).toContain(value);
    }
    expect(html).not.toContain("dst:");
    expect(html).not.toContain("max-w-14");
    expect(html).not.toContain("truncate font-mono text-[8px]");
    expect(html).not.toMatch(/badge-sm[^>]*>UDP<\/span>/);
  });

  it("renders protocol-only rules as wildcard arrow labels", () => {
    const html = renderToString(
      <CalicoNetworkPolicyDiagram
        spec={{
          types: ["Ingress"],
          ingress: [
            {
              action: "Deny",
              protocol: "TCP",
              notProtocol: "UDP",
              source: { selector: "app == 'client'" },
            },
          ],
        }}
      />,
    );

    expect(html).toContain("TCP/*");
    expect(html).not.toMatch(/badge-sm[^>]*>TCP<\/span>/);
    expect(html).toContain("not");
    expect(html).toContain("UDP");
  });

  it("uses native peer colors with stable match priority", () => {
    const renderDestination = (destination: any) =>
      renderToString(
        <CalicoNetworkPolicyDiagram
          spec={{
            selector: "app == 'target'",
            types: ["Egress"],
            egress: [{ destination }],
          }}
        />,
      );

    const combined = renderDestination({
      selector: "app == 'workload'",
      namespaceSelector: "team == 'prod'",
      nets: ["10.0.0.0/8"],
    });
    const namespace = renderDestination({
      namespaceSelector: "team == 'prod'",
    });
    const network = renderDestination({ nets: ["10.0.0.0/8"] });
    const serviceAccount = renderDestination({
      serviceAccountSelector: "role == 'api'",
    });
    const unknown = renderDestination({ unsupportedMatch: "value" });

    expect(combined).toContain("border-emerald-500/30 bg-emerald-500/8");
    expect(combined).not.toContain("border-sky-500/30 bg-sky-500/8");
    expect(combined).not.toContain("border-amber-500/30 bg-amber-500/8");
    expect(namespace).toContain("border-sky-500/30 bg-sky-500/8");
    expect(network).toContain("border-amber-500/30 bg-amber-500/8");
    expect(serviceAccount).toContain("border-emerald-500/30 bg-emerald-500/8");
    expect(unknown).toContain("border-theme-border bg-theme-elevated/50");
    expect(combined).toContain("border-indigo-500/30 bg-indigo-500/8");
  });

  it("does not imply deny-all when a policy has no explicit rules", () => {
    const html = renderToString(
      <CalicoNetworkPolicyDiagram spec={{ types: ["Ingress"], ingress: [] }} />,
    );

    expect(html).toContain("No explicit");
    expect(html).toContain("ingress");
    expect(html).not.toContain("Deny all");
  });

  it("renders staged rules as non-enforced dashed paths", () => {
    const html = renderToString(
      <CalicoNetworkPolicyDiagram
        staged
        spec={{
          selector: "app == 'api'",
          types: ["Ingress"],
          ingress: [{ action: "Allow", source: {} }],
        }}
      />,
    );

    expect(html).toContain("Staged preview");
    expect(html).toContain("not enforced");
    expect(html).toContain('stroke-dasharray="4 3"');
    expect(html).toContain("Allow");
    expect(html).not.toContain("Rule 1");
  });
});
