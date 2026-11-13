package loki

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// fakeLoki answers /loki/api/v1/labels; status other than 200 answers body.
func fakeLoki(t *testing.T, status int, body string, seen *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Store(r.Header.Clone())
		}
		if r.URL.Path != "/loki/api/v1/labels" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const labelsOK = `{"status":"success","data":["namespace","pod","container"]}`

func reset(t *testing.T) {
	t.Helper()
	savedList, savedForward, savedContext := listServices, startForward, currentContext
	t.Cleanup(func() {
		listServices, startForward, currentContext = savedList, savedForward, savedContext
		Retire(nil)
	})
	currentContext = func() string { return "ctx" }
	listServices = func() []corev1.Service { return nil }
	startForward = func(context.Context, string, string, int, string) (string, error) {
		return "", errors.New("no forward in tests")
	}
}

func TestManualURLConnectsAndDetects(t *testing.T) {
	reset(t)
	var seen atomic.Value
	srv := fakeLoki(t, 200, labelsOK, &seen)
	Configure(srv.URL, map[string]string{"X-Scope-OrgID": "t"})
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load().(http.Header).Get("X-Scope-OrgID"); got != "t" {
		t.Errorf("header = %q", got)
	}
	st := CurrentStatus()
	if !st.Configured || !st.Connected || st.Source != "manual" || st.Labels == nil || st.Labels.Pod != "pod" || st.HistoryReason != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNeverCarriesSecrets(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	host := strings.TrimPrefix(srv.URL, "http://")
	Configure("http://user:pw@"+host+"/?token=x", map[string]string{"Authorization": "Bearer zzz"})
	_ = EnsureConnected(context.Background())
	data, _ := json.Marshal(CurrentStatus())
	for _, leak := range []string{"pw", "token=x", "zzz"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("status leaks %q: %s", leak, data)
		}
	}
}

func TestLokiErrorIsRedacted(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 401, "bad Bearer zzz", nil)
	Configure(srv.URL, map[string]string{"Authorization": "Bearer zzz"})
	err := EnsureConnected(context.Background())
	if err == nil || strings.Contains(err.Error(), "zzz") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(CurrentStatus().Error, "zzz") || CurrentStatus().Error == "" {
		t.Fatalf("status error = %q", CurrentStatus().Error)
	}
}

func TestRetireDisables(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	Configure(srv.URL, nil)
	_ = EnsureConnected(context.Background())
	Retire(errors.New("x"))
	st := CurrentStatus()
	if st.Connected || st.Configured || st.Error != "x" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := Get(context.Background(), "/loki/api/v1/labels", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Get after Retire = %v", err)
	}
}

func TestForwardLostDropsAddress(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	startForward = func(context.Context, string, string, int, string) (string, error) { return srv.URL, nil }
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	forwardLost("loki", srv.URL)
	if CurrentStatus().Connected {
		t.Fatal("a lost forward still reads as connected")
	}
	forwardLost("prometheus", "http://localhost:1")
	if !CurrentStatus().Configured {
		t.Fatal("an unrelated loss retired Loki")
	}
}

func TestDiscoveryPrefersDirectThenForward(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	var forwarded atomic.Bool
	startForward = func(_ context.Context, ns, name string, p int, ctxName string) (string, error) {
		if ns != "logging" || name != "loki" || p != 3100 || ctxName != "ctx" {
			t.Errorf("forward to %s/%s:%d on %s", ns, name, p, ctxName)
		}
		forwarded.Store(true)
		return srv.URL, nil
	}
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := CurrentStatus()
	if !forwarded.Load() || st.Source != "discovered" || st.Address != srv.URL || !st.Connected {
		t.Fatalf("status = %+v (the direct Service DNS name can't resolve in tests, so the forward must answer)", st)
	}
}

func TestContextSwitchRediscovers(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	Configure(srv.URL, nil)
	_ = EnsureConnected(context.Background())
	currentContext = func() string { return "other" }
	if CurrentStatus().Connected {
		t.Fatal("a connection found on another context reads as connected")
	}
}

func TestNoLokiFound(t *testing.T) {
	reset(t)
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err == nil || !strings.Contains(err.Error(), "no Loki Service") {
		t.Fatalf("error = %v", err)
	}
}
