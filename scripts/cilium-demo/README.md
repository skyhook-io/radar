# Cilium / Hubble demo cluster

`./scripts/cilium-demo.sh` (or `make cilium-demo`) bootstraps a kind cluster
with Cilium + Hubble Relay and a curl→nginx workload pair, so Radar's Hubble
traffic source can be exercised against real flows in **every connection lane
it has**. Radar connects to hubble-relay directly over the Pod network when it
can (no `pods/portforward` RBAC needed), and falls back to a managed
port-forward when it can't — this demo makes each lane reachable on demand.

## Coverage matrix

| Mode | Command | Lane exercised |
|------|---------|----------------|
| Plaintext relay (default) | `up` | Direct dial, plaintext gRPC — the common self-managed Cilium configuration. Off-cluster (local Radar) the ClusterIP doesn't route, so the same mode also exercises the port-forward fallback + the background reachability probe. |
| TLS relay | `tls` | Direct dial, TLS lane **with SAN discovery**: the relay server cert's SAN is `*.hubble-relay.cilium.io`, not the k8s service DNS name, so Radar's first TLS attempt fails verification and it must probe the cert for the real name and retry. This is the AKS-shaped path. `plaintext` switches back. |
| Blocked direct path | `netpol` | Port-forward fallback *from in-cluster*, and the both-lanes-failed error contract: with the chart's default RBAC the connect error must name `rbac.portForward=true` **and** the network remediation. `netpol-off` removes the policy. |
| In-cluster Radar, default RBAC | `install-radar` | Builds the **current tree** into an image, loads it, installs `deploy/helm/radar` with default values, and verifies the SA cannot `create pods/portforward` — proving Hubble traffic needs no extra grant. |

## Typical flows

Local UI work against real flows:

```bash
make cilium-demo
kubectl config use-context kind-radar-cilium-demo
./scripts/visual-test-start.sh      # or run radar locally; open the Traffic view
```

In-cluster verification of a Hubble change (the #1099 regression check):

```bash
./scripts/cilium-demo.sh up
./scripts/cilium-demo.sh install-radar
kubectl --context kind-radar-cilium-demo -n radar port-forward deploy/radar 9280:9280 &
curl -s -X POST localhost:9280/api/traffic/connect
# expect: {"connected":true,"address":"<ClusterIP>:80",...} — a direct connection,
# with the SA denied pods/portforward
```

Failure-surface check:

```bash
./scripts/cilium-demo.sh netpol
kubectl --context kind-radar-cilium-demo -n radar rollout restart deploy/radar
# reconnect from the Traffic view; the error must carry both remediations
```

## Notes, learned the hard way

- **The relay Service port is 80 → named targetPort `grpc` → container 4245.**
  The direct lane must dial the *service* port; port-forward must target the
  *container* port. A change that conflates the two breaks exactly one lane and
  passes the other, which is easy to misread as flake.
- **Even a plaintext relay install ships `hubble-relay-client-certs`** in
  kube-system (the relay's own client certs for its Hubble peers). Radar loads
  the secret and still connects plaintext — TLS material existing does not mean
  the relay serves TLS. Don't "fix" that.
- **`kubectl port-forward` bypasses NetworkPolicy.** The stream enters through
  the kubelet/container runtime, not the pod network, so `netpol` blocks the
  direct lane while leaving the fallback fully working. That asymmetry is the
  entire point of the mode, not a hole in the policy.
- **Radar caches a failed direct-reachability probe per relay address.** After
  `netpol-off`, the first reconnect may still take the fallback; the background
  re-probe restores the direct lane on the connect after that. Not a bug —
  don't chase it.
- **`tls` restarts the relay pod** (cilium upgrade). Give it a few seconds
  before reconnecting; a connect that races the rollout exercises the
  stale-probe retry path, which is legitimate but not what you set out to test.
