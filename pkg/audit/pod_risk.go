package audit

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// PodRiskFactor is a Pod setting that widens what a compromised container
// reaches: the Pod side of a vulnerability's risk, as Prisma Cloud's risk
// factors and Wiz's toxic combinations pair the two.
type PodRiskFactor string

const (
	// PodRiskPrivileged: a container that keeps running sets privileged.
	PodRiskPrivileged PodRiskFactor = "privileged"
	// PodRiskHostAccess: the Pod shares the host's network, PID or IPC
	// namespace, or a running container mounts a host path the
	// sensitiveHostPath or dockerSocketMount check flags.
	PodRiskHostAccess PodRiskFactor = "hostAccess"
	// PodRiskRunsAsRoot: the effective runAsUser is 0.
	PodRiskRunsAsRoot PodRiskFactor = "runsAsRoot"
	// PodRiskMayRunAsRoot: nothing in the spec rules root out, and no UID is
	// set, so the image's USER decides. Radar doesn't read the image config,
	// so it can't say which.
	PodRiskMayRunAsRoot PodRiskFactor = "mayRunAsRoot"
	// PodRiskDangerousCapabilities: a running container adds a capability
	// the dangerousCapabilities or insecureCapabilities check flags.
	PodRiskDangerousCapabilities PodRiskFactor = "dangerousCapabilities"
	// PodRiskPrivilegeEscalation: a running container doesn't set
	// allowPrivilegeEscalation: false (the privilegeEscalation check). Most
	// specs leave it unset, so most workloads carry it.
	PodRiskPrivilegeEscalation PodRiskFactor = "privilegeEscalation"
	// PodRiskHostPort: a running container binds a port on the node. No
	// Best practices check flags it.
	PodRiskHostPort PodRiskFactor = "hostPort"
)

// PodRisk is one setting behind a factor: the check that flags it on the
// Checks page, and its message there.
type PodRisk struct {
	Factor  PodRiskFactor
	CheckID string
	Message string
}

// PodRisks are the risk factors of a Pod spec's containers that keep
// running: its containers and restartable (sidecar) init containers. An init
// container that runs to completion is gone before the workload serves, so
// unlike the checks, which audit the whole spec, it isn't read. The rules and
// words are the checks' own (checkPodSpecSecurity, checkPodSpecVolumes),
// except that an explicit runAsUser of 0 reads as runs as root rather than
// may. A Windows Pod (spec.os, or a node selector for Windows nodes) has no
// root or privilege escalation factor: its user is runAsUserName, and
// allowPrivilegeEscalation can't be set there.
func PodRisks(spec corev1.PodSpec) []PodRisk {
	var out []PodRisk
	add := func(factor PodRiskFactor, checkID, msg string) {
		out = append(out, PodRisk{Factor: factor, CheckID: checkID, Message: msg})
	}
	if spec.HostNetwork {
		add(PodRiskHostAccess, "hostNetwork", msgHostNetwork)
	}
	if spec.HostPID {
		add(PodRiskHostAccess, "hostPID", msgHostPID)
	}
	if spec.HostIPC {
		add(PodRiskHostAccess, "hostIPC", msgHostIPC)
	}
	running := slices.Clone(spec.Containers)
	for _, c := range spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			running = append(running, c)
		}
	}
	mounted := map[string]bool{}
	windows := (spec.OS != nil && spec.OS.Name == corev1.Windows) || spec.NodeSelector[corev1.LabelOSStable] == string(corev1.Windows)
	for i := range running {
		c := &running[i]
		for _, m := range c.VolumeMounts {
			mounted[m.Name] = true
		}
		if !windows {
			switch rootUser(c.SecurityContext, spec.SecurityContext) {
			case rootSet:
				add(PodRiskRunsAsRoot, "runAsRoot", msgRunsAsRoot(c.Name))
			case rootPossible:
				add(PodRiskMayRunAsRoot, "runAsRoot", msgMayRunAsRoot(c.Name))
			}
		}
		if isPrivileged(c.SecurityContext) {
			add(PodRiskPrivileged, "privileged", msgPrivileged(c.Name))
		}
		for _, a := range addedCapabilities(c) {
			add(PodRiskDangerousCapabilities, a.checkID, a.msg)
		}
		if !windows && allowsEscalation(c.SecurityContext) {
			add(PodRiskPrivilegeEscalation, "privilegeEscalation", msgEscalation(c.Name))
		}
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				add(PodRiskHostPort, "", fmt.Sprintf("Container %q binds host port %d", c.Name, p.HostPort))
			}
		}
	}
	for _, v := range spec.Volumes {
		if !mounted[v.Name] {
			continue
		}
		if checkID, _, msg, ok := hostPathFinding(v); ok {
			add(PodRiskHostAccess, checkID, msg)
		}
	}
	return out
}

