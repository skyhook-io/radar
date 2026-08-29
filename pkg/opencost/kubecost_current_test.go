package opencost

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type kubecostRequest struct {
	path   string
	params url.Values
}

type fakeKubecostTransport struct {
	responses []string
	errors    []error
	requests  []kubecostRequest
}

func (f *fakeKubecostTransport) Do(_ context.Context, _ string, path string, params url.Values) ([]byte, error) {
	f.requests = append(f.requests, kubecostRequest{path: path, params: params})
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return []byte(response), nil
}

func (f *fakeKubecostTransport) Address() string { return "http://kubecost.test" }

func floatPointer(value float64) *float64 { return &value }

func TestComputeKubecostSummaryFallsBackFromNullHourAndNormalizesActualDuration(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[null]}`,
		`{"code":200,"data":[{"__idle__/__idle__":{"properties":{"cluster":"__idle__","namespace":"__idle__"},"start":"2026-08-25T00:00:00Z","end":"2026-08-26T00:00:00Z","cpuCost":12,"ramCost":12,"totalCost":24},"radar-kubecost-e2e/__unallocated__":{"properties":{"cluster":"radar-kubecost-e2e","namespace":"__unallocated__"},"totalCost":100},"radar-kubecost-e2e/demo":{"properties":{"cluster":"radar-kubecost-e2e","namespace":"demo"},"start":"2026-08-25T00:00:00Z","end":"2026-08-26T00:00:00Z","cpuCoreRequestAverage":2,"cpuCoreUsageAverage":1,"cpuCost":24,"ramByteRequestAverage":4,"ramByteUsageAverage":1,"ramCost":24,"pvCost":12,"networkCost":6,"totalCost":66}}]}`,
	}}
	resp, err := ComputeKubecostSummary(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{
		Currency:  "EUR",
		ClusterID: "radar-kubecost-e2e",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || resp.Window != "1d" || resp.Source != "kubecost" || resp.Currency != "EUR" {
		t.Fatalf("unexpected response metadata: %#v", resp)
	}
	if len(resp.Namespaces) != 1 {
		t.Fatalf("namespaces = %d, want 1", len(resp.Namespaces))
	}
	row := resp.Namespaces[0]
	if row.Name != "demo" || row.HourlyCost != 2.75 || row.CPUCost != 1 || row.MemoryCost != 1 || row.StorageCost != 0.5 || row.NetworkCost != 0.25 {
		t.Fatalf("unexpected normalized row: %#v", row)
	}
	if row.CPUUsageCost != 0.5 || row.MemoryUsageCost != 0.25 || row.IdleCost != 1.25 || row.Efficiency != 37.5 {
		t.Fatalf("unexpected usage normalization: %#v", row)
	}
	if resp.TotalIdleCost != 2.25 {
		t.Fatalf("totalIdleCost = %v, want 2.25", resp.TotalIdleCost)
	}
	if len(transport.requests) != 2 || transport.requests[0].params.Get("window") != "1h" || transport.requests[1].params.Get("window") != "1d" {
		t.Fatalf("requests = %#v, want 1h then 1d", transport.requests)
	}
	if got := transport.requests[1].params.Get("filter"); got != `cluster:"radar-kubecost-e2e"` {
		t.Fatalf("filter = %q", got)
	}
}

func TestComputeKubecostWorkloadsRequiresExactIdentity(t *testing.T) {
	tests := []struct {
		name       string
		properties string
		wantError  string
	}{
		{name: "missing namespace", properties: `"cluster":"cluster-a","controllerKind":"deployment","controller":"web"`, wantError: "namespace"},
		{name: "other cluster", properties: `"cluster":"cluster-b","namespace":"demo","controllerKind":"deployment","controller":"web"`, wantError: "cluster-b"},
		{name: "missing controller", properties: `"cluster":"cluster-a","namespace":"demo","controllerKind":"deployment"`, wantError: "controller identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"code":200,"data":[{"row":{"properties":{` + tt.properties + `},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":1,"ramCost":1}}]}`
			transport := &fakeKubecostTransport{responses: []string{body}}
			_, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a"})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestComputeKubecostWorkloadsReturnsUsageAvailability(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"cluster-a/demo/deployment/web":{"properties":{"cluster":"cluster-a","namespace":"demo","controllerKind":"deployment","controller":"web"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T02:00:00Z","cpuCoreRequestAverage":2,"cpuCoreUsageAverage":1,"cpuCost":2,"ramByteRequestAverage":4,"ramByteUsageAverage":2,"ramCost":4}}]}`,
	}}
	resp, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || len(resp.Workloads) != 1 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	row := resp.Workloads[0]
	if row.Kind != "Deployment" || row.Name != "web" || row.HourlyCost != 3 || row.CPUUsageCost != 0.5 || row.MemoryUsageCost != 1 || !row.CPUUsageAvailable || !row.MemoryUsageAvailable {
		t.Fatalf("unexpected workload: %#v", row)
	}
	if got := transport.requests[0].params.Get("filter"); got != `cluster:"cluster-a"+namespace:"demo"` {
		t.Fatalf("filter = %q", got)
	}
	if got := transport.requests[0].params.Get("aggregate"); got != "cluster,namespace,pod,controllerKind,controller" {
		t.Fatalf("aggregate = %q", got)
	}
}

func TestComputeKubecostWorkloadsForNamespacesBoundsQueriesByNamespace(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"a":{"properties":{"cluster":"cluster-a","namespace":"team-a","pod":"api-a","controllerKind":"deployment","controller":"api"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":1,"ramCost":1}}]}`,
		`{"code":200,"data":[{"b":{"properties":{"cluster":"cluster-a","namespace":"team-b","pod":"worker-b","controllerKind":"statefulset","controller":"worker"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":2,"ramCost":2}}]}`,
	}}
	responses, err := ComputeKubecostWorkloadsForNamespaces(context.Background(), NewKubecostClient(transport), map[string]PodOwnerLookup{
		"team-a": nil,
		"team-b": nil,
	}, KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 2 ||
		transport.requests[0].params.Get("filter") != `cluster:"cluster-a"+namespace:"team-a"` ||
		transport.requests[1].params.Get("filter") != `cluster:"cluster-a"+namespace:"team-b"` {
		t.Fatalf("requests = %#v, want one bounded allocation query per namespace", transport.requests)
	}
	if got := responses["team-a"]; !got.Available || len(got.Workloads) != 1 || got.Workloads[0].Name != "api" {
		t.Fatalf("team-a response = %#v", got)
	}
	if got := responses["team-b"]; !got.Available || len(got.Workloads) != 1 || got.Workloads[0].Name != "worker" {
		t.Fatalf("team-b response = %#v", got)
	}
}

