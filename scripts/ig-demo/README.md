# Inspektor Gadget demo fixture

`scripts/ig-demo.sh up` creates or reuses the `radar-ig-demo` cluster (context
`kind-radar-ig-demo`), installs the official Inspektor Gadget chart (v0.54.1 by
default), and applies `fixtures.yaml`.

The `probe` Pod continuously produces:

- successful and NXDOMAIN DNS responses;
- HTTP connections to the `server` Service;
- a multi-container target with an explicit default container.

Use the `server` Pod for process and listening-socket snapshots.

An installed Gadget DaemonSet does not guarantee a successful capture. Use a
cluster whose nodes support Gadget instrumentation to test live evidence; use
an incompatible node to test capture-error presentation.
