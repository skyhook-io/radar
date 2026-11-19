package images

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
)

const (
	Java4ShellCVE              = "CVE-2026-114938"
	java4ShellVBMILabel        = "feature.node.kubernetes.io/cpu-cpuid.AVX512VBMI"
	java4ShellRowLimit         = 100
	java4ShellDockerHubLookups = 24
)

var ErrJava4ShellBusy = errors.New("a Java4Shell check is already running")

type Java4ShellPod struct {
	Pod  *corev1.Pod
	Node *corev1.Node
}

type Java4ShellRegistryAccess struct {
	PullSecrets      []*corev1.Secret
	RadarCredentials bool
}

type Java4ShellFinding struct {
	Workload               resourceid.Ref `json:"workload"`
	Container              string         `json:"container"`
	Image                  string         `json:"image"`
	Digest                 string         `json:"digest"`
	Version                string         `json:"version"`
	FixedIn                string         `json:"fixedIn,omitempty"`
	Pods                   int            `json:"pods"`
	VBMIPods               int            `json:"vbmiPods"`
	UnknownCPUPods         int            `json:"unknownCPUPods"`
	MitigationDeclaredPods int            `json:"mitigationDeclaredPods"`
}

type Java4ShellResult struct {
	Findings         []Java4ShellFinding    `json:"findings"`
	Containers       int                    `json:"containers"`
	Matches          int                    `json:"matches"`
	FindingGroups    int                    `json:"findingGroups"`
	Unassessed       map[string]int         `json:"unassessed"`
	UnassessedRows   []Java4ShellUnassessed `json:"unassessedRows"`
	UnassessedGroups int                    `json:"unassessedGroups"`
}

type Java4ShellReport struct {
	Java4ShellResult
	Coverage resourceapi.Coverage `json:"coverage"`
	Workload *resourceid.Ref      `json:"workload,omitempty"`
}

type Java4ShellUnassessed struct {
	Workload  resourceid.Ref `json:"workload"`
	Container string         `json:"container"`
	Image     string         `json:"image"`
	Digest    string         `json:"digest,omitempty"`
	Reason    string         `json:"reason"`
	Pods      int            `json:"pods"`
}

type java4ShellContainer struct {
	pod                *corev1.Pod
	container          string
	image              string
	lookup             int
	reportedHash       string
	unassessedReason   string
	platformAssumed    bool
	cpu                string
	mitigationDeclared bool
}

type java4ShellGroup struct {
	workload                resourceid.Ref
	container, digest, java string
}

type java4ShellUnassessedGroup struct {
	workload                         resourceid.Ref
	container, image, digest, reason string
}

