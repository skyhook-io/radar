package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
)

const testToken = "rhc_SUPERSECRET_TEST_TOKEN"

type fakePrepared struct {
	mode      cloudinstall.ProvisionMode
	namespace string
	release   string
}

func (f *fakePrepared) Mode() cloudinstall.ProvisionMode { return f.mode }
func (f *fakePrepared) Namespace() string                { return f.namespace }
func (f *fakePrepared) ReleaseName() string              { return f.release }
func (f *fakePrepared) ChartVersion() string             { return "1.9.0" }
func (f *fakePrepared) AppVersion() string               { return "1.9.0" }
func (f *fakePrepared) CurrentChartVersion() string      { return "1.8.0" }
func (f *fakePrepared) CurrentRevision() int             { return 3 }
func (f *fakePrepared) CurrentValues() helm.CloudUpgradeValuesSummary {
	return helm.CloudUpgradeValuesSummary{}
}
func (f *fakePrepared) CurrentManifest() string { return "" }
func (f *fakePrepared) TargetManifest() string  { return "kind: Deployment" }
func (f *fakePrepared) Deployment() helm.DeploymentRef {
	return helm.DeploymentRef{Namespace: f.namespace, Name: f.release}
}

type fakeConnectClient struct {
	cr              *cloud.CreateResponse
	createErr       error
	pollErr         error
	approve         chan *cloud.PollResponse
	approveOnCancel *cloud.PollResponse
	consume         chan error
	// beforeApprovalReturn runs just before an approval is handed back, so a
	// test can widen the window between "approved" and the provisioning claim.
	beforeApprovalReturn func()
	// beforeCreateReturn runs while the Hub connect request is still in flight.
	beforeCreateReturn func()
}

func (f *fakeConnectClient) Create(_ context.Context, _ cloud.ConnectMetadata) (*cloud.CreateResponse, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.beforeCreateReturn != nil {
		f.beforeCreateReturn()
	}
	return f.cr, nil
}

func (f *fakeConnectClient) PollUntilApproved(ctx context.Context, _ *cloud.CreateResponse) (*cloud.PollResponse, error) {
	if f.pollErr != nil {
		return nil, f.pollErr
	}
	select {
	case pr := <-f.approve:
		if f.beforeApprovalReturn != nil {
			f.beforeApprovalReturn()
		}
		return pr, nil
	case <-ctx.Done():
		if f.approveOnCancel != nil {
			// Models finalPollAfterCancellation catching a racing approval.
			return f.approveOnCancel, nil
		}
		// The real client wraps cancellation with an explicit "approval may
		// have created a cluster" warning rather than returning it bare.
		return nil, fmt.Errorf("%w: browser approval may have created a cluster in the Hub", ctx.Err())
	}
}

