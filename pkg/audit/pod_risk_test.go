package audit

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodRisks(t *testing.T) {
	yes, no := true, false
	root, user := int64(0), int64(1000)
	always := corev1.ContainerRestartPolicyAlways
	nonRoot := &corev1.PodSecurityContext{RunAsNonRoot: &yes}
	type risk struct {
		factor  PodRiskFactor
		checkID string
		message string
	}
	cases := []struct {
		name string
		spec corev1.PodSpec
		want []risk
	}{
		{
			name: "no user set: the image's USER decides, so may",
			spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			want: []risk{{PodRiskMayRunAsRoot, "runAsRoot", `Container "app" may run as root (runAsNonRoot not set)`}},
		},
		{
			name: "runAsNonRoot false still leaves it to the image",
			spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &no}}}},
			want: []risk{{PodRiskMayRunAsRoot, "runAsRoot", `Container "app" may run as root (runAsNonRoot not set)`}},
		},
		{
			name: "runAsUser 0 from the Pod runs as root",
			spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{RunAsUser: &root}, Containers: []corev1.Container{{Name: "app"}}},
			want: []risk{{PodRiskRunsAsRoot, "runAsRoot", `Container "app" runs as root (runAsUser: 0)`}},
		},
		{
			name: "a container's UID overrides the Pod's",
			spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{RunAsUser: &root}, Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsUser: &user}}}},
		},
		{
			name: "runAsNonRoot on the Pod rules root out",
			spec: corev1.PodSpec{SecurityContext: nonRoot, Containers: []corev1.Container{{Name: "app"}}},
		},
		{
			name: "privileged, host namespaces and a sensitive mount",
			spec: corev1.PodSpec{
				HostNetwork: true, HostPID: true, HostIPC: true, SecurityContext: nonRoot,
				Containers: []corev1.Container{{Name: "agent", SecurityContext: &corev1.SecurityContext{Privileged: &yes}, VolumeMounts: []corev1.VolumeMount{{Name: "sock"}, {Name: "logs"}}}},
				Volumes: []corev1.Volume{
					{Name: "sock", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/containerd/containerd.sock"}}},
					{Name: "logs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log/pods"}}},
				},
			},
			want: []risk{
				{PodRiskHostAccess, "hostNetwork", "Pod uses host network"},
				{PodRiskHostAccess, "hostPID", "Pod uses host PID namespace"},
				{PodRiskHostAccess, "hostIPC", "Pod uses host IPC namespace"},
				{PodRiskPrivileged, "privileged", `Container "agent" runs in privileged mode`},
				{PodRiskHostAccess, "dockerSocketMount", `Volume "sock" mounts container runtime socket /run/containerd/containerd.sock`},
				{PodRiskHostAccess, "sensitiveHostPath", `Volume "logs" mounts sensitive host path /var/log/pods`},
			},
		},
		{
			name: "a host path no running container mounts gives it nothing",
			spec: corev1.PodSpec{
				SecurityContext: nonRoot,
				InitContainers:  []corev1.Container{{Name: "chown", VolumeMounts: []corev1.VolumeMount{{Name: "etc"}}}},
				Containers:      []corev1.Container{{Name: "app"}},
				Volumes:         []corev1.Volume{{Name: "etc", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc"}}}},
			},
		},
		{
			name: "an init container that finishes isn't read; a sidecar is",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Name: "setup", SecurityContext: &corev1.SecurityContext{Privileged: &yes, RunAsUser: &root}},
					{Name: "proxy", RestartPolicy: &always, SecurityContext: &corev1.SecurityContext{Privileged: &yes, RunAsNonRoot: &yes}},
				},
				Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &yes}}},
			},
			want: []risk{{PodRiskPrivileged, "privileged", `Container "proxy" runs in privileged mode`}},
		},
		{
			name: "a host path outside the checks' list isn't host access",
			spec: corev1.PodSpec{
				SecurityContext: nonRoot,
				Containers:      []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{{Name: "data"}}}},
				Volumes:         []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data/app"}}}},
			},
		},
		{
			name: "a Windows Pod has no root factor",
			spec: corev1.PodSpec{OS: &corev1.PodOS{Name: corev1.Windows}, Containers: []corev1.Container{{Name: "app"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Escalation is left unset in these specs, so each running
			// container allows it; TestPodRisksOptionalFactors reads it.
			got := slices.DeleteFunc(PodRisks(tc.spec), func(r PodRisk) bool { return r.Factor == PodRiskPrivilegeEscalation })
			if len(got) != len(tc.want) {
				t.Fatalf("PodRisks = %+v, want %+v", got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].Factor != w.factor || got[i].CheckID != w.checkID || got[i].Message != w.message {
					t.Errorf("risk %d = %+v, want %+v", i, got[i], w)
				}
			}
		})
	}
}

