package prometheus

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkloadBackendIdentity(t *testing.T) {
	for _, scope := range []prom.WorkloadMetricsScope{
		{SingleCluster: true},
		{ClusterLabels: map[string]string{"cluster": "west"}},
	} {
		c := &Client{workloadScope: &scope, workloadScopeEverSet: true}
		a := prom.Candidate{Namespace: "monitoring", Name: "prometheus", Port: 9090}
		if !c.markConnected("http://127.0.0.1:10001", "", a.Key(), 0) || c.workloadScope == nil || c.backendEpoch() != 0 {
			t.Fatal("initial binding discarded startup assertion")
		}
		memo := &workloadPartitionMemo{expires: time.Now().Add(time.Minute)}
		c.workloadPartition = memo
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entry := &workloadAttributionEntry{cancel: cancel}
		c.workloadAttributionEntries = map[string]*workloadAttributionEntry{"api": entry}
		c.mu.Lock()
		c.dropConnectionLocked()
		c.mu.Unlock()
		if !c.markConnected("http://127.0.0.1:10002", "", a.Key(), 0) || c.workloadScope == nil || c.workloadPartition != memo || ctx.Err() != nil || c.backendEpoch() != 0 {
			t.Fatal("same-service reconnect discarded valid trust")
		}
		b := a
		b.Port = 9091
		if !c.markConnected("http://127.0.0.1:10003", "", b.Key(), 0) {
			t.Fatal("backend replacement failed")
		}
		if c.workloadScope != nil || c.workloadPartition != nil || len(c.workloadAttributionEntries) != 0 || ctx.Err() == nil || c.backendEpoch() != 1 || c.DiscoveryGeneration() != 0 {
			t.Fatal("backend replacement retained trust or changed discovery generation")
		}
		if c.workloadScopeNotice() == "" {
			t.Fatal("discarded assertion was not explained")
		}
		c.markConnected("http://127.0.0.1:10001", "", a.Key(), 0)
		if c.backendEpoch() != 2 || c.workloadScope != nil {
			t.Fatal("returning to A resurrected old trust")
		}
		basePath := a
		basePath.BasePath = "/select/1/prometheus"
		c.markConnected("http://127.0.0.1:10001", basePath.BasePath, basePath.Key(), 0)
		if c.backendEpoch() != 3 {
			t.Fatal("backend tenant path was not part of identity")
		}
	}
}

func TestWorkloadBackendIdentitySurvivesReinitialize(t *testing.T) {
	for _, identity := range []string{"A", "B"} {
		t.Run(identity, func(t *testing.T) {
			Initialize(nil, nil, "original")
			if err := SetWorkloadMetricsScope(prom.WorkloadMetricsScope{SingleCluster: true}, ""); err != nil {
				t.Fatal(err)
			}
			GetClient().markConnected("http://first", "", "A", 0)
			Reinitialize(nil, nil, "original")
			c := GetClient()
			c.markConnected("http://second", "", identity, 0)
			if _, _, configured := c.workloadMetricsConfig(); configured != (identity == "A") {
				t.Fatalf("assertion preservation did not follow logical backend %s", identity)
			}
		})
	}
}

func TestWorkloadTrustDuringAutomaticFailover(t *testing.T) {
	var active atomic.Int32
	aHost := "prometheus-server.monitoring.svc.cluster.local:9090"
	bHost := "prometheus-server.observability.svc.cluster.local:9090"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := aHost
		if active.Load() == 1 {
			host = bHost
		}
		if r.Host != host {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
	}}
	defer transport.CloseIdleConnections()
	service := func(namespace string) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: namespace}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}}}
	}
	c := &Client{
		k8sClient: fake.NewSimpleClientset(service("monitoring"), service("observability")),
		inCluster: true, httpClient: &http.Client{Transport: transport, Timeout: time.Second},
		workloadScope: &prom.WorkloadMetricsScope{SingleCluster: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, host := range []string{aHost, bHost, aHost} {
		active.Store(int32(i % 2))
		addr, _, err := c.EnsureConnected(ctx)
		if err != nil || addr != "http://"+host {
			t.Fatalf("failover step %d: address=%q, err=%v", i, addr, err)
		}
		if c.backendEpoch() != uint64(i) || c.DiscoveryGeneration() != 0 {
			t.Fatalf("failover step %d: wrong identity epoch or discovery generation", i)
		}
		if i == 0 {
			if c.workloadScope == nil {
				t.Fatal("initial discovery lost startup scope")
			}
		} else if c.workloadScope != nil || c.workloadPartition != nil || len(c.workloadAttributionEntries) != 0 {
			t.Fatal("automatic replacement retained previous backend trust")
		}
		c.workloadPartition = &workloadPartitionMemo{expires: time.Now().Add(time.Minute), scope: prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": host}}}
		c.workloadAttributionEntries = map[string]*workloadAttributionEntry{"api": {}}
	}
}

func TestHistoricalProofCannotCrossBackendReplacement(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.Contains(r.Form.Get("query"), "kube_pod_info") {
			close(started)
			<-release
			_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID, "cluster": "west"}}}, ""))
			return
		}
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	defer server.Close()
	defer close(release)
	c := &Client{httpClient: server.Client()}
	c.markConnected(server.URL, "", "A", 0)
	done := make(chan error, 1)
	go func() {
		_, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("historical proof did not start")
	}
	c.markConnected("http://replacement", "", "B", 0)
	c.markConnected(server.URL, "", "A", 0)
	release <- struct{}{}
	select {
	case err := <-done:
		if err == nil || c.workloadPartition != nil {
			t.Fatal("in-flight proof from A survived A→B→A")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale historical proof did not finish")
	}
}