const (
	msgHostNetwork = "Pod uses host network"
	msgHostPID     = "Pod uses host PID namespace"
	msgHostIPC     = "Pod uses host IPC namespace"
)

func msgMayRunAsRoot(container string) string {
	return fmt.Sprintf("Container %q may run as root (runAsNonRoot not set)", container)
}

func msgRunsAsRoot(container string) string {
	return fmt.Sprintf("Container %q runs as root (runAsUser: 0)", container)
}

func msgPrivileged(container string) string {
	return fmt.Sprintf("Container %q runs in privileged mode", container)
}

func msgEscalation(container string) string {
	return fmt.Sprintf("Container %q allows privilege escalation", container)
}

// allowsEscalation is the privilegeEscalation check's rule: anything but an
// explicit false allows it.
func allowsEscalation(sc *corev1.SecurityContext) bool {
	return sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation
}

type capabilityFinding struct{ checkID, severity, msg string }

// addedCapabilities are what the capability checks say of a container's
// added capabilities: dangerousCapabilities for near-root ones,
// insecureCapabilities for those that enable spoofing or process injection.
func addedCapabilities(c *corev1.Container) []capabilityFinding {
	sc := c.SecurityContext
	if sc == nil || sc.Capabilities == nil {
		return nil
	}
	var out []capabilityFinding
	for _, cap := range sc.Capabilities.Add {
		switch strings.ToUpper(string(cap)) {
		case "SYS_ADMIN", "NET_ADMIN", "ALL":
			out = append(out, capabilityFinding{"dangerousCapabilities", SeverityDanger, fmt.Sprintf("Container %q adds dangerous capability %s", c.Name, cap)})
		case "NET_RAW", "SYS_PTRACE", "MKNOD", "DAC_OVERRIDE":
			out = append(out, capabilityFinding{"insecureCapabilities", SeverityWarning, fmt.Sprintf("Container %q adds insecure capability %s", c.Name, cap)})
		}
	}
	return out
}

func isPrivileged(sc *corev1.SecurityContext) bool {
	return sc != nil && sc.Privileged != nil && *sc.Privileged
}

type rootState int

const (
	rootNo rootState = iota
	rootPossible
	rootSet
)

// rootUser is whether a container runs as root: set when the merged
// runAsUser is 0, possible when effectivelyNonRoot doesn't rule it out
// otherwise.
func rootUser(sc *corev1.SecurityContext, podSC *corev1.PodSecurityContext) rootState {
	if effectivelyNonRoot(sc, podSC) {
		return rootNo
	}
	var runAsUser *int64
	if podSC != nil {
		runAsUser = podSC.RunAsUser
	}
	if sc != nil && sc.RunAsUser != nil {
		runAsUser = sc.RunAsUser
	}
	if runAsUser != nil && *runAsUser == 0 {
		return rootSet
	}
	return rootPossible
}

// hostPathFinding is what the volume checks say of a hostPath volume: the
// container runtime socket, the host's root filesystem, or one of
// sensitiveHostPaths. ok is false for any other volume.
func hostPathFinding(v corev1.Volume) (checkID, severity, msg string, ok bool) {
	if v.HostPath == nil {
		return "", "", "", false
	}
	p := v.HostPath.Path
	if strings.Contains(p, "docker.sock") || strings.Contains(p, "containerd.sock") || strings.Contains(p, "crio.sock") {
		return "dockerSocketMount", SeverityDanger, fmt.Sprintf("Volume %q mounts container runtime socket %s", v.Name, p), true
	}
	if p == "/" {
		return "sensitiveHostPath", SeverityDanger, fmt.Sprintf("Volume %q mounts the entire host root filesystem", v.Name), true
	}
	for _, prefix := range sensitiveHostPaths {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return "sensitiveHostPath", SeverityWarning, fmt.Sprintf("Volume %q mounts sensitive host path %s", v.Name, p), true
		}
	}
	return "", "", "", false
}
