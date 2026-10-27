#!/usr/bin/env python3
"""Live differential check for Radar's TLS-certificate and Traefik issues.

Applies throwaway fixtures to a real cluster, asks the controller which
certificate it actually serves for each host (TLS handshake through a
port-forward, leaf fingerprint compared with every fixture Secret), and
compares that with what a running Radar reports. The expected answer comes
from the controller, never from Radar's model: every Radar TLS row must name
a Secret the controller serves and that is invalid (precision), and every
served invalid Secret Radar misses is listed (recall, informational).

Read README.md first. Requires: kubectl, python3 with `cryptography`.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
import secrets
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID

NOW = dt.datetime.now(dt.timezone.utc)
DAY = dt.timedelta(days=1)
HOUR = dt.timedelta(hours=1)


def make_cert(names, not_before, not_after):
    key = ec.generate_private_key(ec.SECP256R1())
    cert = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, names[0])]))
        .issuer_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "radar-tls-live-check")]))
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(not_before)
        .not_valid_after(not_after)
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(n) for n in names]), critical=False)
        .sign(key, hashes.SHA256())
    )
    cert_pem = cert.public_bytes(serialization.Encoding.PEM)
    key_pem = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
    return cert_pem, key_pem, hashlib.sha256(cert.public_bytes(serialization.Encoding.DER)).hexdigest()


def kubectl(ctx, *args, stdin=None, check=True):
    r = subprocess.run(["kubectl", "--context", ctx, *args], input=stdin, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit(f"kubectl {' '.join(args)} failed: {r.stderr.strip()}")
    return r.stdout


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def served_fingerprint(port, host):
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
            with ctx.wrap_socket(sock, server_hostname=host) as tls:
                return hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()
    except OSError as e:
        return f"error: {e}"


# Each scenario: secrets {name: (sans, not_before, not_after)} and the
# objects using them. Hosts live under <run>.example.test.
def ingress_scenarios(run):
    h = lambda s: f"{s}.{run}.example.test"
    expired = lambda *names: (list(names), NOW - 90 * DAY, NOW - DAY)
    valid = lambda *names: (list(names), NOW - HOUR, NOW + 90 * DAY)
    return {
        "expired": {"secrets": {"expired": expired(h("expired"))}, "ingresses": [("expired", h("expired"), ["expired"], 0)]},
        "not-yet-valid": {"secrets": {"future": (["" + h("future")], NOW + 2 * DAY, NOW + 90 * DAY)}, "ingresses": [("future", h("future"), ["future"], 0)]},
        "expiring": {"secrets": {"soon": ([h("soon")], NOW - 60 * DAY, NOW + 3 * HOUR)}, "ingresses": [("soon", h("soon"), ["soon"], 0)]},
        "wildcard": {"secrets": {"wild": expired(f"*.{run}.example.test")}, "ingresses": [("wild", h("wild"), ["wild"], 0)]},
        # The certificate doesn't cover the host: the controller serves something else.
        "uncovered": {"secrets": {"other-host": expired(h("elsewhere"))}, "ingresses": [("uncovered", h("uncovered"), ["other-host"], 0)]},
        # Two Ingresses, one host: older expired, newer valid. Which is served is the controller's call.
        "shared-host": {
            "secrets": {"shared-old": expired(h("shared")), "shared-new": valid(h("shared"))},
            "ingresses": [("shared-old", h("shared"), ["shared-old"], 0), ("shared-new", h("shared"), ["shared-new"], 5)],
        },
        # A valid wildcard entry before an expired exact one in the same Ingress.
        "exact-after-wildcard": {
            "secrets": {"exact-wild": valid(f"*.{run}.example.test"), "exact": expired(h("exact"))},
            "ingresses": [("exact", h("exact"), ["exact-wild", "exact"], 0)],
        },
    }


def ingress_manifest(ns, name, host, secret_names, ingress_class, path="/"):
    tls = [{"hosts": [host], "secretName": s} for s in secret_names]
    return {
        "apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
        "metadata": {"name": name, "namespace": ns},
        "spec": {
            "ingressClassName": ingress_class, "tls": tls,
            "rules": [{"host": host, "http": {"paths": [{"path": path, "pathType": "Prefix", "backend": {"service": {"name": "echo", "port": {"number": 80}}}}]}}],
        },
    }


def gateway_manifests(ns, run, gateway_class):
    h = lambda s: f"{s}.{run}.example.test"
    expired = lambda *names: (list(names), NOW - 90 * DAY, NOW - DAY)
    secrets_ = {"gw-expired": expired(h("gw")), "gw-any": expired(h("any"))}
    gw = {
        "apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
        "metadata": {"name": "tls-check", "namespace": ns, "annotations": {"networking.istio.io/service-type": "ClusterIP"}},
        "spec": {"gatewayClassName": gateway_class, "listeners": [
            {"name": "named", "protocol": "HTTPS", "port": 443, "hostname": h("gw"), "tls": {"certificateRefs": [{"name": "gw-expired"}]}, "allowedRoutes": {"namespaces": {"from": "Same"}}},
            {"name": "any", "protocol": "HTTPS", "port": 8443, "tls": {"certificateRefs": [{"name": "gw-any"}]}, "allowedRoutes": {"namespaces": {"from": "Same"}}},
        ]},
    }
    route = {
        "apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
        "metadata": {"name": "tls-check", "namespace": ns},
        "spec": {"parentRefs": [{"name": "tls-check"}], "rules": [{"backendRefs": [{"name": "echo", "port": 80}]}]},
    }
    probes = [(h("gw"), 443), (h("any"), 8443)]
    return secrets_, [gw, route], probes


ECHO = [
    {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "echo"}, "spec": {"replicas": 1, "selector": {"matchLabels": {"app": "echo"}}, "template": {"metadata": {"labels": {"app": "echo"}}, "spec": {"containers": [{"name": "echo", "image": "registry.k8s.io/e2e-test-images/agnhost:2.53", "args": ["netexec", "--http-port=8080"], "ports": [{"containerPort": 8080}]}]}}}},
    {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "echo"}, "spec": {"selector": {"app": "echo"}, "ports": [{"port": 80, "targetPort": 8080}]}},
]


def apply(ctx, ns, objs):
    for o in objs:
        o.setdefault("metadata", {}).setdefault("namespace", ns)
    kubectl(ctx, "apply", "-f", "-", stdin=json.dumps({"apiVersion": "v1", "kind": "List", "items": objs}))


def tls_secret(ns, name, cert_pem, key_pem):
    import base64
    return {"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls", "metadata": {"name": name, "namespace": ns},
            "data": {"tls.crt": base64.b64encode(cert_pem).decode(), "tls.key": base64.b64encode(key_pem).decode()}}


def invalid(nb, na):
    return NOW < nb - dt.timedelta(minutes=5) or NOW >= na or na - NOW <= DAY


def radar_rows(radar, ns):
    url = f"{radar.rstrip('/')}/api/issues?namespaces={ns}&limit=1000"
    with urllib.request.urlopen(url, timeout=60) as r:
        body = json.load(r)
    return [i for i in body.get("issues", []) if i.get("kind") == "Secret" and str(i.get("reason", "")).startswith("TLSCertificate")]


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--context", required=True)
    ap.add_argument("--radar", required=True, help="URL of a Radar running against the same context")
    ap.add_argument("--mode", required=True, choices=["ingress", "gateway"])
    ap.add_argument("--ingress-class", help="ingress mode: the IngressClass to use")
    ap.add_argument("--controller-service", help="ingress mode: <namespace>/<service> of the controller, probed on --controller-port")
    ap.add_argument("--controller-port", type=int, default=443)
    ap.add_argument("--gateway-class", default="istio")
    ap.add_argument("--keep", action="store_true", help="leave the namespace for inspection")
    args = ap.parse_args()

    run = "rtc-" + secrets.token_hex(3)
    ns = f"radar-tls-check-{run[4:]}"
    print(f"run {run}, namespace {ns}, context {args.context}")
    kubectl(args.context, "create", "namespace", ns)
    pf = None
    try:
        apply(args.context, ns, json.loads(json.dumps(ECHO)))
        fingerprints, validity, objs, probes = {}, {}, [], []
        if args.mode == "ingress":
            for scen in ingress_scenarios(run).values():
                for name, (sans, nb, na) in scen["secrets"].items():
                    c, k, fp = make_cert(sans, nb, na)
                    fingerprints[fp], validity[name] = name, (nb, na)
                    objs.append(tls_secret(ns, name, c, k))
            apply(args.context, ns, objs)
            for scen in ingress_scenarios(run).values():
                for ing_name, host, secret_names, delay in scen["ingresses"]:
                    time.sleep(delay)  # creation order decides some controllers' choice
                    # Distinct paths: ingress-nginx's webhook refuses a host and path
                    # another Ingress already defines.
                    apply(args.context, ns, [ingress_manifest(ns, ing_name, host, secret_names, args.ingress_class, "/" + ing_name)])
                    probes.append((host, args.controller_port))
            svc_ns, svc = args.controller_service.split("/")
            target = (svc_ns, f"svc/{svc}", args.controller_port)
        else:
            secrets_, gw_objs, probes = gateway_manifests(ns, run, args.gateway_class)
            for name, (sans, nb, na) in secrets_.items():
                c, k, fp = make_cert(sans, nb, na)
                fingerprints[fp], validity[name] = name, (nb, na)
                objs.append(tls_secret(ns, name, c, k))
            apply(args.context, ns, objs + gw_objs)
            kubectl(args.context, "-n", ns, "wait", "--for=condition=Programmed", "gateway/tls-check", "--timeout=180s")
            target = (ns, "svc/tls-check-istio", None)

        time.sleep(20)  # controllers and Radar's informers settle
        ports = sorted({p for _, p in probes})
        pf_ports = {p: free_port() for p in ports}
        pf = subprocess.Popen(["kubectl", "--context", args.context, "-n", target[0], "port-forward", target[1],
                               *[f"{pf_ports[p]}:{p}" for p in ports]], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                for p in pf_ports.values():
                    socket.create_connection(("127.0.0.1", p), timeout=2).close()
                break
            except OSError:
                time.sleep(1)

        served = {}
        for host, port in probes:
            fp = served_fingerprint(pf_ports[port], host)
            served[host] = fingerprints.get(fp, "(not a fixture certificate)" if not fp.startswith("error") else fp)
        broken = {s for s in served.values() if s in validity and invalid(*validity[s])}

        rows = radar_rows(args.radar, ns)
        reported = {r["name"]: r for r in rows}
        print("\nserved per host:")
        for host, s in sorted(served.items()):
            flag = " (invalid)" if s in broken else ""
            print(f"  {host:45s} -> {s}{flag}")
        print("\nradar TLS rows:")
        for name, r in sorted(reported.items()):
            print(f"  {name:20s} {r.get('severity'):9s} {r.get('reason')}")
        errors = sorted(h for h, s in served.items() if str(s).startswith("error"))
        if errors:
            print(f"\ninconclusive: no handshake for {errors}; the controller's answer is unknown")
            return 2
        false_rows = sorted(set(reported) - broken)
        missed = sorted(broken - set(reported))
        print(f"\nprecision: {'PASS' if not false_rows else 'FAIL'} false rows={false_rows}")
        print(f"recall (informational): missed={missed}")
        return 1 if false_rows else 0
    finally:
        if pf:
            pf.terminate()
        if args.keep:
            print(f"kept namespace {ns}")
        else:
            kubectl(args.context, "delete", "namespace", ns, "--wait=false", check=False)


if __name__ == "__main__":
    sys.exit(main())
