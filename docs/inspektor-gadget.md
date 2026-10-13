# Inspektor Gadget Live Debug

Radar's Live Debug uses an existing Inspektor Gadget DaemonSet to collect kernel
evidence for a Pod. Processes and connections are snapshots; DNS captures last
10, 30, or 60 seconds in the UI. Processes and DNS cover the selected container.
Connections cover the Pod network namespace, including sidecars, without
container attribution; host-network Pod connections are unavailable. Radar does
not install Gadget.

## Security boundary

Captures use the current user's Kubernetes credentials: the kubeconfig identity
in local mode, or the impersonated user in an authenticated installation.

Inspektor Gadget's authorization boundary is node-wide, not workload-wide. An
identity that can proxy to a Gadget Pod can ask that daemon to instrument other
workloads on the same node. Pod-level RBAC does not narrow this. Treat Gadget
proxy access as privileged diagnostic access.

Grant capture users the following access in the Gadget namespace:

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

Discovery needs cluster-wide `list daemonsets` access. Radar first uses its own
identity, then tries the capture user's identity if detection is unknown. If
neither can verify the installation, Live Debug reports `unknown` and captures
are unavailable.

Bind this Role to operators trusted with node-level diagnostics. Capture users
also need `get pods` in the target workload namespace.

## Artifact policy

Gadget images are pinned by digest. To use a mirror, set `--ig-gadget-registry`
and allow those mirrored references in the daemon's artifact policy. A denied
or failed pull fails the capture; Radar does not substitute another artifact.

## Compatibility

Detection of an installed DaemonSet does not establish node compatibility.
The target node must have a Ready Gadget Pod; capture errors report runtime
compatibility failures.

Process IDs and parent process IDs refer to the host PID namespace, not the
PID values inside a container.

A partial capture or unknown event loss does not establish that all activity
was observed. Unanswered DNS queries mean no response was observed during the
capture, not a proven timeout.

## Choosing evidence

Open **Live Debug** in Pod details, choose an aspect and container, then capture.
Captures are unavailable through Radar Cloud's read-only connection.
For MCP invocation and result fields, see [Instrumentation and Write Tools](mcp.md#instrumentation-and-write-tools).
