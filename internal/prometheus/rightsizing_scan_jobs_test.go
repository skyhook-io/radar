package prometheus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
)

func scanTestManager(t *testing.T, run scanRunner) *RightsizingScanManager {
	t.Helper()
	m := newRightsizingScanManager()
	m.prepare = func() (scanRunner, func() bool) { return run, func() bool { return true } }
	t.Cleanup(m.Invalidate)
	return m
}

func scanTestRequest(m *RightsizingScanManager) RightsizingScanRequest {
	return RightsizingScanRequest{Generation: m.Generation(), Namespaces: []string{"app"}, Scope: RightsizingScanScope{NamespacesByKind: map[string][]string{"Deployment": {"app"}}}, Start: true}
}

func awaitScan(t *testing.T, m *RightsizingScanManager, request RightsizingScanRequest) *RightsizingScanResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request.Wait = time.Second
	for ctx.Err() == nil {
		result, err := m.Resolve(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if result.ScanStatus != "running" {
			return result
		}
	}
	t.Fatal("scan did not finish")
	return nil
}

func TestScanSurvivesDisconnectAndReusesPublishedBatches(t *testing.T) {
	finish := make(chan struct{})
	published := make(chan struct{})
	var calls atomic.Int32
	m := scanTestManager(t, func(ctx context.Context, scope RightsizingScanScope, publish func(RightsizingScanResponse)) RightsizingScanResponse {
		calls.Add(1)
		result := newRightsizingScanResponse(time.Now(), scope)
		result.Coverage.WorkloadsEvaluated = 50
		result.Warnings = make([]RightsizingScanWarning, 1, 4)
		result.Warnings[0] = RightsizingScanWarning{Code: "z"}
		publish(result)
		close(published)
		select {
		case <-finish:
		case <-ctx.Done():
		}
		appendScanWarning(&result, "a", "supporting evidence unavailable")
		result.Coverage.WorkloadsEvaluated = 100
		result.State = RightsizingScanPartial
		return result
	})
	request := scanTestRequest(m)
	ctx, cancel := context.WithCancel(context.Background())
	first, err := m.Resolve(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-published
	request.ID = first.ScanID
	progress, err := m.Resolve(context.Background(), request)
	if err != nil || progress.Coverage.WorkloadsEvaluated != 50 || progress.ScanStatus != "running" || progress.State != RightsizingScanPartial {
		t.Fatalf("progress: %+v, %v", progress, err)
	}
	request.ID = ""
	joined, err := m.Resolve(context.Background(), request)
	if err != nil || joined.ScanID != first.ScanID || calls.Load() != 1 {
		t.Fatalf("duplicate scan: %+v, %v", joined, err)
	}
	close(finish)
	request.ID = first.ScanID
	final := awaitScan(t, m, request)
	if final.ScanStatus != "finished" || final.Coverage.WorkloadsEvaluated != 100 {
		t.Fatalf("final: %+v", final)
	}
	if progress.Warnings[0].Code != "z" {
		t.Fatal("a later publish mutated an earlier snapshot")
	}
	request.ID = ""
	cached, err := m.Resolve(context.Background(), request)
	if err != nil || cached.ScanID != first.ScanID || calls.Load() != 1 {
		t.Fatal("terminal lookup reran queries")
	}
}

func TestScanIdentityScopeAndGenerationIsolation(t *testing.T) {
	m := scanTestManager(t, func(ctx context.Context, scope RightsizingScanScope, _ func(RightsizingScanResponse)) RightsizingScanResponse {
		<-ctx.Done()
		return newRightsizingScanResponse(time.Now(), scope)
	})
	request := scanTestRequest(m)
	alice := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{"operators"}})
	first, err := m.Resolve(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = first.ScanID
	for _, user := range []*auth.User{{Username: "bob", Groups: []string{"operators"}}, {Username: "alice", Groups: []string{"viewers"}}} {
		ctx := auth.ContextWithUser(context.Background(), user)
		if _, err := m.Resolve(ctx, request); !errors.Is(err, ErrRightsizingScanNotFound) {
			t.Fatalf("other identity: %v", err)
		}
		if _, err := m.Namespaces(ctx, first.ScanID, request.Generation); !errors.Is(err, ErrRightsizingScanNotFound) {
			t.Fatalf("scope leaked: %v", err)
		}
	}
	namespaces, err := m.Namespaces(alice, first.ScanID, request.Generation)
	if err != nil || len(namespaces) != 1 || namespaces[0] != "app" {
		t.Fatalf("original scope: %v %v", namespaces, err)
	}
	request.Scope.NamespacesByKind = map[string][]string{}
	if _, err := m.Resolve(alice, request); !errors.Is(err, ErrRightsizingScanScopeChanged) {
		t.Fatalf("revoked permission: %v", err)
	}
	m.Invalidate()
	if _, err := m.Resolve(alice, request); !errors.Is(err, ErrRightsizingScanScopeChanged) {
		t.Fatalf("old generation: %v", err)
	}
}

