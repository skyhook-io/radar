package images

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
)

const (
	java4ShellVBMILabel        = "feature.node.kubernetes.io/cpu-cpuid.AVX512VBMI"
	java4ShellRowLimit         = 100
	java4ShellDockerHubLookups = 24
)

var ErrJava4ShellBusy = errors.New("a Java4Shell check is already running")

type Java4ShellPod struct {
	Pod  *corev1.Pod
	Node *corev1.Node
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
	Findings      []Java4ShellFinding `json:"findings"`
	Containers    int                 `json:"containers"`
	Matches       int                 `json:"matches"`
	FindingGroups int                 `json:"findingGroups"`
	Unassessed    map[string]int      `json:"unassessed"`
}

type java4ShellContainer struct {
	pod                *corev1.Pod
	container          string
	image              string
	lookup             int
	reportedHash       string
	platformAssumed    bool
	cpu                string
	mitigationDeclared bool
}

type java4ShellGroup struct {
	workload                resourceid.Ref
	container, digest, java string
}

// CheckJava4Shell uses anonymous registry reads only. Work keys keep different
// platform configs separate, while CPU evidence is evaluated for each Pod.
func (r *RuntimeInventory) CheckJava4Shell(ctx context.Context, pods []Java4ShellPod, workloadFor func(*corev1.Pod) resourceid.Ref) (Java4ShellResult, error) {
	if !r.java4ShellActive.CompareAndSwap(false, true) {
		return Java4ShellResult{}, ErrJava4ShellBusy
	}
	defer r.java4ShellActive.Store(false)

	out := Java4ShellResult{Findings: []Java4ShellFinding{}, Unassessed: map[string]int{}}
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
			ref, source, ok := imageRef(container.Image, status.ImageID)
			if !ok || source != "digest" {
				out.Unassessed["image_identity_unknown"]++
				continue
			}
			key := imageWorkKey(ref, platform) + "|advisory"
			i, exists := byKey[key]
			if !exists {
				onDockerHub := ref.Context().RegistryStr() == name.DefaultRegistry
				if len(items) >= maxLookupsPerCall || (onDockerHub && dockerHubLookups >= java4ShellDockerHubLookups) {
					out.Unassessed[StatusNotChecked]++
					continue
				}
				if onDockerHub {
					dockerHubLookups++
				}
				i = len(items)
				byKey[key] = i
				items = append(items, &work{key: key, ref: ref, platform: platform})
			}
			image := status.Image
			if image == "" {
				image = ref.Context().Name()
			}
			containers = append(containers, java4ShellContainer{
				pod: pod, container: container.Name, image: image, lookup: i, reportedHash: ref.Identifier(),
				platformAssumed: assumed, cpu: cpu, mitigationDeclared: java4ShellMitigationDeclared(container),
			})
		}
	}

	results := r.lookupConfigs(context.WithValue(ctx, strictRegistryLookupKey{}, true), items, false)
	groups := map[java4ShellGroup]*Java4ShellFinding{}
	workloads := map[*corev1.Pod]resourceid.Ref{}
	for _, c := range containers {
		res := results[c.lookup]
		if res.status != StatusOK {
			out.Unassessed[res.status]++
			continue
		}
		// An image index can point at several platform manifests. Without
		// verified placement, only an exact manifest digest is evidence.
		if c.platformAssumed && c.reportedHash != res.digest {
			out.Unassessed["platform_unknown"]++
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
			out.Unassessed["no_java_declaration"]++
			continue
		}
		affected, fixedIn, known := java4ShellVersion(version)
		if !known {
			out.Unassessed["version_unknown"]++
			continue
		}
		if !affected {
			continue
		}
		workload, resolved := workloads[c.pod]
		if !resolved {
			workload = workloadFor(c.pod)
			workloads[c.pod] = workload
		}
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
	return out, nil
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
