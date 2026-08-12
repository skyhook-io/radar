# Inspektor Gadget Live Debug

Radar's Live Debug feature uses an existing Inspektor Gadget DaemonSet to
collect bounded kernel evidence for one Pod container. Radar never installs
Gadget from the UI or status endpoint. Processes and connections are one-shot
snapshots; DNS is a 10, 30, or 60 second capture.

## Security boundary

The Kubernetes API server authorizes the Gadget Pod proxy connection using the
current Radar user's impersonated identity. In local mode it uses the current
kubeconfig identity. A run fails closed if Radar cannot construct that config.

Inspektor Gadget's authorization boundary is node-wide, not workload-wide. An
identity that can proxy to a Gadget Pod can ask that daemon to instrument other
workloads on the same node. Pod-level RBAC does not narrow this. Treat Gadget
proxy access as privileged diagnostic access.

The minimum namespace-scoped grant verified against Inspektor Gadget v0.54.1 is:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: radar-live-debug
  namespace: gadget
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]
```

Bind this only to operators who may collect node-level kernel evidence. Radar
also needs ordinary `get pods` access in the target workload namespace to pin
the Pod UID, node, container name, and runtime container ID.

## Artifact policy

Radar pins the three curated OCI gadgets by multi-platform manifest digest for
each release. `--ig-gadget-registry` changes only the registry prefix for a
mirror; it does not fall back to tags or another digest. Configure Gadget's
daemon-side allowed-gadgets and signature policy to allow those exact mirrored
references. Radar surfaces policy and pull errors without substituting a less
restricted artifact.

## Compatibility

An installed DaemonSet does not prove that every node can load eBPF programs.
Live Debug checks that the target node's Gadget Pod is Ready, then preserves the
runtime's concrete compatibility error. Linux nodes generally need kernel 5.10+
with BTF. GKE Autopilot, Fargate, and Docker Desktop kernels without BTF may be
unsupported even though detection reports `installed`.

Every result records the pinned Pod UID, node, container ID, capture window,
row/event count, Radar truncation, and whether IG-side event loss was measurable.
DNS absence is phrased as "no response observed in the capture window," never as
a proven timeout.