func TestScanConcurrencySlotsOutliveCancellation(t *testing.T) {
	release := make(chan struct{})
	m := scanTestManager(t, func(ctx context.Context, scope RightsizingScanScope, _ func(RightsizingScanResponse)) RightsizingScanResponse {
		<-release
		return newRightsizingScanResponse(time.Now(), scope)
	})
	defer close(release)
	request := scanTestRequest(m)
	for _, ns := range []string{"a", "b"} {
		request.Namespaces = []string{ns}
		if _, err := m.Resolve(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	request.Namespaces = []string{"c"}
	if _, err := m.Resolve(context.Background(), request); !errors.Is(err, ErrRightsizingScanBusy) {
		t.Fatalf("third active job: %v", err)
	}
	m.Invalidate()
	request.Generation = m.Generation()
	if _, err := m.Resolve(context.Background(), request); !errors.Is(err, ErrRightsizingScanBusy) {
		t.Fatalf("cancellation freed still-running workers: %v", err)
	}
}

func TestScanTimeoutAndStopRetainPartialEvidence(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "stop"}[stop], func(t *testing.T) {
			published := make(chan struct{})
			m := scanTestManager(t, func(ctx context.Context, scope RightsizingScanScope, publish func(RightsizingScanResponse)) RightsizingScanResponse {
				result := newRightsizingScanResponse(time.Now(), scope)
				result.Coverage.WorkloadsEvaluated = 50
				publish(result)
				close(published)
				<-ctx.Done()
				return result
			})
			m.duration = 30 * time.Millisecond
			if stop {
				m.duration = time.Second
			}
			request := scanTestRequest(m)
			first, err := m.Resolve(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			<-published
			request.ID, request.Cancel = first.ScanID, stop
			final := awaitScan(t, m, request)
			want := "timed_out"
			if stop {
				want = "cancelled"
			}
			if final.ScanStatus != want || final.State != RightsizingScanPartial || final.Coverage.WorkloadsEvaluated != 50 || final.ExpiresAt == nil || final.PollAfterSeconds != 0 {
				t.Fatalf("final: %+v", final)
			}
		})
	}
}

func TestScanSourceInvalidationAndRetainedLimits(t *testing.T) {
	var valid atomic.Bool
	valid.Store(true)
	m := scanTestManager(t, func(_ context.Context, scope RightsizingScanScope, _ func(RightsizingScanResponse)) RightsizingScanResponse {
		result := newRightsizingScanResponse(time.Now(), scope)
		result.State = RightsizingScanComplete
		return result
	})
	run, _ := m.prepare()
	m.prepare = func() (scanRunner, func() bool) { return run, valid.Load }
	request := scanTestRequest(m)
	first := awaitScan(t, m, request)
	for i := 0; i < rightsizingScanMaxRetained; i++ {
		request.Refresh = true
		awaitScan(t, m, request)
	}
	request.ID = first.ScanID
	if _, err := m.Resolve(context.Background(), request); !errors.Is(err, ErrRightsizingScanNotFound) {
		t.Fatalf("old result wasn't evicted: %v", err)
	}
	request.ID, request.Refresh = "", false
	last, err := m.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	valid.Store(false)
	request.ID = last.ScanID
	if _, err := m.Resolve(context.Background(), request); !errors.Is(err, ErrRightsizingScanNotFound) {
		t.Fatalf("new backend served old result: %v", err)
	}
}

func TestScanScopeKeyPreservesNilAndNormalizesOrder(t *testing.T) {
	a := RightsizingScanScope{NamespacesByKind: map[string][]string{"Deployment": {"b", "a"}}, RestrictedKinds: []string{"StatefulSet", "DaemonSet"}}
	b := RightsizingScanScope{NamespacesByKind: map[string][]string{"Deployment": {"a", "b"}}, RestrictedKinds: []string{"DaemonSet", "StatefulSet"}}
	if scanScopeKey([]string{"b", "a"}, a) != scanScopeKey([]string{"a", "b"}, b) {
		t.Fatal("equivalent scopes did not join")
	}
	if scanScopeKey(nil, a) == scanScopeKey([]string{}, a) {
		t.Fatal("cluster-wide and no-access scopes collided")
	}
}