func TestComputeKubecostWorkloadsForNamespacesRejectsRowsOutsideRequestedNamespace(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"private":{"properties":{"cluster":"cluster-a","namespace":"private","pod":"hidden","controllerKind":"deployment","controller":"hidden"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":10,"ramCost":10}}]}`,
	}}
	_, err := ComputeKubecostWorkloadsForNamespaces(context.Background(), NewKubecostClient(transport), map[string]PodOwnerLookup{
		"team-a": nil,
	}, KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err == nil || !strings.Contains(err.Error(), `has namespace "private", expected "team-a"`) {
		t.Fatalf("error = %v, want mismatched namespace rejection", err)
	}
}

func TestComputeKubecostWorkloadsIncludesBatchControllers(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"cluster-a/demo/job/migrate":{"properties":{"cluster":"cluster-a","namespace":"demo","controllerKind":"job","controller":"migrate"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":5,"ramCost":5},"cluster-a/demo/deployment/web":{"properties":{"cluster":"cluster-a","namespace":"demo","controllerKind":"deployment","controller":"web"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":1,"ramCost":1}}]}`,
	}}
	resp, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || len(resp.Workloads) != 2 || resp.Workloads[0].Kind != "Job" || resp.Workloads[0].Name != "migrate" || resp.Workloads[1].Kind != "Deployment" || resp.Workloads[1].Name != "web" {
		t.Fatalf("unexpected workload response: %#v", resp)
	}
}

func TestComputeKubecostWorkloadsNormalizesAfterPodChurnAggregation(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"old":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"web-old","controllerKind":"deployment","controller":"web"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T00:40:00Z","cpuCost":0.6666666667,"ramCost":0},"new":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"web-new","controllerKind":"deployment","controller":"web"},"start":"2026-08-26T00:40:00Z","end":"2026-08-26T01:00:00Z","cpuCost":0.3333333333,"ramCost":0}}]}`,
	}}
	resp, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Workloads) != 1 || resp.Workloads[0].HourlyCost != 1 || resp.Workloads[0].Replicas != 2 {
		t.Fatalf("unexpected churn-normalized workload: %#v", resp.Workloads)
	}
}