func (f *fakeConnectClient) WaitUntilConsumed(ctx context.Context, _ *cloud.CreateResponse, _ time.Duration) error {
	select {
	case err := <-f.consume:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func testCreateResponse() *cloud.CreateResponse {
	return &cloud.CreateResponse{
		RequestID:            "req_1",
		DeviceSecret:         "devsecret",
		ConnectURL:           "https://app.test.example/connect/req_1",
		ExpiresIn:            900,
		TokenPickupExpiresIn: 1800,
		PollInterval:         2,
	}
}

type managerFixture struct {
	m         *cloudInstallManager
	connect   *fakeConnectClient
	provision struct {
		gotToken   string
		gotCluster string
		err        error
		block      chan struct{}
	}
}

func newManagerFixture(mode cloudinstall.ProvisionMode, planMode cloudinstall.InstallPlanMode, scanErr error) *managerFixture {
	return newManagerFixtureOn(mode, planMode, scanErr, false)
}

func newManagerFixtureOn(mode cloudinstall.ProvisionMode, planMode cloudinstall.InstallPlanMode, scanErr error, shared bool) *managerFixture {
	fx := &managerFixture{
		connect: &fakeConnectClient{
			cr:      testCreateResponse(),
			approve: make(chan *cloud.PollResponse, 1),
			consume: make(chan error, 1),
		},
	}
	prepared := &fakePrepared{mode: mode, namespace: "radar", release: "radar"}
	fx.m = &cloudInstallManager{
		cfg:            CloudConnectConfig{HubAPIURL: "https://api.test.example", HubAppURL: "https://app.test.example"},
		sharedListener: func() bool { return shared },
		backend: cloudInstallBackend{
			captureClients: func() (cloudInstallClients, string, error) {
				return cloudInstallClients{}, "kind-test", nil
			},
			inspectPlan: func(context.Context, cloudInstallClients, string, string) (cloudinstall.InstallPlan, error) {
				return cloudinstall.InstallPlan{Mode: planMode, Namespace: "radar", Release: "radar", ClusterWideScanError: scanErr}, nil
			},
			prepare: func(context.Context, cloudInstallClients, cloudinstall.PrepareConfig) (preparedInstall, error) {
				return prepared, nil
			},
			preflight: func(context.Context, cloudInstallClients, preparedInstall) (cloudinstall.PreflightResult, error) {
				return cloudinstall.PreflightResult{}, nil
			},
			provision: func(_ context.Context, _ cloudInstallClients, _ preparedInstall, cfg cloudinstall.ProvisionConfig) error {
				fx.provision.gotToken = cfg.Token
				fx.provision.gotCluster = cfg.ClusterID
				if fx.provision.block != nil {
					<-fx.provision.block
				}
				return fx.provision.err
			},
			newConnectClient: func() cloudConnectClient { return fx.connect },
			connectMetadata: func(_ context.Context, _ cloudInstallClients, name string) cloud.ConnectMetadata {
				return cloud.ConnectMetadata{DeploymentMode: "in-cluster", ClusterName: name}
			},
		},
	}
	return fx
}

func waitForState(t *testing.T, m *cloudInstallManager, want string) cloudInstallStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := m.status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %q, want %q (status %+v)", st.State, want, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertNoTokenInStatus is the load-bearing security check: the cluster token
// must never serialize through the status API in any state.
func assertNoTokenInStatus(t *testing.T, m *cloudInstallManager) {
	t.Helper()
	raw, err := json.Marshal(m.status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(raw), testToken) || strings.Contains(string(raw), "SUPERSECRET") {
		t.Fatalf("status JSON leaks the cluster token: %s", raw)
	}
}

func TestCloudInstallCommandTargetUsesContextSource(t *testing.T) {
	m := &cloudInstallManager{
		cfg: CloudConnectConfig{Kubeconfig: "/primary/config"},
		backend: cloudInstallBackend{
			contextSource: func(name string) (string, string, bool) {
				if name != "prod (paris)" {
					t.Fatalf("context source lookup = %q", name)
				}
				return "/clusters/paris.yaml", "prod", true
			},
		},
	}

	got := m.commandTarget("prod (paris)")
	if got.Kubeconfig != "/clusters/paris.yaml" || got.Context != "prod" {
		t.Fatalf("command target = %+v", got)
	}
}

func TestCloudInstallCommandTargetFallsBackOutsideRegistry(t *testing.T) {
	m := &cloudInstallManager{cfg: CloudConnectConfig{Kubeconfig: "/primary/config"}}
	got := m.commandTarget("prod")
	if got.Kubeconfig != "/primary/config" || got.Context != "prod" {
		t.Fatalf("command target = %+v", got)
	}
}

func TestCloudInstallHappyPathFreshNeverLeaksToken(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)

	flow, blocked, err := fx.m.prepare(context.Background())
	if err != nil || blocked != nil {
		t.Fatalf("prepare: flow=%v blocked=%+v err=%v", flow, blocked, err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if st.Plan == nil || st.Plan.Mode != "fresh" || st.Plan.ContextName != "kind-test" || st.Plan.DefaultClusterName == "" {
		t.Fatalf("plan summary = %+v", st.Plan)
	}
	assertNoTokenInStatus(t, fx.m)

	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID, ClusterName: "prod"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	st = waitForState(t, fx.m, cloudFlowAwaitingApproval)
	if st.ConnectURL == "" {
		t.Fatalf("awaiting_approval status = %+v", st)
	}
	assertNoTokenInStatus(t, fx.m)

	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_1", Token: testToken, WSSURL: "wss://api.test.example/agent"}
	waitForState(t, fx.m, cloudFlowWaitingTunnel)
	assertNoTokenInStatus(t, fx.m)

	fx.connect.consume <- nil
	st = waitForState(t, fx.m, cloudFlowConnected)
	if st.Connected == nil || st.Connected.ClusterID != "cl_1" ||
		st.Connected.ClusterURL != "https://app.test.example/c/cl_1" {
		t.Fatalf("connected = %+v", st.Connected)
	}
	if fx.provision.gotToken != testToken || fx.provision.gotCluster != "cl_1" {
		t.Fatalf("provision received token=%q cluster=%q", fx.provision.gotToken, fx.provision.gotCluster)
	}
	assertNoTokenInStatus(t, fx.m)

	if err := fx.m.dismiss(st.FlowID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if got := fx.m.status(); got.State != "idle" {
		t.Fatalf("post-dismiss state = %q", got.State)
	}
}

func TestCloudInstallGitOpsIsBlockedBeforeAnyHubContact(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeGitOps, nil)
	_, blocked, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if blocked == nil || blocked.Reason != "gitops" || !strings.Contains(blocked.Message, "radar cloud install") {
		t.Fatalf("blocked = %+v", blocked)
	}
	if st := fx.m.status(); st.State != "idle" {
		t.Fatalf("blocked prepare retained a flow: %+v", st)
	}
}

func TestCloudInstallPreflightBlockingIsSurfaced(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.m.backend.preflight = func(context.Context, cloudInstallClients, preparedInstall) (cloudinstall.PreflightResult, error) {
		return cloudinstall.PreflightResult{Blocking: []string{"create ClusterRoleBinding radar-cloud-owner"}}, nil
	}
	_, blocked, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if blocked == nil || blocked.Reason != "preflight" || len(blocked.Blocking) != 1 {
		t.Fatalf("blocked = %+v", blocked)
	}
}

func TestCloudInstallMultipleInstallsBlockedWithCLIHint(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.m.backend.inspectPlan = func(context.Context, cloudInstallClients, string, string) (cloudinstall.InstallPlan, error) {
		return cloudinstall.InstallPlan{}, &cloudinstall.MultipleTargetsError{Targets: []cloudinstall.RadarTarget{{Namespace: "a"}, {Namespace: "b"}}}
	}
	_, blocked, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if blocked == nil || !strings.Contains(blocked.Message, "radar cloud install --namespace") {
		t.Fatalf("blocked = %+v", blocked)
	}
}

func TestCloudInstallAdoptionRequiresConsent(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionAdopt, cloudinstall.InstallModeAdopt, nil)
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if st.Plan.CurrentRevision != 3 || st.Plan.CurrentChartVersion != "1.8.0" {
		t.Fatalf("adoption summary = %+v", st.Plan)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("start without consent: %v", err)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID, AcceptAdoption: true}); err != nil {
		t.Fatalf("start with consent: %v", err)
	}
}

func TestCloudInstallUncertaintyRequiresAcknowledgement(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, errors.New("namespaces forbidden"))
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if st.Plan.Uncertainty == "" {
		t.Fatalf("uncertainty missing from summary: %+v", st.Plan)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err == nil || !strings.Contains(err.Error(), "acknowledgement") {
		t.Fatalf("start without ack: %v", err)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID, AcknowledgeIncompleteDiscovery: true}); err != nil {
		t.Fatalf("start with ack: %v", err)
	}
}

func TestCloudInstallApprovalTerminalOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		pollErr   error
		wantKind  string
		retrySafe bool
	}{
		{"expired", cloud.ErrConnectExpired, cloudFailExpired, true},
		{"rejected", cloud.ErrConnectRejected, cloudFailRejected, true},
		{"pickup expired", cloud.ErrConnectPickupExpired, cloudFailPickupExpired, false},
		{"recovery timeout is ambiguous", cloud.ErrConnectRecoveryTimeout, cloudFailApprovalUnknown, false},
		{"transport", errors.New("hub returned 502"), cloudFailConnect, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
			fx.connect.pollErr = tc.pollErr
			_, _, err := fx.m.prepare(context.Background())
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			st := waitForState(t, fx.m, cloudFlowReady)
			if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
				t.Fatalf("start: %v", err)
			}
			st = waitForState(t, fx.m, cloudFlowFailed)
			if st.Failure.Kind != tc.wantKind || st.Failure.RetrySafe != tc.retrySafe {
				t.Fatalf("failure = %+v, want kind %q retrySafe %v", st.Failure, tc.wantKind, tc.retrySafe)
			}
		})
	}
}

// A cancel is never provably harmless: the Hub's approval page stays valid
// until it expires, so an approval can commit after Radar stops polling.
func TestCloudInstallCancelBeforeApprovalIsAmbiguous(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForState(t, fx.m, cloudFlowAwaitingApproval)
	if err := fx.m.cancelFlow(st.FlowID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailApprovalUnknown || st.Failure.RetrySafe {
		t.Fatalf("failure = %+v", st.Failure)
	}
	if st.Failure.Guidance == nil || !strings.HasSuffix(st.Failure.Guidance.ClusterURL, "/clusters") {
		t.Fatalf("guidance = %+v", st.Failure.Guidance)
	}
}

func TestCloudInstallCancelRacingApprovalReportsClusterExists(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.connect.approveOnCancel = &cloud.PollResponse{Status: "approved", ClusterID: "cl_race", Token: testToken, WSSURL: "wss://api.test.example/agent"}
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForState(t, fx.m, cloudFlowAwaitingApproval)
	if err := fx.m.cancelFlow(st.FlowID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailCanceledApproved || st.Failure.RetrySafe {
		t.Fatalf("failure = %+v", st.Failure)
	}
	if st.Failure.Guidance == nil || !strings.Contains(st.Failure.Guidance.ClusterURL, "cl_race") {
		t.Fatalf("guidance = %+v", st.Failure.Guidance)
	}
	assertNoTokenInStatus(t, fx.m)
}

func TestCloudInstallCancelDuringProvisioningRefused(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.provision.block = make(chan struct{})
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_1", Token: testToken, WSSURL: "wss://api.test.example/agent"}
	waitForState(t, fx.m, cloudFlowProvisioning)
	if err := fx.m.cancelFlow(st.FlowID); err == nil || !strings.Contains(err.Error(), "cannot be canceled") {
		t.Fatalf("cancel during provisioning: %v", err)
	}
	close(fx.provision.block)
	fx.connect.consume <- nil
	waitForState(t, fx.m, cloudFlowConnected)
}

func TestCloudInstallProvisionFailureCarriesRecoveryGuidance(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionAdopt, cloudinstall.InstallModeAdopt, nil)
	fx.provision.err = &cloudinstall.AdoptionUpgradeError{Err: errors.New("upgrade failed"), RollbackVerified: true}
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID, AcceptAdoption: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_1", Token: testToken, WSSURL: "wss://api.test.example/agent"}
	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailProvision || st.Failure.Guidance == nil {
		t.Fatalf("failure = %+v", st.Failure)
	}
	g := st.Failure.Guidance
	if !strings.Contains(strings.Join(g.Lines, "\n"), "atomic rollback restored") {
		t.Fatalf("guidance lines = %v", g.Lines)
	}
	if len(g.Inspect) == 0 || !strings.Contains(g.Inspect[0], "helm") {
		t.Fatalf("guidance inspect = %v", g.Inspect)
	}
	assertNoTokenInStatus(t, fx.m)
}

func TestCloudInstallTunnelTimeoutCarriesGuidance(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_1", Token: testToken, WSSURL: "wss://api.test.example/agent"}
	waitForState(t, fx.m, cloudFlowWaitingTunnel)
	fx.connect.consume <- cloud.ErrConnectConsumptionTimeout
	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailTunnelUnconfirmed || st.Failure.Guidance == nil {
		t.Fatalf("failure = %+v", st.Failure)
	}
	if !strings.Contains(st.Failure.Guidance.Summary, "five-minute confirmation window") {
		t.Fatalf("guidance summary = %q", st.Failure.Guidance.Summary)
	}
}

func TestCloudInstallSingleFlightAndStaleFlowIDs(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	_, _, err := fx.m.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)

	if _, _, err := fx.m.prepare(context.Background()); !errors.Is(err, errFlowActive) {
		t.Fatalf("concurrent prepare: %v", err)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: "stale"}); !errors.Is(err, errFlowStale) {
		t.Fatalf("stale start: %v", err)
	}
	if err := fx.m.cancelFlow("stale"); !errors.Is(err, errFlowStale) {
		t.Fatalf("stale cancel: %v", err)
	}
	if err := fx.m.dismiss(st.FlowID); err == nil {
		t.Fatal("dismiss of non-terminal flow succeeded")
	}
	if err := fx.m.cancelFlow(st.FlowID); err != nil {
		t.Fatalf("cancel ready flow (discard): %v", err)
	}
	if got := fx.m.status(); got.State != "idle" {
		t.Fatalf("post-discard state = %q", got.State)
	}
}

