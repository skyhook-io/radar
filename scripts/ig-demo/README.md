# Inspektor Gadget demo fixture

`scripts/ig-demo.sh up` creates or reuses `kind-radar-ig-demo`, installs the
official Inspektor Gadget v0.54.1 chart, and applies `fixtures.yaml`.

The `probe` Pod continuously produces:

- successful and NXDOMAIN DNS responses;
- HTTP connections to the `server` Service;
- a multi-container target with an explicit default container.

The `server` Pod supplies a stable process tree and listening sockets.

Kind nodes inherit the host kernel. Docker Desktop's LinuxKit kernel may not
expose BTF, in which case installation/detection succeeds but a capture returns
the expected compatibility error. Run the same fixtures on a BTF-capable Linux
test cluster for live evidence and use the local cluster for the unsupported-host
state.