// A risk factor names a check, in that check's words, so the Checks page and
// the vulnerability rows say the same thing of the same spec.
func TestPodRisksSpeakAsTheChecks(t *testing.T) {
	yes := true
	spec := corev1.PodSpec{
		HostNetwork: true,
		Containers:  []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{Privileged: &yes}, VolumeMounts: []corev1.VolumeMount{{Name: "etc"}}}},
		Volumes:     []corev1.Volume{{Name: "etc", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc"}}}},
	}
	var findings []Finding
	tr := newEvalTracker()
	findings = append(findings, checkPodSpecSecurity(tr, "Deployment", "ns", "web", spec, nil, false)...)
	findings = append(findings, checkPodSpecVolumes(tr, "Deployment", "ns", "web", spec)...)
	said := map[string]bool{}
	for _, f := range findings {
		said[f.CheckID+"\x00"+f.Message] = true
	}
	spec.Containers[0].SecurityContext.Capabilities = &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN", "net_raw", "CHOWN"}}
	spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 80, HostPort: 8080}}
	findings = append(findings, checkPodSpecSecurity(tr, "Deployment", "ns", "web", spec, nil, false)...)
	for _, f := range findings {
		said[f.CheckID+"\x00"+f.Message] = true
	}
	for _, r := range PodRisks(spec) {
		if r.Factor == PodRiskHostPort {
			// No Best practices check flags a host port.
			if r.CheckID != "" {
				t.Errorf("host port names check %q", r.CheckID)
			}
			continue
		}
		if _, ok := CheckRegistry[r.CheckID]; !ok {
			t.Errorf("%s names no registered check", r.CheckID)
		}
		if !said[r.CheckID+"\x00"+r.Message] {
			t.Errorf("risk %q (%s) isn't what the check says", r.Message, r.CheckID)
		}
	}
}

// The optional factors: added capabilities in the capability checks' words
// (both lists), privilege escalation unless explicitly false, and host ports.
func TestPodRisksOptionalFactors(t *testing.T) {
	no, yes := false, true
	always := corev1.ContainerRestartPolicyAlways
	spec := corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes},
		InitContainers: []corev1.Container{
			{Name: "setup", SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"ALL"}}}},
			{Name: "proxy", RestartPolicy: &always, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no}, Ports: []corev1.ContainerPort{{ContainerPort: 15001, HostPort: 15001}}},
		},
		Containers: []corev1.Container{
			{Name: "app", SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "SYS_PTRACE", "CHOWN"}}}},
			{Name: "debug", SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &yes}, Ports: []corev1.ContainerPort{{ContainerPort: 80}}},
		},
	}
	type risk struct {
		factor  PodRiskFactor
		checkID string
		message string
	}
	var got []risk
	for _, r := range PodRisks(spec) {
		got = append(got, risk{r.Factor, r.CheckID, r.Message})
	}
	want := []risk{
		{PodRiskDangerousCapabilities, "dangerousCapabilities", `Container "app" adds dangerous capability NET_ADMIN`},
		{PodRiskDangerousCapabilities, "insecureCapabilities", `Container "app" adds insecure capability SYS_PTRACE`},
		{PodRiskPrivilegeEscalation, "privilegeEscalation", `Container "debug" allows privilege escalation`},
		{PodRiskHostPort, "", `Container "proxy" binds host port 15001`},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PodRisks:\n got %+v\nwant %+v", got, want)
	}
}