func TestComputeKubecostWorkloadsIncludesStandaloneAndStaticPods(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"bare-a":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"api-abc-123"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":1},"bare-b":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"api-def-456","controllerKind":"replicaset","controller":"orphan-rs"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":1},"static":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"static-pod"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCost":2}}]}`,
	}}
	owners := func(pod string) (WorkloadOwner, bool) {
		if pod == "static-pod" {
			return WorkloadOwner{Kind: "Node", Name: "worker"}, true
		}
		if pod == "api-abc-123" {
			return WorkloadOwner{Kind: "standalone"}, true
		}
		return WorkloadOwner{}, false
	}
	resp, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a", Owners: owners})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Workloads) != 2 {
		t.Fatalf("unexpected pod workloads: %#v", resp.Workloads)
	}
	byKind := map[string]WorkloadCost{}
	for _, workload := range resp.Workloads {
		byKind[workload.Kind] = workload
	}
	if standalone := byKind["standalone"]; standalone.Name != "api" || standalone.Replicas != 2 {
		t.Fatalf("unexpected standalone workload: %#v", standalone)
	}
	if staticPod := byKind["staticpod"]; staticPod.Name != "static-pod" {
		t.Fatalf("unexpected static pod workload: %#v", staticPod)
	}
}

func TestComputeKubecostWorkloadsResolvesAndCoalescesPodOwners(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"cluster-a/demo/queue-0/__unallocated__/__unallocated__":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"queue-0"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCoreRequestAverage":1,"cpuCoreUsageAverage":0.5,"cpuCost":1,"ramByteRequestAverage":1,"ramByteUsageAverage":0.5,"ramCost":1},"cluster-a/demo/queue-1/__unallocated__/__unallocated__":{"properties":{"cluster":"cluster-a","namespace":"demo","pod":"queue-1"},"start":"2026-08-26T00:00:00Z","end":"2026-08-26T01:00:00Z","cpuCoreRequestAverage":1,"cpuCoreUsageAverage":0.5,"cpuCost":1,"ramByteRequestAverage":1,"ramByteUsageAverage":0.5,"ramCost":1}}]}`,
	}}
	owners := func(pod string) (WorkloadOwner, bool) {
		if pod == "queue-0" || pod == "queue-1" {
			return WorkloadOwner{Kind: "StatefulSet", Name: "queue"}, true
		}
		return WorkloadOwner{}, false
	}
	resp, err := ComputeKubecostWorkloads(context.Background(), NewKubecostClient(transport), "demo", KubecostCurrentOptions{ClusterID: "cluster-a", Owners: owners})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Workloads) != 1 {
		t.Fatalf("workloads = %#v, want one coalesced StatefulSet", resp.Workloads)
	}
	row := resp.Workloads[0]
	if row.Kind != "StatefulSet" || row.Name != "queue" || row.HourlyCost != 4 || row.CPUCost != 2 || row.MemoryCost != 2 {
		t.Fatalf("unexpected coalesced workload: %#v", row)
	}
}

