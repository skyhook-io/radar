# Experimental node image inspection

This prototype tests the temporary helper proposed in issue #1819. It exposes a
CLI command and returns the image filesystem as JSON. The Image Filesystem UI,
HTTP handlers and Helm chart do not expose this command yet.

Build the Radar binary with `go build -o radar ./cmd/explorer`, then run:

```sh
./radar node-image inspect \
  --kubeconfig test-kubeconfig \
  --namespace default \
  --pod example \
  --helper-image example/radar:prototype
```

The helper image must contain the same binary at `/radar`. The command overrides
the image entrypoint. The helper must be available to the node, either through a
registry or a local import. For k3s, pass
`--store /var/lib/rancher/k3s/agent/containerd/io.containerd.content.v1.content`.
That path is configurable; this prototype was tested on kind, not k3s.

The command reads the Pod's kubelet image ID, checks workload exec access, starts
a helper on the same node, and retrieves content through Kubernetes exec. It
verifies every downloaded blob against its SHA-256 digest and selects the node's
OS and architecture when resolving an image index. It reuses Radar's filesystem
layer processing and applies image deletions. Downloaded content and expanded
layers use temporary directories separate from the shared registry cache.

The helper mounts only the content directory, read-only. It has no runtime socket,
service account token, host network or host process namespace. It drops all Linux
capabilities, disables privilege escalation and has a read-only root filesystem.
It runs as UID 0 so it can traverse containerd's content directory. It can read
other image blobs on that node; the mount does not isolate a single image.

The CLI uses the caller's credentials for every API operation. It requires Pod
read access, workload exec access, node read access for platform selection, and
permission to create, read, exec into and delete the helper Pod in the workload
namespace. This is a testable CLI boundary, not a decision about the shared
server's ServiceAccount permissions. Baseline Pod Security rejects its hostPath.

The helper is deleted after success or failure. It also has a three-minute
execution deadline. A killed client can leave a terminated Pod object behind;
the deadline stops the process but does not delete the object. Its owner reference
allows collection when the workload Pod is deleted. A production implementation
needs a reliable cleanup policy for interrupted clients.

## Live results

Tested with Kubernetes 1.34.0 and containerd 2.1.3 on a disposable kind cluster.

- An ordinary `kind load docker-image` import produced a bare config digest in
  kubelet `imageID`. The content reader could fetch that config, but it could not
  use it as a manifest. Inspection failed explicitly.
- Registering an additional `repository@sha256:...` image name inside the test
  node made kubelet return a manifest-backed image ID for a newly created Pod.
  Inspection then succeeded without contacting a registry. This changes import
  metadata and is not evidence that ordinary imports work.
- The successful image used a nonexistent registry hostname and pull policy
  `Never`. Its filesystem contained 522 entries. A file added in one layer and
  removed in the next was absent from the result.
- One measured successful inspection took 2.26 seconds with the helper image
  already loaded. This is a single small-image sample, not a benchmark.
- A caller without workload exec permission was rejected before helper creation.
- Baseline Pod Security rejected the helper's hostPath mount.
- Deliberately removing one fixture layer blob made inspection fail explicitly.
  No helper Pods remained after success or these failure cases.

## Before integrating

Ordinary imported config IDs need a way to locate the corresponding manifest.
This may be possible by searching retained manifests in the content store; the
prototype does not perform that search. No socket requirement has been established.

A shared server integration must keep caller authorization separate from helper
execution, use operator-controlled helper images and content paths, audit access,
limit concurrent helpers, and define cleanup and registry fallback. The UI must
show the selected source. It also needs real k3s validation, image index variant
handling, private helper image credentials and admission-policy coverage beyond
the tested kind cluster.
