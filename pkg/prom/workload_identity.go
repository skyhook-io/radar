package prom

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type WorkloadPodIdentity struct {
	Name string
	UID  string
}

var podIdentityUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var cgroupPodUID = regexp.MustCompile(`pod([a-f0-9]{8}[-_][a-f0-9]{4}[-_][a-f0-9]{4}[-_][a-f0-9]{4}[-_][a-f0-9]{12})`)

var podCgroupFormats = []struct{ prefix, separator, suffix string }{
	{`/kubepods(/(burstable|besteffort))?/pod`, "-", `/.*`},
	{`/kubepods\.slice/(kubepods-(burstable|besteffort)\.slice/)?kubepods(-(burstable|besteffort))?-pod`, "_", `\.slice/.*`},
	{`/kubelet\.slice/kubelet-kubepods\.slice/(kubelet-kubepods-(burstable|besteffort)\.slice/)?kubelet-kubepods(-(burstable|besteffort))?-pod`, "_", `\.slice/.*`},
}

func PodUIDFromCgroupID(id string) string {
	match := cgroupPodUID.FindStringSubmatch(id)
	if len(match) == 0 {
		return ""
	}
	uid := strings.ReplaceAll(match[1], "_", "-")
	pattern, err := PodCgroupPattern(uid)
	if err != nil || !regexp.MustCompile("^(?:"+pattern+")$").MatchString(id) {
		return ""
	}
	return uid
}

// PodCgroupPattern matches the Pod segment, never a UID appearing in a container name.
func PodCgroupPattern(uid string) (string, error) {
	if !podIdentityUID.MatchString(uid) {
		return "", fmt.Errorf("invalid Kubernetes Pod UID")
	}
	patterns := make([]string, 0, len(podCgroupFormats))
	for _, format := range podCgroupFormats {
		patterns = append(patterns, format.prefix+strings.ReplaceAll(uid, "-", format.separator)+format.suffix)
	}
	return "(" + strings.Join(patterns, "|") + ")", nil
}

type workloadIdentityFilter struct {
	pairs string
	beyla bool
}

func workloadMetricExpression(metric, scope, target string, identity *workloadIdentityFilter, window string) string {
	expression := workloadMetricSelector(metric, scope, target)
	if window != "" {
		expression = "rate(" + expression + "[" + window + "])"
	}
	if identity == nil {
		return expression
	}
	podLabel, uidLabel := "k8s_pod_name", "k8s_pod_uid"
	if !identity.beyla {
		podLabel, uidLabel = "pod", "__radar_uid"
		expression = `label_replace(` + expression + `,"__radar_uid","","",".*")`
		for _, driver := range podCgroupFormats {
			prefix := strings.ReplaceAll(driver.prefix, "(", "(?:")
			uid := `([a-f0-9]{8})` + driver.separator + `([a-f0-9]{4})` + driver.separator + `([a-f0-9]{4})` + driver.separator + `([a-f0-9]{4})` + driver.separator + `([a-f0-9]{12})`
			expression = `label_replace(` + expression + `,"__radar_uid","$1-$2-$3-$4-$5","id",` + strconv.Quote(prefix+uid+driver.suffix) + `)`
		}
	}
	expression = `label_join(` + expression + `,"__radar_pair","/","` + podLabel + `","` + uidLabel + `")`
	// Clear a preexisting source label before using it as the match sentinel.
	expression = `label_replace(` + expression + `,"__radar_keep","","",".*")`
	return `(label_replace(` + expression + `,"__radar_keep","1","__radar_pair",` + strconv.Quote(identity.pairs) + `) and on(__radar_keep) label_replace(vector(1),"__radar_keep","1","",".*"))`
}

func buildWorkloadIdentityFilter(pods []WorkloadPodIdentity, source RequestSource) (*workloadIdentityFilter, error) {
	if len(pods) == 0 || len(pods) > 100 {
		return nil, fmt.Errorf("workload identity requires between 1 and 100 Pods")
	}
	if source != RequestSourceBeyla && source != "" {
		return nil, fmt.Errorf("source does not expose a supported Pod UID")
	}
	seen := map[string]bool{}
	pairs := make([]string, 0, len(pods))
	for _, pod := range pods {
		if pod.Name == "" || !podIdentityUID.MatchString(pod.UID) || seen[pod.Name] {
			return nil, fmt.Errorf("invalid or duplicate workload Pod identity")
		}
		seen[pod.Name] = true
		pairs = append(pairs, regexp.QuoteMeta(pod.Name+"/"+pod.UID))
	}
	sort.Strings(pairs)
	return &workloadIdentityFilter{pairs: "^(" + strings.Join(pairs, "|") + ")$", beyla: source == RequestSourceBeyla}, nil
}
