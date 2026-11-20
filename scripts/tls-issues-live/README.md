# Live check: TLS-certificate issues

`check.py` tests the precision of Radar's TLS-certificate `issues` rows
against what a real controller serves. Unit fixtures encode Radar's model of
how controllers pick a certificate. This check takes its expected answer from
the controller instead: for every test host it completes a TLS handshake,
fingerprints the leaf certificate the controller presents, and matches it to
the fixture Secret it came from.

- **Precision (pass/fail):** every Radar TLS row must name a Secret that the
  controller serves for some test host and that is invalid: expired, not yet
  valid, or within 24 hours of expiry.
- **Recall (informational):** served invalid Secrets that Radar did not
  report. Radar deliberately leaves ambiguous cases to audit, so a miss is
  not a failure.

## What it creates

One namespace `radar-tls-check-<id>` holding an echo Deployment and Service,
self-signed TLS Secrets with chosen validity windows and names, and Ingresses
or one Gateway plus HTTPRoute. The namespace is deleted at the end unless you
pass `--keep`. The Gateway is annotated to get a ClusterIP Service, so no
cloud load balancer is created. Ingresses use the existing controller and its
existing Service.

Ingress scenarios: expired, not yet valid, expiring within hours, wildcard,
a certificate that doesn't cover the host, two Ingresses sharing a host
(older expired, newer valid), and a valid wildcard entry before an expired
exact one. Gateway scenarios: a listener with a hostname and one without.

## Running

Use the designated test clusters only. Start Radar from the build under
test against the same context, with an isolated `HOME` and a port you own:

```bash
HOME=$(mktemp -d) ./radar --context <ctx> --port <p> --no-browser &
```

Then:

```bash
# Traefik behind a ClusterIP Service (radar-test-nonprod)
python3 scripts/tls-issues-live/check.py --context radar-test-nonprod \
  --radar http://127.0.0.1:<p> --mode ingress --ingress-class traefik \
  --controller-service traefik/traefik --controller-port 443

# ingress-nginx (radar-test-management)
python3 scripts/tls-issues-live/check.py --context radar-test-management \
  --radar http://127.0.0.1:<p> --mode ingress --ingress-class nginx \
  --controller-service ingress-nginx/ingress-nginx-controller --controller-port 443

# Istio Gateway (radar-test-nonprod)
python3 scripts/tls-issues-live/check.py --context radar-test-nonprod \
  --radar http://127.0.0.1:<p> --mode gateway --gateway-class istio
```

Exit status 1 means a Radar row names a Secret the controller doesn't serve,
or one that is valid. Exit status 2 means a handshake failed, so the
controller's answer is unknown and the run proves nothing.