func TestComputeKubecostNodesDoesNotSendUnsupportedAggregate(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"node":{"type":"Node","properties":{"cluster":"cluster-a","name":"worker","providerID":"kind://worker","region":"local"},"labels":{"node_kubernetes_io_instance_type":"kind"},"start":"2026-08-25T00:00:00Z","end":"2026-08-26T00:00:00Z","cpuCost":24,"ramCost":12,"totalCost":36}}]}`,
	}}
	resp, err := ComputeKubecostNodes(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || len(resp.Nodes) != 1 || resp.Nodes[0].HourlyCost != 1.5 || resp.Nodes[0].InstanceType != "kind" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if transport.requests[0].params.Has("aggregate") {
		t.Fatalf("assets request must not send aggregate: %v", transport.requests[0].params)
	}
	if got := transport.requests[0].params.Get("window"); got != "1h" {
		t.Fatalf("assets window = %q, want 1h", got)
	}
	if got := transport.requests[0].params.Get("filter"); got != `cluster:"cluster-a"+assetType:"node"` {
		t.Fatalf("assets filter = %q", got)
	}
}

func TestComputeKubecostNodesFallsBackToDailyAssets(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[null]}`,
		`{"code":200,"data":[{"node":{"type":"Node","properties":{"cluster":"cluster-a","name":"worker"},"start":"2026-08-25T00:00:00Z","end":"2026-08-26T00:00:00Z","totalCost":24}}]}`,
	}}
	resp, err := ComputeKubecostNodes(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || len(resp.Nodes) != 1 || resp.Nodes[0].HourlyCost != 1 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(transport.requests) != 2 || transport.requests[0].params.Get("window") != "1h" || transport.requests[1].params.Get("window") != "1d" {
		t.Fatalf("requests = %#v, want 1h then 1d", transport.requests)
	}
}

func TestKubecostAllocationFallbackRetriesAfterHourlyError(t *testing.T) {
	transport := &fakeKubecostTransport{
		errors: []error{errors.New("hourly ETL unavailable"), nil},
		responses: []string{
			`{"code":200,"data":[{"cluster-a/demo":{"properties":{"cluster":"cluster-a","namespace":"demo"},"start":"2026-08-25T00:00:00Z","end":"2026-08-26T00:00:00Z","cpuCost":24,"totalCost":24}}]}`,
		},
	}
	resp, err := ComputeKubecostSummary(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Available || resp.Window != "1d" || len(transport.requests) != 2 {
		t.Fatalf("unexpected fallback response=%#v requests=%#v", resp, transport.requests)
	}
}

func TestKubecostAllocationFallbackPrefersSuccessfulEmptyResponse(t *testing.T) {
	transport := &fakeKubecostTransport{
		errors:    []error{errors.New("hourly ETL unavailable"), nil},
		responses: []string{`{"code":200,"data":[null]}`},
	}
	resp, err := ComputeKubecostSummary(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Available || resp.Reason != ReasonNoMetrics || len(transport.requests) != 2 {
		t.Fatalf("unexpected fallback response=%#v requests=%#v", resp, transport.requests)
	}
}

func TestKubecostAssetsFallbackPrefersSuccessfulEmptyResponse(t *testing.T) {
	transport := &fakeKubecostTransport{
		errors:    []error{errors.New("hourly ETL unavailable"), nil},
		responses: []string{`{"code":200,"data":[null]}`},
	}
	resp, err := ComputeKubecostNodes(context.Background(), NewKubecostClient(transport), KubecostCurrentOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Available || resp.Reason != ReasonNoMetrics || len(transport.requests) != 2 {
		t.Fatalf("unexpected fallback response=%#v requests=%#v", resp, transport.requests)
	}
}

func TestLatestKubecostTimestampComparesInstants(t *testing.T) {
	current := "2026-08-26T10:00:00+0300"
	candidate := "2026-08-26T08:30:00Z"
	if got := LatestKubecostTimestamp(current, candidate); got != candidate {
		t.Fatalf("latest timestamp = %q, want %q", got, candidate)
	}
}

func TestKubecostUsageCostDistinguishesUnavailableFromZero(t *testing.T) {
	if _, available := kubecostUsageCost(1, nil, floatPointer(0)); available {
		t.Fatal("missing request must be unavailable")
	}
	if cost, available := kubecostUsageCost(1, floatPointer(2), floatPointer(0)); !available || cost != 0 {
		t.Fatalf("zero usage = (%v, %v), want (0, true)", cost, available)
	}
}