// CheckJava4Shell shares registry reads only among Pods with the same image,
// platform and credential grant. The caller authorizes and captures the
// credentials for each Pod before a private read.
func (r *RuntimeInventory) CheckJava4Shell(ctx context.Context, pods []Java4ShellPod, workloadFor func(context.Context, *corev1.Pod) resourceid.Ref, accessFor func(*corev1.Pod) Java4ShellRegistryAccess) (Java4ShellResult, error) {
	if !r.java4ShellActive.CompareAndSwap(false, true) {
		return Java4ShellResult{}, ErrJava4ShellBusy
	}
	defer r.java4ShellActive.Store(false)

	out := Java4ShellResult{
		Findings: []Java4ShellFinding{}, Unassessed: map[string]int{},
		UnassessedRows: []Java4ShellUnassessed{},
	}
	pods = slices.Clone(pods)
	slices.SortFunc(pods, func(a, b Java4ShellPod) int {
		return cmp.Or(cmp.Compare(a.Pod.Namespace, b.Pod.Namespace), cmp.Compare(a.Pod.Name, b.Pod.Name))
	})
	var items []*work
	byKey := map[string]int{}
	dockerHubLookups := 0
	var containers []java4ShellContainer
	for _, target := range pods {
		pod := target.Pod
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		platform, assumed, cpu := java4ShellPlacement(target.Node)
		var access Java4ShellRegistryAccess
		var secrets []credential
		var credentialScope string
		accessChecked := false
		statuses := map[string]corev1.ContainerStatus{}
		for _, source := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
			for _, status := range source {
				if status.State.Running != nil {
					statuses[status.Name] = status
				}
			}
		}
		for _, container := range runningContainers(pod) {
			status, running := statuses[container.Name]
			if !running {
				continue
			}
			out.Containers++
			c := java4ShellContainer{pod: pod, container: container.Name, image: status.Image}
			if strings.HasPrefix(c.image, "sha256:") {
				c.image = ""
			}
			ref, source, ok := imageRef(container.Image, status.ImageID)
			if !ok || source != "digest" {
				if c.image == "" {
					c.image = container.Image
				}
				c.unassessedReason = "image_identity_unknown"
				containers = append(containers, c)
				continue
			}
			if c.image == "" {
				c.image = ref.Context().Name()
			}
			c.reportedHash = ref.Identifier()
			if !accessChecked && accessFor != nil {
				access = accessFor(pod)
				var scope strings.Builder
				if access.RadarCredentials {
					scope.WriteString("|radar")
				}
				for _, secret := range access.PullSecrets {
					c := credential{namespace: secret.Namespace, secret: secret.Name, snapshot: secret,
						identity: string(secret.UID) + "/" + secret.ResourceVersion}
					secrets = append(secrets, c)
					scope.WriteByte('|')
					scope.WriteString(c.id())
				}
				credentialScope = scope.String()
				accessChecked = true
			}
			key := imageWorkKey(ref, platform) + "|advisory"
			lookupKey := key + credentialScope
			i, exists := byKey[lookupKey]
			if !exists {
				onDockerHub := ref.Context().RegistryStr() == name.DefaultRegistry
				if len(items) >= maxLookupsPerCall || (onDockerHub && dockerHubLookups >= java4ShellDockerHubLookups) {
					c.unassessedReason = StatusNotChecked
					containers = append(containers, c)
					continue
				}
				if onDockerHub {
					dockerHubLookups++
				}
				i = len(items)
				byKey[lookupKey] = i
				items = append(items, &work{key: key, ref: ref, platform: platform, advisory: true,
					secrets: secrets, radarCredentials: access.RadarCredentials})
			}
			c.lookup = i
			c.platformAssumed = assumed
			c.cpu = cpu
			c.mitigationDeclared = java4ShellMitigationDeclared(container)
			containers = append(containers, c)
		}
	}

	results := r.lookupConfigs(context.WithValue(ctx, strictRegistryLookupKey{}, true), items, false)
	groups := map[java4ShellGroup]*Java4ShellFinding{}
	workloads := map[*corev1.Pod]resourceid.Ref{}
	resolveWorkload := func(ctx context.Context, pod *corev1.Pod) resourceid.Ref {
		workload, resolved := workloads[pod]
		if !resolved {
			workload = resourceid.NewRef("", "Pod", pod.Namespace, pod.Name)
			if workloadFor != nil && ctx.Err() == nil {
				workload = workloadFor(ctx, pod)
			}
			workloads[pod] = workload
		}
		return workload
	}
	for i := range containers {
		c := &containers[i]
		if c.unassessedReason != "" {
			continue
		}
		res := results[c.lookup]
		if res.status != StatusOK {
			c.unassessedReason = res.status
			continue
		}
		// An image index can point at several platform manifests. Without
		// verified placement, only an exact manifest digest is evidence.
		if c.platformAssumed && c.reportedHash != res.digest {
			c.unassessedReason = "platform_unknown"
			continue
		}
		version := ""
		for _, declaration := range res.declared {
			if declaration.Runtime == "java" {
				version = declaration.Version
				break
			}
		}
		if version == "" {
			c.unassessedReason = "no_java_declaration"
			continue
		}
		affected, fixedIn, known := java4ShellVersion(version)
		if !known {
			c.unassessedReason = "version_unknown"
			continue
		}
		if !affected {
			continue
		}
		workload := resolveWorkload(ctx, c.pod)
		key := java4ShellGroup{workload: workload, container: c.container, digest: c.reportedHash, java: version}
		row := groups[key]
		if row == nil {
			row = &Java4ShellFinding{Workload: workload, Container: c.container, Image: c.image, Digest: c.reportedHash, Version: version, FixedIn: fixedIn}
			groups[key] = row
		}
		out.Matches++
		row.Pods++
		switch c.cpu {
		case "vbmi":
			row.VBMIPods++
		case "unknown":
			row.UnknownCPUPods++
		}
		if c.mitigationDeclared {
			row.MitigationDeclaredPods++
		}
	}
	for _, row := range groups {
		out.Findings = append(out.Findings, *row)
	}
	slices.SortFunc(out.Findings, func(a, b Java4ShellFinding) int {
		return cmp.Or(
			cmp.Compare(b.VBMIPods, a.VBMIPods),
			strings.Compare(a.Workload.Namespace, b.Workload.Namespace),
			strings.Compare(a.Workload.Name, b.Workload.Name),
			strings.Compare(a.Workload.Kind, b.Workload.Kind),
			strings.Compare(a.Workload.Group, b.Workload.Group),
			strings.Compare(a.Container, b.Container),
			strings.Compare(a.Image, b.Image),
			strings.Compare(a.Digest, b.Digest),
			strings.Compare(a.Version, b.Version),
		)
	})
	out.FindingGroups = len(out.Findings)
	out.Findings = out.Findings[:min(out.FindingGroups, java4ShellRowLimit)]

	// A cluster full of images without Java metadata can need many owner
	// permission checks. Fall back to the visible Pod once the budget expires.
	detailsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	unassessed := map[java4ShellUnassessedGroup]int{}
	for _, missingDeclaration := range []bool{false, true} {
		for _, c := range containers {
			if c.unassessedReason == "" || (c.unassessedReason == "no_java_declaration") != missingDeclaration {
				continue
			}
			out.Unassessed[c.unassessedReason]++
			key := java4ShellUnassessedGroup{
				workload: resolveWorkload(detailsCtx, c.pod), container: c.container, image: c.image,
				digest: c.reportedHash, reason: c.unassessedReason,
			}
			unassessed[key]++
		}
	}
	for key, count := range unassessed {
		out.UnassessedRows = append(out.UnassessedRows, Java4ShellUnassessed{
			Workload: key.workload, Container: key.container, Image: key.image, Digest: key.digest, Reason: key.reason, Pods: count,
		})
	}
	slices.SortFunc(out.UnassessedRows, func(a, b Java4ShellUnassessed) int {
		return cmp.Or(
			cmp.Compare(unassessedPriority(a.Reason), unassessedPriority(b.Reason)),
			cmp.Compare(b.Pods, a.Pods),
			strings.Compare(a.Workload.Namespace, b.Workload.Namespace),
			strings.Compare(a.Workload.Name, b.Workload.Name),
			strings.Compare(a.Workload.Kind, b.Workload.Kind),
			strings.Compare(a.Workload.Group, b.Workload.Group),
			strings.Compare(a.Container, b.Container),
			strings.Compare(a.Image, b.Image),
			strings.Compare(a.Digest, b.Digest),
			strings.Compare(a.Reason, b.Reason),
		)
	})
	out.UnassessedGroups = len(out.UnassessedRows)
	out.UnassessedRows = out.UnassessedRows[:min(out.UnassessedGroups, java4ShellRowLimit)]
	return out, nil
}