func TestCloudInstallEndpointGating(t *testing.T) {
	oldMode := cloudConnectDeploymentMode
	cloudConnectDeploymentMode = func() k8s.DeploymentMode { return k8s.DeploymentModeLocal }
	t.Cleanup(func() { cloudConnectDeploymentMode = oldMode })

	newSrv := func(listen string) *Server {
		return &Server{
			listenAddress:   listen,
			cloudConnectCfg: CloudConnectConfig{HubAPIURL: "https://api.test.example", HubAppURL: "https://app.test.example"},
			cloudInstall:    newCloudInstallManager(CloudConnectConfig{HubAPIURL: "https://api.test.example", HubAppURL: "https://app.test.example"}),
		}
	}

	// A shared listener does NOT disable the lane: /api/resources/apply and
	// pods/exec are ungated there too, so this would hold the weaker capability
	// to a higher bar. The exposure is surfaced on the plan card instead.
	t.Run("non-loopback listener still serves, flagged as shared", func(t *testing.T) {
		srv := newSrv("0.0.0.0")
		w := httptest.NewRecorder()
		srv.handleCloudInstallStatus(w, httptest.NewRequest(http.MethodGet, "/api/cloud/install/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if !srv.sharedListener() {
			t.Fatal("0.0.0.0 not reported as a shared listener")
		}
		if loopback := newSrv("127.0.0.1"); loopback.sharedListener() {
			t.Fatal("127.0.0.1 reported as a shared listener")
		}
	})

	t.Run("auth enabled disables the lane", func(t *testing.T) {
		srv := newSrv("127.0.0.1")
		srv.authConfig.Mode = "proxy"
		if srv.cloudConnectDriverEnabled() {
			t.Fatal("driver lane enabled with auth on")
		}
	})

	t.Run("loopback listener serves idle status with no-store", func(t *testing.T) {
		srv := newSrv("127.0.0.1")
		w := httptest.NewRecorder()
		srv.handleCloudInstallStatus(w, httptest.NewRequest(http.MethodGet, "/api/cloud/install/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control = %q", cc)
		}
		var st cloudInstallStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.State != "idle" {
			t.Fatalf("body = %s err = %v", w.Body.String(), err)
		}
	})

	t.Run("cross-origin mutation is refused", func(t *testing.T) {
		srv := newSrv("127.0.0.1")
		req := httptest.NewRequest(http.MethodPost, "/api/cloud/install/prepare", strings.NewReader("{}"))
		req.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		srv.handleCloudInstallPrepare(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("tunnel-configured deployment gets no capability at all", func(t *testing.T) {
		srv := newSrv("127.0.0.1")
		srv.cloudConnectCfg.CloudTunnelConfigured = true
		if cap := srv.cloudConnectCapability(); cap != nil {
			t.Fatalf("capability = %+v, want nil — an already-connected cluster must not be pitched", cap)
		}
	})

	t.Run("wizard lane capability when driver disabled", func(t *testing.T) {
		t.Setenv("RADAR_CLOUD_FUNNEL", "on")
		srv := newSrv("127.0.0.1")
		srv.authConfig.Mode = "proxy"
		cap := srv.cloudConnectCapability()
		if cap == nil || cap.Lane != "wizard" || cap.AppURL != "https://app.test.example" {
			t.Fatalf("capability = %+v", cap)
		}
	})

	// apiUrl is what enables the dialog's live-copy fetch, so its presence is a
	// server-side decision the frontend never second-guesses.
	t.Run("capability carries the Hub API origin for the live-copy fetch", func(t *testing.T) {
		t.Setenv("RADAR_CLOUD_FUNNEL", "on")
		srv := newSrv("127.0.0.1")
		cap := srv.cloudConnectCapability()
		if cap == nil || cap.APIURL != "https://api.test.example" {
			t.Fatalf("capability = %+v, want apiUrl https://api.test.example", cap)
		}
	})

	t.Run("production constructor wires the shared-listener probe", func(t *testing.T) {
		srv := New(Config{DevMode: true, ListenAddress: "0.0.0.0"})
		if srv.cloudInstall.sharedListener == nil || !srv.cloudInstall.sharedListener() {
			t.Fatal("New did not wire the shared-listener acknowledgement probe")
		}
		if loopback := New(Config{DevMode: true, ListenAddress: "127.0.0.1"}); loopback.cloudInstall.sharedListener() {
			t.Fatal("loopback listener reported as shared")
		}
	})

	t.Run("driver lane capability on loopback", func(t *testing.T) {
		t.Setenv("RADAR_CLOUD_FUNNEL", "on")
		srv := newSrv("127.0.0.1")
		cap := srv.cloudConnectCapability()
		if cap.Lane != "driver" {
			t.Fatalf("capability = %+v", cap)
		}
	})

	t.Run("out-of-cohort rollout hides the capability", func(t *testing.T) {
		t.Setenv("RADAR_CLOUD_FUNNEL", "off")
		srv := newSrv("127.0.0.1")
		if cap := srv.cloudConnectCapability(); cap != nil {
			t.Fatalf("capability = %+v, want nil during staged rollout opt-out", cap)
		}
	})

	t.Run("existing cloud tunnel disables the driver lane", func(t *testing.T) {
		srv := newSrv("127.0.0.1")
		srv.cloudConnectCfg.CloudTunnelConfigured = true
		if srv.cloudConnectDriverEnabled() {
			t.Fatal("driver lane enabled despite --cloud-url")
		}
	})
}

func TestCloudInstallStatusJSONNeverContainsTokenField(t *testing.T) {
	// Compile-time-ish guard: the wire structs must not even declare a token
	// field, so a future refactor can't quietly add one.
	for _, typ := range []any{cloudInstallStatus{}, cloudInstallConnected{}, cloudInstallFailure{}, cloudInstallPlanSummary{}} {
		raw, err := json.Marshal(typ)
		if err != nil {
			t.Fatalf("marshal %T: %v", typ, err)
		}
		if strings.Contains(strings.ToLower(string(raw)), "token") {
			t.Fatalf("%T serializes a token-named field: %s", typ, raw)
		}
	}
	if strings.Contains(strings.ToLower(fmt.Sprintf("%+v", cloudInstallStatus{})), "token") {
		t.Fatal("cloudInstallStatus carries a token-named field")
	}
}

// A cancel accepted while the approval poll is returning must not be followed
// by a Helm apply: the user got a success response for the cancel.
func TestCloudInstallCancelAcceptedBeforeProvisionBlocksTheApply(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.m.backend.provision = func(context.Context, cloudInstallClients, preparedInstall, cloudinstall.ProvisionConfig) error {
		t.Error("provision ran after the cancel was accepted")
		return nil
	}

	if _, _, err := fx.m.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForState(t, fx.m, cloudFlowAwaitingApproval)

	// Cancel from inside the approval hook: the poll has already committed to
	// returning an approval, so this deterministically exercises the window
	// between "approved" and the provisioning claim rather than racing the
	// poll's own select.
	fx.connect.beforeApprovalReturn = func() {
		if err := fx.m.cancelFlow(st.FlowID); err != nil {
			t.Errorf("cancel during approval: %v", err)
		}
	}
	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_race", Token: testToken, WSSURL: "wss://api.test.example/agent"}

	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailCanceledApproved || st.Failure.RetrySafe {
		t.Fatalf("failure = %+v", st.Failure)
	}
	if st.Failure.Guidance == nil || !strings.Contains(st.Failure.Guidance.ClusterURL, "cl_race") {
		t.Fatalf("guidance = %+v", st.Failure.Guidance)
	}
	assertNoTokenInStatus(t, fx.m)
}

// Cancel during `starting` has no context to cancel yet (the Hub request is in
// flight), so it must be honored before the run goroutine is launched.
func TestCloudInstallCancelDuringStartingDoesNotLaunchTheFlow(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	fx.m.backend.provision = func(context.Context, cloudInstallClients, preparedInstall, cloudinstall.ProvisionConfig) error {
		t.Error("provision ran for a canceled start")
		return nil
	}
	if _, _, err := fx.m.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)

	// Cancel lands while Create is still running.
	fx.connect.beforeCreateReturn = func() {
		if err := fx.m.cancelFlow(st.FlowID); err != nil {
			t.Errorf("cancel during starting: %v", err)
		}
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}

	got := fx.m.status()
	if got.State != cloudFlowFailed || got.Failure.Kind != cloudFailCanceled || !got.Failure.RetrySafe {
		t.Fatalf("status = %+v", got)
	}
	// Approval must never have been polled.
	select {
	case fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_x", Token: testToken}:
	default:
		t.Fatal("approval channel was consumed by a flow that should not have launched")
	}
}

// The plan card must be able to warn that anyone reachable on this listener
// could approve the connection into their own org.
func TestCloudInstallPlanFlagsSharedListener(t *testing.T) {
	for _, shared := range []bool{false, true} {
		fx := newManagerFixtureOn(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil, shared)
		if _, _, err := fx.m.prepare(context.Background()); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		st := waitForState(t, fx.m, cloudFlowReady)
		if st.Plan.SharedListener != shared {
			t.Fatalf("sharedListener = %v, want %v", st.Plan.SharedListener, shared)
		}
	}
}

// A shared listener means the approver may not be the operator, and the
// binding outlives the request — so it must be an explicit decision.
func TestCloudInstallSharedListenerRequiresAcknowledgement(t *testing.T) {
	fx := newManagerFixtureOn(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil, true)
	if _, _, err := fx.m.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if !st.Plan.SharedListener {
		t.Fatal("plan did not flag the shared listener")
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err == nil || !strings.Contains(err.Error(), "acknowledgement") {
		t.Fatalf("start without acknowledgement: %v", err)
	}
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID, AcknowledgeSharedListener: true}); err != nil {
		t.Fatalf("start with acknowledgement: %v", err)
	}
}

// Loopback must not demand the extra click.
func TestCloudInstallLoopbackNeedsNoSharedAcknowledgement(t *testing.T) {
	fx := newManagerFixtureOn(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil, false)
	if _, _, err := fx.m.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start on loopback: %v", err)
	}
}

// The shared-listener lane must work for the browser it exists for: a page
// served from the same non-loopback authority is same-origin and must pass,
// while a genuinely foreign origin must not.
func TestSameOriginOKAcceptsTheServingAuthority(t *testing.T) {
	cases := []struct {
		name, host, origin string
		want               bool
	}{
		{"no origin (non-browser)", "10.0.0.5:9280", "", true},
		{"same non-loopback authority", "10.0.0.5:9280", "http://10.0.0.5:9280", true},
		{"hostname case is ignored", "Radar.Example.com:9280", "http://radar.example.com:9280", true},
		{"same loopback authority", "127.0.0.1:9280", "http://127.0.0.1:9280", true},
		{"vite dev proxy, loopback to loopback", "localhost:9280", "http://localhost:9273", true},
		{"bracketed IPv6 loopback without request port", "[::1]", "http://[::1]:9273", true},
		{"foreign origin", "10.0.0.5:9280", "https://evil.example", false},
		{"lookalike hostname", "10.0.0.5:9280", "http://localhost.evil.com", false},
		{"different port on the same non-loopback host", "10.0.0.5:9280", "http://10.0.0.5:9999", false},
		{"loopback origin against a non-loopback host", "10.0.0.5:9280", "http://127.0.0.1:9280", false},
		{"unparseable origin", "10.0.0.5:9280", "://nope", false},
		{"opaque (null) origin", "10.0.0.5:9280", "null", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/cloud/install/prepare", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := sameOriginOK(r); got != tc.want {
				t.Fatalf("sameOriginOK(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

// An admission webhook can quote the Secret it denied, so a provisioning error
// may carry the cluster token. It must not reach the status API — the wire
// structs having no token FIELD is not enough when the value rides inside a
// message string.
// TestSameOriginOKRejectsSchemeDowngrade covers the HTTP-origin-on-HTTPS-request
// case: when the request arrives over TLS (directly or via X-Forwarded-Proto),
// an http:// Origin whose authority matches must still be rejected, because it is
// a different origin from the https:// site.
func TestSameOriginOKRejectsSchemeDowngrade(t *testing.T) {
	cases := []struct {
		name           string
		tls            bool
		forwardedProto string
		origin         string
		want           bool
	}{
		{"https request, http origin (downgrade)", true, "", "http://10.0.0.5:9280", false},
		{"https request, https origin", true, "", "https://10.0.0.5:9280", true},
		{"forwarded https, http origin (downgrade)", false, "https", "http://10.0.0.5:9280", false},
		{"forwarded https, https origin", false, "https", "https://10.0.0.5:9280", true},
		{"plain http request, http origin", false, "", "http://10.0.0.5:9280", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/cloud/install/prepare", nil)
			r.Host = "10.0.0.5:9280"
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.forwardedProto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwardedProto)
			}
			r.Header.Set("Origin", tc.origin)
			if got := sameOriginOK(r); got != tc.want {
				t.Fatalf("sameOriginOK(tls=%v xfp=%q origin=%q) = %v, want %v", tc.tls, tc.forwardedProto, tc.origin, got, tc.want)
			}
		})
	}
}

func TestCloudInstallProvisionErrorNeverLeaksTokenIntoStatus(t *testing.T) {
	fx := newManagerFixture(cloudinstall.ProvisionFresh, cloudinstall.InstallModeFresh, nil)
	// The apiserver folds stringData into base64 `data` before admission runs,
	// so a webhook quoting the object it denied reports the ENCODED token.
	// Exercise both representations.
	encoded := base64.StdEncoding.EncodeToString([]byte(testToken))
	fx.provision.err = fmt.Errorf(
		`admission webhook "policy.example.com" denied the request: Secret radar-cloud-config has disallowed data: {"token":%q} (raw %s)`,
		encoded, testToken,
	)

	// Capture the log sink too — it is as readable as the status API.
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if _, _, err := fx.m.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := waitForState(t, fx.m, cloudFlowReady)
	if _, err := fx.m.start(cloudInstallStartRequest{FlowID: st.FlowID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	fx.connect.approve <- &cloud.PollResponse{Status: "approved", ClusterID: "cl_1", Token: testToken, WSSURL: "wss://api.test.example/agent"}

	st = waitForState(t, fx.m, cloudFlowFailed)
	if st.Failure.Kind != cloudFailProvision {
		t.Fatalf("failure = %+v", st.Failure)
	}
	assertNoTokenInStatus(t, fx.m)

	// The operator still needs to see WHAT failed, just not the credential.
	raw, _ := json.Marshal(st)
	if !strings.Contains(string(raw), "admission webhook") {
		t.Fatalf("redaction destroyed the diagnostic: %s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("expected an explicit redaction marker: %s", raw)
	}
	if strings.Contains(string(raw), encoded) {
		t.Fatalf("status leaks the base64 token: %s", raw)
	}
	if got := logs.String(); strings.Contains(got, testToken) || strings.Contains(got, encoded) {
		t.Fatalf("server log leaks the token: %s", got)
	}
}
