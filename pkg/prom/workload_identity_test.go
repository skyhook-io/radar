package prom

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCompactWorkloadQueryGrowth(t *testing.T) {
	var pods []WorkloadPodIdentity
	var names []string
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("api-%d", i)
		pods = append(pods, WorkloadPodIdentity{Name: name, UID: fmt.Sprintf("%08x-c1fc-48b0-9bb4-683489285358", i)})
		names = append(names, name)
	}
	q, err := BuildIdentityRequestQueries(time.Minute, SelectPods("shop", names), pods, RequestSourceBeyla, `job="beyla"`)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{q.Rate, q.Errors, q.P95, q.HistogramCoverage, q.HistogramUniformity, q.StatusCoverage, q.Population} {
		if len(query) > 16000 {
			t.Fatalf("100 short-name Pods exceeded query budget: %d", len(query))
		}
	}
	for i := range pods {
		pods[i].Name = strings.Repeat("long-name-", 5) + names[i]
		names[i] = pods[i].Name
	}
	q, err = BuildIdentityRequestQueries(time.Minute, SelectPods("shop", names), pods, RequestSourceBeyla, `job="beyla"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Rate) > 16000 || len(q.Population) <= 16000 {
		t.Fatalf("expected coverage query to hit the bound first: rate=%d population=%d", len(q.Rate), len(q.Population))
	}
}

func TestPodCgroupPattern(t *testing.T) {
	const uid = "030a7597-c1fc-48b0-9bb4-683489285358"
	pattern, err := PodCgroupPattern(uid)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("^(?:" + pattern + ")$")
	for _, test := range []struct {
		path string
		want bool
	}{
		{"/kubepods/pod" + uid + "/container", true},
		{"/kubepods/burstable/pod" + uid + "/container", true},
		{"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/cri-containerd-container.scope", true},
		{"/kubepods.slice/kubepods-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/container.scope", true},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-burstable.slice/kubelet-kubepods-burstable-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/cri-containerd-container.scope", true},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-besteffort.slice/kubelet-kubepods-besteffort-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/container.scope", true},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/container.scope", true},
		{"/other.slice/kubelet-kubepods.slice/kubelet-kubepods-pod030a7597_c1fc_48b0_9bb4_683489285358.slice/container.scope", false},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-burstable-pod030a7597_c1fc_48b0_9bb4_683489285358extra.slice/container.scope", false},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod030a7597_c1fc_48b0_9bb4_683489285358.slice", false},
		{"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-pod00000000_0000_0000_0000_000000000000.slice/" + uid, false},
		{"/kubepods/pod" + uid + "extra/container", false},
		{"/other/pod" + uid + "/container", false},
		{"/kubepods/pod00000000-0000-0000-0000-000000000000/" + uid, false},
		{"/kubepods/pod" + uid, false},
	} {
		if got := re.MatchString(test.path); got != test.want {
			t.Errorf("%q matched=%v, want %v", test.path, got, test.want)
		}
		if got := PodUIDFromCgroupID(test.path); (got == uid) != test.want {
			t.Errorf("%q extracted UID=%q, want match=%v", test.path, got, test.want)
		}
	}
	if _, err := PodCgroupPattern(".*"); err == nil {
		t.Fatal("accepted regex as UID")
	}
}

func TestWorkloadIdentityMatchers(t *testing.T) {
	pods := []WorkloadPodIdentity{{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}, {Name: "api-1", UID: "0d709b51-f624-4d0a-b7dd-79e17402acb9"}}
	matchers, err := buildWorkloadIdentityFilter(pods, RequestSourceBeyla)
	if err != nil || matchers == nil {
		t.Fatalf("matchers=%v, err=%v", matchers, err)
	}
	if !strings.Contains(matchers.pairs, `api-0/`+pods[0].UID) {
		t.Fatalf("lost name/UID association: %v", matchers)
	}
	if _, err := buildWorkloadIdentityFilter(pods, RequestSourceIstio); err == nil {
		t.Fatal("invented Istio UID support")
	}
	pods[1].Name = pods[0].Name
	if _, err := buildWorkloadIdentityFilter(pods, RequestSourceBeyla); err == nil {
		t.Fatal("accepted conflicting name/UID tuple")
	}
}