func unassessedPriority(reason string) int {
	if reason == "no_java_declaration" {
		return 1
	}
	return 0
}

func java4ShellPlacement(node *corev1.Node) (v1.Platform, bool, string) {
	platform := v1.Platform{OS: "linux", Architecture: "amd64"}
	if node == nil {
		return platform, true, "unknown"
	}
	info := node.Status.NodeInfo
	assumed := info.OperatingSystem == "" || info.Architecture == ""
	if !assumed {
		platform.OS, platform.Architecture = info.OperatingSystem, info.Architecture
	}
	switch {
	case info.Architecture != "" && info.Architecture != "amd64" && info.Architecture != "386":
		return platform, assumed, "non_x86"
	case info.Architecture == "amd64" && node.Labels[java4ShellVBMILabel] == "true":
		return platform, assumed, "vbmi"
	default:
		return platform, assumed, "unknown"
	}
}

var (
	java8Release      = regexp.MustCompile(`^(?:jdk-?)?8u([0-9]+)(?:-?b([0-9]+))?$`)
	javaReleaseSuffix = regexp.MustCompile(`^(?:\+[0-9]+(?:-lts)?)?$`)
)

// java4ShellVersion compares a declared upstream version string with the
// November 2026 advisory. A vendor package revision needs its own advisory.
func java4ShellVersion(raw string) (affected bool, fixedIn string, known bool) {
	v := strings.ToLower(raw)
	if strings.TrimPrefix(strings.TrimPrefix(v, "jdk"), "-") == "8u511b33-perf" {
		return true, "", true
	}
	if parts := java8Release.FindStringSubmatch(v); parts != nil {
		return false, "", parts[1] != "511" || (parts[2] != "" && parts[2] != "33")
	}
	version, suffix := parseVersionRest(v)
	if len(version) == 1 && strings.HasPrefix(suffix, "+") {
		version = append(version, 0, 0)
	}
	if len(version) < 3 || len(version) > 5 || !javaReleaseSuffix.MatchString(suffix) {
		return false, "", false
	}
	switch version[0] {
	case 17:
		if compareVersions(version[:3], []int{17, 0, 6}) < 0 {
			return false, "", true
		}
		fixedIn = "17.0.21.1"
	case 21:
		fixedIn = "21.0.13.1"
	case 25:
		fixedIn = "25.0.5.1"
	case 27:
		fixedIn = "27.0.1.1"
	case 18, 19, 20, 22, 23, 24, 26:
		return true, "", true
	case 9, 10, 11, 12, 13, 14, 15, 16:
		return false, "", true
	default:
		return false, "", false
	}
	fixed, _ := parseVersionRest(fixedIn)
	if len(version) > 3 && slices.Equal(version[:3], fixed[:3]) &&
		!slices.Equal(version, fixed) && !slices.Equal(version[3:], []int{0, 3}) {
		return false, "", false
	}
	return compareVersions(version, fixed) < 0, fixedIn, true
}

func java4ShellMitigationDeclared(container corev1.Container) bool {
	for _, arg := range slices.Concat(container.Command, container.Args) {
		if java4ShellMitigationOption(arg) {
			return true
		}
	}
	for _, env := range container.Env {
		switch env.Name {
		case "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS":
			if slices.ContainsFunc(strings.Fields(env.Value), java4ShellMitigationOption) {
				return true
			}
		}
	}
	return false
}

func java4ShellMitigationOption(option string) bool {
	switch option {
	case "-XX:-UseBASE64Intrinsics", "-XX:UseAVX=0", "-XX:UseAVX=1", "-XX:UseAVX=2":
		return true
	default:
		return false
	}
}
