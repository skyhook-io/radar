package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	"github.com/skyhook-io/radar/pkg/readreason"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

const (
	fullToken    = "full-token-0123456789"
	limitedToken = "limited-token-0123456789"
	// scopedToken has viewer on atespace research only: no global role, so
	// no atespace list and no list across atespaces, as Substrate's role
	// model gives a scoped user.
	scopedToken = "scoped-token-0123456789"
)

// fakeControl serves the read RPCs Radar calls. The full token reads
// everything; the limited one may not list across atespaces and is denied
// the "secret" atespace.
type fakeControl struct {
	ateapi.UnimplementedControlServer
	atespaces []string
	actors    []*ateapi.Actor
	templates []*ateapi.ActorTemplate
	workers   []*ateapi.Worker
	egress    map[string]*ateapi.EgressPolicy
}

func principal(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) == 1 {
		return strings.TrimPrefix(v[0], "Bearer ")
	}
	return ""
}

func authInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if p := principal(ctx); p != fullToken && p != limitedToken && p != scopedToken {
		return nil, status.Error(codes.Unauthenticated, "token not recognized")
	}
	return handler(ctx, req)
}

func allowed(ctx context.Context, atespace string) error {
	switch principal(ctx) {
	case scopedToken:
		if atespace != "research" {
			return status.Error(codes.PermissionDenied, "caller lacks viewer on atespace "+atespace)
		}
		return nil
	case limitedToken:
	default:
		return nil
	}
	if atespace == "" || atespace == "secret" {
		return status.Error(codes.PermissionDenied, "caller lacks viewer on atespace "+atespace)
	}
	return nil
}

func paged[T any](items []T, opts *ateapi.ListOptions) ([]T, string) {
	start, _ := strconv.Atoi(opts.GetPageToken())
	size := int(opts.GetPageSize())
	if size <= 0 {
		size = 100
	}
	end := min(start+size, len(items))
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next
}

func (f *fakeControl) ListAtespaces(ctx context.Context, req *ateapi.ListAtespacesRequest) (*ateapi.ListAtespacesResponse, error) {
	if principal(ctx) == scopedToken {
		return nil, status.Error(codes.PermissionDenied, "ListAtespaces requires a global viewer")
	}
	var out []*ateapi.Atespace
	for _, a := range f.atespaces {
		out = append(out, &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{Name: a}})
	}
	page, next := paged(out, req.GetOptions())
	return &ateapi.ListAtespacesResponse{Atespaces: page, NextPageToken: next}, nil
}

func (f *fakeControl) ListWorkers(_ context.Context, req *ateapi.ListWorkersRequest) (*ateapi.ListWorkersResponse, error) {
	page, next := paged(f.workers, req.GetOptions())
	return &ateapi.ListWorkersResponse{Workers: page, NextPageToken: next}, nil
}

func (f *fakeControl) ListActorTemplates(ctx context.Context, req *ateapi.ListActorTemplatesRequest) (*ateapi.ListActorTemplatesResponse, error) {
	if err := allowed(ctx, req.GetAtespace()); err != nil {
		return nil, err
	}
	var out []*ateapi.ActorTemplate
	for _, t := range f.templates {
		if req.GetAtespace() == "" || t.GetMetadata().GetAtespace() == req.GetAtespace() {
			out = append(out, t)
		}
	}
	page, next := paged(out, req.GetOptions())
	return &ateapi.ListActorTemplatesResponse{ActorTemplates: page, NextPageToken: next}, nil
}

func (f *fakeControl) ListActors(ctx context.Context, req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
	if err := allowed(ctx, req.GetAtespace()); err != nil {
		return nil, err
	}
	var out []*ateapi.Actor
	for _, a := range f.actors {
		if req.GetAtespace() == "" || a.GetMetadata().GetAtespace() == req.GetAtespace() {
			out = append(out, a)
		}
	}
	page, next := paged(out, req.GetOptions())
	return &ateapi.ListActorsResponse{Actors: page, NextPageToken: next}, nil
}

func (f *fakeControl) GetActor(ctx context.Context, req *ateapi.GetActorRequest) (*ateapi.Actor, error) {
	if err := allowed(ctx, req.GetActor().GetAtespace()); err != nil {
		return nil, err
	}
	for _, a := range f.actors {
		if a.GetMetadata().GetAtespace() == req.GetActor().GetAtespace() && a.GetMetadata().GetName() == req.GetActor().GetName() {
			return a, nil
		}
	}
	return nil, status.Error(codes.NotFound, "actor not found")
}

func (f *fakeControl) GetActorTemplate(ctx context.Context, req *ateapi.GetActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	if err := allowed(ctx, req.GetActorTemplate().GetAtespace()); err != nil {
		return nil, err
	}
	for _, t := range f.templates {
		if t.GetMetadata().GetAtespace() == req.GetActorTemplate().GetAtespace() && t.GetMetadata().GetName() == req.GetActorTemplate().GetName() {
			return t, nil
		}
	}
	return nil, status.Error(codes.NotFound, "template not found")
}

func (f *fakeControl) GetActorEgressPolicy(_ context.Context, req *ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	if p := f.egress[req.GetActor().GetAtespace()+"/"+req.GetActor().GetName()]; p != nil {
		return p, nil
	}
	return nil, status.Error(codes.NotFound, "no egress policy")
}

func fakeActor(atespace, name, template string, state ateapi.ActorState, updated time.Time) *ateapi.Actor {
	return &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: atespace, Name: name, UpdateTime: timestamppb.New(updated)},
		ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: template},
		Status:        &ateapi.ActorStatus{State: state},
	}
}

func assign(a *ateapi.Actor, worker, pool, pod string) *ateapi.Actor {
	a.Status.WorkerAssignment = &ateapi.WorkerAssignment{
		Worker: &ateapi.ObjectRef{Name: worker}, WorkerNamespace: "agents", WorkerPool: pool, WorkerPod: pod, NodeName: "node-1",
	}
	return a
}

func defaultFake() *fakeControl {
	now := time.Now()
	crashed := fakeActor("research", "lit-0193", "lang", ateapi.ActorState_ACTOR_STATE_CRASHED, now.Add(-4*time.Minute))
	crashed.Status.Crash = &ateapi.ActorCrash{Message: "container agent: OOMKilled (limit 2Gi)", CrashTime: timestamppb.New(now.Add(-4 * time.Minute))}
	suspended := fakeActor("research", "lit-0100", "lang", ateapi.ActorState_ACTOR_STATE_SUSPENDED, now.Add(-time.Hour))
	suspended.Status.Snapshots = []*ateapi.Snapshot{{Generation: 3, Fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY,
		DurableSnapshot: &ateapi.SnapshotStorage{State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED}}}
	return &fakeControl{
		atespaces: []string{"research", "secret", "support"},
		actors: []*ateapi.Actor{
			assign(fakeActor("research", "lit-0201", "lang", ateapi.ActorState_ACTOR_STATE_RUNNING, now.Add(-time.Minute)), "w-1", "agent-pool", "agent-pool-abc"),
			crashed,
			suspended,
			assign(fakeActor("support", "ticket-1", "adk", ateapi.ActorState_ACTOR_STATE_RUNNING, now.Add(-2*time.Minute)), "w-1", "agent-pool", "agent-pool-abc"),
			fakeActor("secret", "hidden-1", "classified", ateapi.ActorState_ACTOR_STATE_RUNNING, now),
		},
		templates: []*ateapi.ActorTemplate{
			{Metadata: &ateapi.ResourceMetadata{Atespace: "research", Name: "lang"}, WorkerSelector: &ateapi.Selector{MatchLabels: map[string]string{"class": "gvisor"}},
				Status: &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: &ateapi.GoldenSnapshotStatus{GoldenTag: &ateapi.ObjectRef{Atespace: "research", Name: "golden-v12"}}}},
			{Metadata: &ateapi.ResourceMetadata{Atespace: "support", Name: "adk"},
				Status: &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: &ateapi.GoldenSnapshotStatus{ErrorMessage: "WarmupFailed: exited 137"}}},
			{Metadata: &ateapi.ResourceMetadata{Atespace: "secret", Name: "classified"}},
		},
		workers: []*ateapi.Worker{{
			Metadata: &ateapi.ResourceMetadata{Name: "w-1"}, WorkerNamespace: "agents", WorkerPool: "agent-pool", WorkerPod: "agent-pool-abc", NodeName: "node-1",
			Labels: map[string]string{"class": "gvisor"},
			Status: &ateapi.WorkerStatus{State: ateapi.WorkerState_WORKER_STATE_ACTIVE,
				Capacity:  &ateapi.WorkerResources{CpuMillicores: 8000, MemoryBytes: 16 << 30, Actors: 8},
				Allocated: &ateapi.WorkerResources{CpuMillicores: 2000, MemoryBytes: 4 << 30, Actors: 2}},
		}},
		egress: map[string]*ateapi.EgressPolicy{"research/lit-0201": {Rules: []*ateapi.EgressRule{
			{Https: &ateapi.HttpsRule{Hostnames: []string{"api.openai.com"}}},
			{TlsPassthrough: &ateapi.TlsPassthroughRule{Hostnames: []string{"*.zendesk.com", "api.openai.com"}}},
		}}},
	}
}

// startFakeSubstrate serves fake over TLS on loopback, with a certificate
// for Substrate's own API name, and returns the endpoint and the CA file.
func startFakeSubstrate(t *testing.T, fake *fakeControl) (string, string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake substrate CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{substrateapi.APIServerName}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}})),
		grpc.UnaryInterceptor(authInterceptor),
	)
	ateapi.RegisterControlServer(srv, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), caFile
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// useTestCluster points the current context at a cluster at host, so
// k8s.CurrentProfileTarget has a binding and a fingerprint.
func useTestCluster(t *testing.T, host string) {
	t.Helper()
	old := k8s.SetTestConfig(&rest.Config{Host: host})
	t.Cleanup(func() { k8s.SetTestConfig(old) })
}

// substrateTestServer is a local Radar on loopback with a Substrate manager
// reading settings from a temporary store. Unless kube names a binding, the
// manager's capture follows the current profile target, as in production.
func substrateTestServer(t *testing.T, kube substrateapi.Kube) *Server {
	t.Helper()
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestProfileSource("/fixture/substrate", "dev", "developer"))
	useTestCluster(t, "https://cluster-a")
	store := substrateapi.NewStore(filepath.Join(t.TempDir(), "config.json"))
	m := substrateapi.NewManager(store, func() (substrateapi.Kube, error) {
		k := kube
		if k.Binding == "" {
			target, err := k8s.CurrentProfileTarget()
			if err != nil {
				return k, err
			}
			k.Binding, k.Fingerprint, k.Context = target.Binding, target.Fingerprint, target.Context
		}
		return k, nil
	})
	t.Cleanup(m.Reset)
	return &Server{listenAddress: "127.0.0.1", substrate: m}
}

// saveTestSettings saves settings for whatever the manager captures now.
func saveTestSettings(t *testing.T, s *Server, settings *substrateapi.Settings) {
	t.Helper()
	k, err := s.substrate.Kube()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.substrate.SaveSettings(context.Background(), substrateapi.Target{Binding: k.Binding, Context: k.Context, Fingerprint: k.Fingerprint}, settings); err != nil {
		t.Fatal(err)
	}
}

func connectFake(t *testing.T, s *Server, fake *fakeControl, token string, atespaces ...string) {
	t.Helper()
	endpoint, caFile := startFakeSubstrate(t, fake)
	saveTestSettings(t, s, &substrateapi.Settings{
		Identity: substrateapi.IdentityTokenFile, TokenFile: tokenFile(t, token), Endpoint: endpoint, CAFile: caFile, Atespaces: atespaces,
	})
}

// settledStatus reads the workspace first, which waits for the snapshot,
// then the status, which never waits.
func settledStatus(t *testing.T, s *Server) substrateStatusResponse {
	t.Helper()
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, nil)
	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status)
	return status
}

// fakeConnection starts a fake Substrate and returns a PUT body that reaches
// it with token.
func fakeConnection(t *testing.T, token string) map[string]any {
	t.Helper()
	endpoint, caFile := startFakeSubstrate(t, defaultFake())
	return map[string]any{"identity": "tokenFile", "tokenFile": tokenFile(t, token), "endpoint": endpoint, "caFile": caFile}
}

// putConnection sends settings with the target a fresh GET returned, as the
// Connect form does.
func putConnection(t *testing.T, s *Server, settings map[string]any, out any) *httptest.ResponseRecorder {
	t.Helper()
	var view substrateConnectionView
	if rec := call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &view); rec.Code != http.StatusOK {
		t.Fatalf("GET connection = %d %s", rec.Code, rec.Body)
	}
	return putConnectionFor(t, s, view.Target, settings, out)
}

// putConnectionFor sends settings with a given target, as a tab holding an
// older view does.
func putConnectionFor(t *testing.T, s *Server, target k8s.ProfileTarget, settings map[string]any, out any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"target": target, "settings": settings})
	return call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: string(data)}, out)
}

type substrateCall struct {
	method, path, body string
	params             map[string]string
	header             map[string]string
	// user is the authenticated caller, as the auth middleware sets it.
	user *auth.User
}

func call(t *testing.T, s *Server, h http.HandlerFunc, c substrateCall, out any) *httptest.ResponseRecorder {
	t.Helper()
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, c.path, strings.NewReader(c.body))
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.user != nil {
		req = req.WithContext(auth.ContextWithUser(req.Context(), c.user))
	}
	if len(c.params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range c.params {
			rctx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v\n%s", c.path, err, rec.Body.String())
		}
	}
	for _, secret := range []string{fullToken, limitedToken, scopedToken} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%s leaked a token: %s", c.path, rec.Body.String())
		}
	}
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.ErrorCode
}

func TestSubstrateStatusNotConfigured(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	var status substrateStatusResponse
	if rec := call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if status.APITier.State != substrateapi.TierNotConfigured || !status.CanConfigure || status.APIService.Name != "api" {
		t.Errorf("status = %+v", status)
	}
	rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors"}, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != substrateapi.CodeNotConfigured {
		t.Errorf("actors before configuring: %d %s", rec.Code, rec.Body)
	}
	var ws substrateWorkspaceResponse
	if rec := call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws); rec.Code != http.StatusOK || ws.Coverage.SnapshotCoverage != nil || ws.ActorStates != nil {
		t.Errorf("workspace without the API answers 200 with no API sections: %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateNotInstalled(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{Client: fake.NewClientset(), Config: &rest.Config{Host: "https://kubernetes"}})
	saveTestSettings(t, s, &substrateapi.Settings{Identity: substrateapi.IdentityShared})
	status := settledStatus(t, s)
	if status.APITier.State != substrateapi.TierFailed || status.APITier.Code != substrateapi.CodeNotInstalled {
		t.Errorf("tier = %+v", status.APITier)
	}
	if status.APITier.Identity == nil || status.APITier.Identity.Principal != substrateapi.SharedPrincipal {
		t.Errorf("the shared identity must name its principal: %+v", status.APITier.Identity)
	}
}

func TestSubstrateConnected(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)

	status := settledStatus(t, s)
	if status.APITier.State != substrateapi.TierConnected || status.APITier.ReadAt.IsZero() || status.APITier.ReadStartedAt.After(status.APITier.ReadAt) || status.APITier.Identity.Mode != substrateapi.IdentityTokenFile || status.APITier.Identity.Principal != "" {
		t.Fatalf("tier = %+v", status.APITier)
	}

	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	if ws.Coverage.Actors.State != substrateapi.CoverageFull || ws.ActorStates.Total != 5 || ws.ActorStates.ByState[model.StateCrashed] != 1 {
		t.Errorf("actor states = %+v coverage %+v", ws.ActorStates, ws.Coverage)
	}
	if ws.Summary.Slots == nil || *ws.Summary.Slots != (model.Usage{Used: 2, Total: 8}) || ws.Summary.Templates.GoldenFailed != 1 {
		t.Errorf("summary = %+v", ws.Summary)
	}
	if len(ws.Pools) != 1 || ws.Pools[0].Capacity != model.CapacityFree || *ws.Pools[0].Actors != 2 {
		t.Errorf("pools = %+v", ws.Pools)
	}
	ids := []string{}
	for _, a := range ws.NeedsAttention {
		ids = append(ids, a.ID)
	}
	if !slices.Contains(ids, model.RuleCrashedActors) || !slices.Contains(ids, "goldenSnapshotFailed:support/adk") {
		t.Errorf("needs attention = %v", ids)
	}
	if len(ws.Atespaces) != 3 || ws.Atespaces[0].Name != "research" || ws.Atespaces[0].Actors.Total != 3 {
		t.Errorf("atespaces = %+v", ws.Atespaces)
	}

	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=research&state=running&limit=1"}, &actors)
	if *actors.Total != 1 || actors.Counts.Total != 3 || actors.Counts.ByState[model.StateCrashed] != 1 || len(actors.Rows) != 1 || actors.Rows[0].Name != "lit-0201" {
		t.Errorf("actors = %+v (counts must ignore the state filter, not the atespace filter)", actors)
	}
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?pool=agents/agent-pool"}, &actors)
	if *actors.Total != 2 || actors.Rows[0].Worker.Pod != "agent-pool-abc" {
		t.Errorf("pool filter = %+v", actors)
	}
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?q=0193"}, &actors)
	if *actors.Total != 1 || actors.Rows[0].Reason != "container agent: OOMKilled (limit 2Gi)" {
		t.Errorf("search = %+v", actors)
	}
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=research&state=crashed,suspended"}, &actors)
	if *actors.Total != 2 || actors.Counts.Total != 3 {
		t.Errorf("several states = total %v counts %+v", *actors.Total, actors.Counts)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?state=running,sleeping"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("one unknown state in a list = %d", rec.Code)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?state=sleeping"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown state = %d", rec.Code)
	}

	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/research/lit-0201", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &detail)
	if detail.Egress.State != "set" || detail.Egress.Rules != 2 || !slices.Equal(detail.Egress.Destinations, []string{"api.openai.com", "*.zendesk.com"}) {
		t.Errorf("egress = %+v", detail.Egress)
	}
	if detail.Template == nil || detail.Template.Golden.State != model.GoldenReady || detail.Worker == nil || !detail.Worker.HasFreeSlot {
		t.Errorf("detail = %+v", detail)
	}
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/research/lit-0193", params: map[string]string{"atespace": "research", "name": "lit-0193"}}, &detail)
	if detail.Egress.State != "none" || detail.Row.State != model.StateCrashed || len(detail.Attention) != 1 || detail.Attention[0].Rule != model.RuleCrashedActors {
		t.Errorf("crashed detail = %+v", detail)
	}
	rec := call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/research/nope", params: map[string]string{"atespace": "research", "name": "nope"}}, nil)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != substrateapi.CodeNotFound {
		t.Errorf("missing actor = %d %s", rec.Code, rec.Body)
	}

	var workers substrateWorkersResponse
	call(t, s, s.handleSubstrateWorkers, substrateCall{path: "/api/substrate/workers?pool=agents/agent-pool"}, &workers)
	if workers.Counts.Active != 1 || len(workers.Workers) != 1 || len(workers.Workers[0].Actors) != 2 || workers.Workers[0].Allocated.Actors != 2 {
		t.Errorf("workers = %+v", workers)
	}
	for _, a := range workers.Workers[0].Actors {
		// The Workers list marks a transition past its budget as stuck from it.
		if a.UpdateTime.IsZero() {
			t.Errorf("assigned actor %s/%s has no updateTime", a.Atespace, a.Name)
		}
	}

	var templates substrateTemplatesResponse
	call(t, s, s.handleSubstrateTemplates, substrateCall{path: "/api/substrate/templates"}, &templates)
	byName := map[string]model.TemplateRow{}
	for _, row := range templates.Templates {
		byName[row.Name] = row
	}
	if lang := byName["lang"]; len(lang.MatchingPools) != 1 || lang.Golden.Tag.Name != "golden-v12" || lang.Actors.Total != 3 || lang.PreferredFidelity != model.FidelityMemory {
		t.Errorf("lang = %+v", lang)
	}
	if adk := byName["adk"]; adk.Golden.State != model.GoldenFailed || adk.Golden.Error != "WarmupFailed: exited 137" {
		t.Errorf("adk = %+v", adk)
	}

	var pool substratePoolResponse
	if rec := call(t, s, s.handleSubstratePool, substrateCall{path: "/api/substrate/workerpools/agents/agent-pool", params: map[string]string{"namespace": "agents", "name": "agent-pool"}}, &pool); rec.Code != http.StatusOK {
		t.Fatalf("pool %d %s", rec.Code, rec.Body)
	}
	if len(pool.Workers) != 1 || len(pool.Workers[0].Actors) != 2 || !slices.Contains(pool.Templates, model.Ref{Atespace: "research", Name: "lang"}) {
		t.Errorf("pool = %+v", pool)
	}
}

func TestSubstrateUnreachable(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	_, caFile := startFakeSubstrate(t, defaultFake())
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := lis.Addr().String()
	lis.Close()
	saveTestSettings(t, s, &substrateapi.Settings{
		Identity: substrateapi.IdentityTokenFile, TokenFile: tokenFile(t, fullToken), Endpoint: closed, CAFile: caFile,
	})
	status := settledStatus(t, s)
	if status.APITier.State != substrateapi.TierFailed || status.APITier.Code != substrateapi.CodeUnreachable {
		t.Errorf("tier = %+v", status.APITier)
	}
	rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors"}, nil)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != substrateapi.CodeUnreachable {
		t.Errorf("actors = %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateUnauthenticated(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), "wrong-token-0123456789")
	status := settledStatus(t, s)
	if status.APITier.Code != substrateapi.CodeUnauthenticated {
		t.Errorf("tier = %+v", status.APITier)
	}
	if strings.Contains(status.APITier.Reason, "wrong-token") {
		t.Errorf("reason carries the token: %q", status.APITier.Reason)
	}
}

func TestSubstratePermissionDeniedForOneAtespace(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), limitedToken)
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	for kind, cov := range map[string]substrateapi.Coverage{"actors": ws.Coverage.Actors, "templates": ws.Coverage.Templates} {
		if cov.State != substrateapi.CoveragePartial || !slices.Equal(cov.DeniedAtespaces, []string{"secret"}) || cov.Code != substrateapi.CodeDenied {
			t.Errorf("%s coverage = %+v", kind, cov)
		}
	}
	if ws.ActorStates.Total != 4 {
		t.Errorf("actors read = %d, want the 4 outside the denied atespace", ws.ActorStates.Total)
	}
	for _, row := range ws.Atespaces {
		if row.Name == "secret" && (row.ActorsRead != atespaceReadDenied || row.Actors != nil || row.TemplatesRead != atespaceReadDenied) {
			t.Errorf("a denied atespace has no counts, not zero: %+v", row)
		}
	}
	rec := call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/secret/hidden-1", params: map[string]string{"atespace": "secret", "name": "hidden-1"}}, nil)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != substrateapi.CodeDenied {
		t.Errorf("denied actor = %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateCapExceeded(t *testing.T) {
	prev := substrateapi.MaxActors
	substrateapi.MaxActors = 3
	t.Cleanup(func() { substrateapi.MaxActors = prev })
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors"}, &actors)
	cov := actors.Coverage
	if cov.State != substrateapi.CoveragePartial || cov.Code != substrateapi.CodeCapExceeded || *cov.Count != 3 || cov.Cap != 3 || *actors.Total != 3 {
		t.Errorf("coverage = %+v total %v", cov, actors.Total)
	}
}

func TestSubstrateSharedListenerRefusesConnection(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	s.listenAddress = "0.0.0.0"
	rec := call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: `{"identity":"shared"}`}, nil)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != substrateapi.CodeSharedRadar {
		t.Errorf("PUT on a shared listener = %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("GET on a shared listener = %d", rec.Code)
	}
	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status)
	if status.CanConfigure || status.APITier.State != substrateapi.TierDisabled || status.APITier.Code != substrateapi.CodeSharedRadar {
		t.Errorf("status = %+v", status)
	}
	if saved, _, _ := s.substrate.SavedSettings(); saved != nil {
		t.Error("refused PUT saved settings")
	}
}

func TestSubstrateCrossSitePutRefused(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	h := s.requireSameOrigin(http.HandlerFunc(s.handlePutSubstrateConnection))
	rec := call(t, s, h.ServeHTTP, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: `{"identity":"shared"}`,
		header: map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}}, nil)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "cross_origin_refused" {
		t.Errorf("cross-site PUT = %d %s", rec.Code, rec.Body)
	}
	if saved, _, _ := s.substrate.SavedSettings(); saved != nil {
		t.Error("cross-site PUT saved settings")
	}
}

func TestSubstrateConnectionRoundTrip(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	body := fakeConnection(t, fullToken)
	path := body["tokenFile"].(string)
	var view substrateConnectionView
	rec := putConnection(t, s, body, &view)
	if rec.Code != http.StatusOK || !view.Saved || view.Settings.TokenFile != path || view.Target.Binding == "" || view.Target.Fingerprint == "" {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []map[string]any{
		{"identity": "tokenFile", "tokenFile": "relative/token"}, {"identity": "root"},
		{"identity": "shared", "caFile": "/x"}, {"identity": "shared", "endpoint": "no-port"},
	} {
		if rec := putConnection(t, s, bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%v accepted: %d", bad, rec.Code)
		}
	}
	if rec := call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: `{"settings":{"identity":"shared"}}`}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a PUT without the reviewed target = %d", rec.Code)
	}
	var current substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &current)
	flat, _ := json.Marshal(map[string]any{"target": current.Target, "identity": "shared"})
	if rec := call(t, s, s.handlePutSubstrateConnection, substrateCall{method: http.MethodPut, path: "/api/substrate/connection", body: string(flat)}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("settings must be nested as GET returns them, not flat: %d", rec.Code)
	}
	if rec := putConnection(t, s, map[string]any{"identity": "none", "tokenFile": "/x"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("identity none with other fields = %d", rec.Code)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, nil).Body.Bytes(), &top)
	if _, ok := top["context"]; ok || top["target"] == nil {
		t.Errorf("GET has no top-level context; target.context is the one: %v", top)
	}
	putConnection(t, s, map[string]any{"identity": "none"}, &view)
	if view.Saved {
		t.Error("identity none must remove the settings")
	}
}

// Two tabs: one reads the form for cluster A, the other switches the context
// to cluster B, then the first saves. Its save carries A's target, so it is
// refused and nothing is saved, for B or for A.
func TestSubstrateConnectionBoundToReviewedTarget(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	var onA substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &onA)

	useTestCluster(t, "https://cluster-b")
	rec := putConnectionFor(t, s, onA.Target, map[string]any{"identity": "tokenFile", "tokenFile": tokenFile(t, fullToken)}, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != substrateapi.CodeTargetChanged {
		t.Fatalf("save reviewed on A, sent after a switch to B = %d %s", rec.Code, rec.Body)
	}
	var onB substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &onB)
	if onB.Saved || onB.Target.Fingerprint == onA.Target.Fingerprint {
		t.Errorf("B after the refused save = %+v", onB)
	}
	if _, ok, _ := s.substrate.Store.Get(onA.Target.Binding); ok {
		t.Error("the refused save wrote settings")
	}

	// A disconnect reviewed on B, sent after a switch back to A, is refused
	// too, and leaves A's settings in place.
	useTestCluster(t, "https://cluster-a")
	if rec := putConnection(t, s, fakeConnection(t, fullToken), nil); rec.Code != http.StatusOK {
		t.Fatalf("save on A = %d %s", rec.Code, rec.Body)
	}
	if rec := putConnectionFor(t, s, onB.Target, map[string]any{"identity": "none"}, nil); rec.Code != http.StatusConflict {
		t.Errorf("disconnect reviewed on B, sent on A = %d", rec.Code)
	}
	var again substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &again)
	if !again.Saved || again.Settings.Identity != substrateapi.IdentityTokenFile {
		t.Errorf("A's settings after the refused disconnect = %+v", again)
	}
}

// When the snapshot is capped, an atespace filter reads that atespace from
// Substrate, and an exact name it still misses is read directly.
func TestSubstrateActorsReachBeyondTheSnapshot(t *testing.T) {
	prev := substrateapi.MaxActors
	substrateapi.MaxActors = 2
	t.Cleanup(func() { substrateapi.MaxActors = prev })
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)

	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors"}, &actors)
	if !slices.Equal(actors.Sources, []string{"snapshot"}) || actors.Coverage.Code != substrateapi.CodeCapExceeded {
		t.Fatalf("fixture: the snapshot must be capped: %+v", actors.Coverage)
	}
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=support"}, &actors)
	if !slices.Equal(actors.Sources, []string{"atespace"}) || actors.Total == nil || *actors.Total != 1 || actors.Rows[0].Name != "ticket-1" ||
		actors.Coverage.State != substrateapi.CoverageFull {
		t.Errorf("an atespace beyond the snapshot = %+v sources %v", actors.Coverage, actors.Sources)
	}
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=research&q=lit-0100"}, &actors)
	if !slices.Equal(actors.Sources, []string{"atespace", "actor"}) || actors.Total == nil || *actors.Total != 1 || actors.Rows[0].Name != "lit-0100" {
		t.Errorf("an exact name beyond a capped atespace read = %+v sources %v", actors.Rows, actors.Sources)
	}
}

func TestSubstrateSettingsForAnotherClusterAreNotUsed(t *testing.T) {
	kube := substrateapi.Kube{Binding: "b", Fingerprint: "cluster-a", Context: "ctx"}
	s := substrateTestServer(t, kube)
	connectFake(t, s, defaultFake(), fullToken)
	s.substrate.Kube = func() (substrateapi.Kube, error) {
		kube.Fingerprint = "cluster-b"
		return kube, nil
	}
	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status)
	if status.APITier.Code != substrateapi.CodeTargetChanged {
		t.Errorf("tier = %+v", status.APITier)
	}
}

// podLogClient serves one Pod's log, as the API server would.
func podLogClient(t *testing.T, namespace, pod, body string) *kubernetes.Clientset {
	t.Helper()
	return podLogsClient(t, map[string]string{namespace + "/" + pod: body})
}

// podLogsClient serves the logs of several Pods, keyed namespace/pod, and
// answers NotFound for any other Pod.
func podLogsClient(t *testing.T, logs map[string]string) *kubernetes.Clientset {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, body := range logs {
			ns, pod, _ := strings.Cut(key, "/")
			if r.URL.Path == "/api/v1/namespaces/"+ns+"/pods/"+pod+"/log" {
				if r.URL.Query().Get("container") != "" {
					t.Errorf("the worker Pod log is read without a container, as kubectl ate does")
				}
				w.Write([]byte(body))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404,"message":"pods not found"}`))
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSubstrateActorLogs(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	label := func(atespace, actor, container string) string {
		l := fmt.Sprintf(`"ate.atespace":%q,"ate.actor.name":%q`, atespace, actor)
		if container != "" {
			l += fmt.Sprintf(`,"ate.actor.container.name":%q`, container)
		}
		return `"labels":{` + l + `}`
	}
	log := strings.Join([]string{
		`2026-11-17T15:47:58Z {"message":"loading papers",` + label("research", "lit-0201", "agent") + `}`,
		`2026-11-17T15:47:59Z {"message":"other actor",` + label("research", "lit-0202", "agent") + `}`,
		`2026-11-17T15:48:00Z {"message":"same name elsewhere",` + label("support", "lit-0201", "agent") + `}`,
		`2026-11-17T15:48:01Z {"message":"restored snapshot gen 3",` + label("research", "lit-0201", "") + `}`,
		`2026-11-17T15:48:02Z worker plain text`,
	}, "\n")
	prev := k8s.SetTestClient(podLogClient(t, "agents", "agent-pool-abc", log))
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	read := func(query string) substrateLogsResponse {
		var resp substrateLogsResponse
		rec := call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/api/substrate/actors/research/lit-0201/logs" + query, params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &resp)
		if rec.Code != http.StatusOK {
			t.Fatalf("logs%s = %d %s", query, rec.Code, rec.Body)
		}
		return resp
	}
	all := read("")
	if got := lineMessages(all.Lines); !slices.Equal(got, []string{"loading papers", "restored snapshot gen 3"}) || all.Worker.Pod != "agent-pool-abc" || all.Scanned != 5 {
		t.Errorf("all = %v (%+v)", got, all)
	}
	if got := lineMessages(read("?source=containers").Lines); !slices.Equal(got, []string{"loading papers"}) {
		t.Errorf("containers = %v", got)
	}
	lifecycle := read("?source=lifecycle")
	if got := lineMessages(lifecycle.Lines); !slices.Equal(got, []string{"restored snapshot gen 3"}) || lifecycle.Lines[0].Container != "" {
		t.Errorf("lifecycle = %+v", lifecycle.Lines)
	}
	if got := read("?tailLines=1").Lines; len(got) != 1 || got[0].Message != "restored snapshot gen 3" {
		t.Errorf("tail keeps the newest lines: %+v", got)
	}
	rec := call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs?source=lifecycle&container=agent", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("lifecycle with a container = %d", rec.Code)
	}

	var suspended substrateLogsResponse
	call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs", params: map[string]string{"atespace": "research", "name": "lit-0100"}}, &suspended)
	if suspended.Worker != nil || len(suspended.Lines) != 0 || !strings.Contains(suspended.Reason, "no worker") || suspended.State != model.StateSuspended {
		t.Errorf("suspended = %+v", suspended)
	}
}

func lineMessages(lines []substrateLogLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Message)
	}
	return out
}

func messages(lines []model.LogLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Message)
	}
	return out
}

func TestSubstrateLogScanSkipsOversizedLines(t *testing.T) {
	line := func(msg string) string {
		return `{"message":"` + msg + `","labels":{"ate.atespace":"research","ate.actor.name":"lit-0201","ate.actor.container.name":"agent"}}`
	}
	huge := `{"message":"` + strings.Repeat("x", 3<<20) + `"}`
	body := strings.Join([]string{line("before"), huge, line("after")}, "\n") + "\n"
	filter, _ := model.NewLogFilter(model.Ref{Atespace: "research", Name: "lit-0201"}, "", "")
	lines, scanned, err := filterSubstrateLog(strings.NewReader(body), filter, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(lines); !slices.Equal(got, []string{"before", "after"}) || scanned != 3 {
		t.Errorf("lines %v scanned %d: an oversized line is skipped, not the end of the scan", got, scanned)
	}
}

func TestSubstrateCaptureRefusedDuringContextOperation(t *testing.T) {
	t.Cleanup(k8s.SetTestContextOperationInProgress(true))
	if _, err := currentSubstrateKube(); !errors.Is(err, substrateapi.ErrClusterChanging) {
		t.Fatalf("capture during a context operation = %v, want ErrClusterChanging", err)
	}
	s := substrateTestServer(t, substrateapi.Kube{})
	s.substrate.Kube = currentSubstrateKube
	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status)
	if status.APITier.Code != substrateapi.CodeContextChanged {
		t.Errorf("tier during a switch = %+v", status.APITier)
	}
	rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors"}, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != substrateapi.CodeContextChanged {
		t.Errorf("actors during a switch = %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateScopedIdentity(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), scopedToken, "research")
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	if cov := ws.Coverage.Actors; cov.State != substrateapi.CoveragePartial || cov.Code != substrateapi.CodeScoped || !slices.Equal(cov.ReadAtespaces, []string{"research"}) {
		t.Errorf("scoped actors coverage = %+v", cov)
	}
	if ws.Coverage.Atespaces.State != substrateapi.CoverageDenied || len(ws.Atespaces) != 1 || ws.Atespaces[0].Name != "research" || ws.Atespaces[0].Actors.Total != 3 {
		t.Errorf("atespace rows come from the scope: %+v", ws.Atespaces)
	}
	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=research"}, &actors)
	if actors.Total == nil || *actors.Total != 3 {
		t.Errorf("the allowed atespace's actors = %+v", actors)
	}

	var view substrateConnectionView
	call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &view)
	if !slices.Equal(view.Settings.Atespaces, []string{"research"}) || view.Settings.Identity != substrateapi.IdentityTokenFile {
		t.Errorf("connection = %+v; the scope never falls back to the shared identity", view.Settings)
	}
	rec := putConnection(t, s, map[string]any{"identity": "shared", "atespaces": []string{"Not_Valid"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid atespace name accepted: %d", rec.Code)
	}
}

func TestSubstrateDetailAttentionBeyondSample(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	for i := range 25 {
		a := fakeActor("research", fmt.Sprintf("crash-%02d", i), "lang", ateapi.ActorState_ACTOR_STATE_CRASHED, time.Now().Add(-time.Duration(i)*time.Second))
		f.actors = append(f.actors, a)
	}
	connectFake(t, s, f, fullToken)
	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/research/lit-0193", params: map[string]string{"atespace": "research", "name": "lit-0193"}}, &detail)
	if len(detail.Attention) != 1 || detail.Attention[0].Rule != model.RuleCrashedActors || slices.Contains(detail.Attention[0].Sample, model.Ref{Atespace: "research", Name: "lit-0193"}) {
		t.Errorf("a crashed actor outside the sample still gets its row: %+v", detail.Attention)
	}
	if detail.Coverage.Actors.State != substrateapi.CoverageFull {
		t.Errorf("detail carries its source coverage: %+v", detail.Coverage)
	}
}

func TestSubstrateTemplateBeyondCap(t *testing.T) {
	prev := substrateapi.MaxListItems
	substrateapi.MaxListItems = 2
	t.Cleanup(func() { substrateapi.MaxListItems = prev })
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	// The fake lists lang, adk, classified; the cap keeps the first two.
	var resp substrateTemplateResponse
	rec := call(t, s, s.handleSubstrateTemplate, substrateCall{path: "/api/substrate/templates/secret/classified", params: map[string]string{"atespace": "secret", "name": "classified"}}, &resp)
	if rec.Code != http.StatusOK || resp.Template.Name != "classified" {
		t.Fatalf("a template beyond the cap = %d %s", rec.Code, rec.Body)
	}
	if resp.Coverage.Templates.Code != substrateapi.CodeCapExceeded {
		t.Errorf("template detail must carry the capped coverage: %+v", resp.Coverage.Templates)
	}
	rec = call(t, s, s.handleSubstrateTemplate, substrateCall{path: "/api/substrate/templates/secret/nope", params: map[string]string{"atespace": "secret", "name": "nope"}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a template Substrate confirms missing = %d", rec.Code)
	}
}

func TestSubstrateKnownEmptyVersusUnknown(t *testing.T) {
	fields := func(v any) map[string]any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got, ok := fields(substrateWorkerRowOf(model.Worker{Name: "empty"}, nil, true, nil))["actors"]; !ok || len(got.([]any)) != 0 {
		t.Errorf("actors read and none assigned must encode as [], got %v (present %v)", got, ok)
	}
	if _, ok := fields(substrateWorkerRowOf(model.Worker{Name: "w"}, nil, false, nil))["actors"]; ok {
		t.Error("actors not read must be absent")
	}
	if got, ok := fields(substratePoolResponse{Templates: []model.Ref{}})["templates"]; !ok || len(got.([]any)) != 0 {
		t.Errorf("no matching template must encode as [], got %v", got)
	}
	if _, ok := fields(substratePoolResponse{})["templates"]; ok {
		t.Error("templates not read must be absent")
	}
}

func TestSubstratePoolEvidenceGatesNegativeClaims(t *testing.T) {
	for _, tc := range []struct {
		state            string
		usable, complete bool
	}{
		{integration.KindCoverageFull, true, true},
		{integration.KindCoverageNotInstalled, true, true},
		{integration.KindCoveragePartial, true, false},
		{integration.KindCoverageSyncing, false, false},
		{integration.KindCoverageUncached, false, false},
		{integration.KindCoverageDenied, false, false},
		{integration.KindCoverageError, false, false},
	} {
		in := substrateInputs(nil, nil, integration.KindCoverage{State: tc.state}, time.Now())
		if in.PoolsUsable != tc.usable || in.PoolsComplete != tc.complete {
			t.Errorf("%s: usable %v complete %v", tc.state, in.PoolsUsable, in.PoolsComplete)
		}
	}
	s := substrateTestServer(t, substrateapi.Kube{})
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	if usable := poolsUsable(ws.Coverage.Pools); usable != (ws.Summary.Pools != nil) {
		t.Errorf("pool counts must be absent exactly when the pool read is unusable: coverage %+v summary %+v", ws.Coverage.Pools, ws.Summary.Pools)
	}
}

func TestSubstrateEveryAPIRouteRejectsSharedRadar(t *testing.T) {
	for _, mode := range []string{"shared-listener", "auth", "cloud"} {
		t.Run(mode, func(t *testing.T) {
			s := substrateTestServer(t, substrateapi.Kube{})
			s.substrate.Kube = func() (substrateapi.Kube, error) {
				t.Error("the API-tier manager was reached in " + mode)
				return substrateapi.Kube{}, nil
			}
			switch mode {
			case "shared-listener":
				s.listenAddress = "0.0.0.0"
			case "auth":
				s.authConfig.Mode = "proxy"
			case "cloud":
				s.cloudConnectCfg.CloudTunnelConfigured = true
			}
			for _, h := range []http.HandlerFunc{s.handleGetSubstrateConnection, s.handlePutSubstrateConnection, s.handleSubstrateActors, s.handleSubstrateActor,
				s.handleSubstrateActorLogs, s.handleSubstrateWorkers, s.handleSubstrateTemplates, s.handleSubstrateTemplate, s.handleSubstratePool} {
				if rec := call(t, s, h, substrateCall{path: "/api/substrate/x"}, nil); rec.Code != http.StatusForbidden {
					t.Errorf("API-tier route = %d %s", rec.Code, rec.Body)
				}
			}
			for _, h := range []http.HandlerFunc{s.handleSubstrateStatus, s.handleSubstrateWorkspace} {
				if rec := call(t, s, h, substrateCall{path: "/api/substrate/x"}, nil); rec.Code != http.StatusOK {
					t.Errorf("Kubernetes-only route = %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}

// The workspace says which checks ran and why the others did not, and an
// actor's page carries what a revert leaves and the pools both selectors
// match.
func TestSubstrateChecksRecoveryAndMatchingPools(t *testing.T) {
	fake := defaultFake()
	for _, a := range fake.actors {
		if a.GetMetadata().GetName() == "lit-0193" {
			a.Status.Snapshots = []*ateapi.Snapshot{
				{Generation: 5, Fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, DurableSnapshot: &ateapi.SnapshotStorage{State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED}},
				{Generation: 6, Fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, DurableSnapshot: &ateapi.SnapshotStorage{State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING}},
			}
		}
	}
	saved := substrateServiceSource
	substrateServiceSource = func(context.Context, []model.PoolRef, []string) *model.Service {
		return &model.Service{Why: "Prometheus is not connected"}
	}
	t.Cleanup(func() { substrateServiceSource = saved })
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, fake, fullToken)
	settledStatus(t, s)

	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	ran := map[string]model.Check{}
	for _, c := range ws.Checks {
		ran[c.Rule] = c
	}
	if !ran[model.RuleCrashedActors].Ran || !ran[model.RuleWorkerDraining].Ran {
		t.Errorf("inventory checks ran: %+v", ws.Checks)
	}
	if c := ran[model.RuleNodeUploadsOverdue]; c.Ran || c.Why == "" {
		t.Errorf("without Prometheus the metrics checks say why they did not run: %+v", c)
	}
	if ws.Service.Read || ws.Service.Why == "" || len(ws.NotAssessed) == 0 {
		t.Errorf("service = %+v, not assessed = %v", ws.Service, ws.NotAssessed)
	}

	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/api/substrate/actors/research/lit-0193", params: map[string]string{"atespace": "research", "name": "lit-0193"}}, &detail)
	r := detail.Recovery
	if r == nil || r.Generation != 5 || len(r.Discarded) != 1 || r.Discarded[0].Why != model.DiscardUploadAbandoned {
		t.Errorf("a revert keeps generation 5 and drops the crashed suspend's upload: %+v", r)
	}
	if detail.Row.ResumeGeneration != 5 {
		t.Errorf("an abandoned pending upload is not the resume source either: %+v", detail.Row)
	}
	if detail.MatchingPools == nil || len(detail.MatchingPools) != 1 || detail.MatchingPools[0].Name != "agent-pool" {
		t.Errorf("matching pools = %+v", detail.MatchingPools)
	}
}

// A save dials the submitted settings and reads once before persisting; a
// failed read saves nothing and says why.
func TestSubstrateConnectionProbedBeforeSave(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	saved := func() bool {
		var view substrateConnectionView
		call(t, s, s.handleGetSubstrateConnection, substrateCall{path: "/api/substrate/connection"}, &view)
		return view.Saved
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := lis.Addr().String()
	lis.Close()
	unreachable := fakeConnection(t, fullToken)
	unreachable["endpoint"] = closed
	for name, tc := range map[string]struct {
		body map[string]any
		code string
	}{
		"unreachable":     {unreachable, substrateapi.CodeUnreachable},
		"unauthenticated": {fakeConnection(t, "wrong-token-0123456789"), substrateapi.CodeUnauthenticated},
	} {
		rec := putConnection(t, s, tc.body, nil)
		if rec.Code != http.StatusBadGateway || errorCode(t, rec) != tc.code || !strings.Contains(rec.Body.String(), "nothing was saved") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "wrong-token") {
			t.Errorf("%s: the error carries the token", name)
		}
		if saved() {
			t.Fatalf("%s: a failed probe saved settings", name)
		}
	}

	// An identity Substrate authenticates but will not let list atespaces
	// is reachable and valid: scoped.
	if rec := putConnection(t, s, fakeConnection(t, scopedToken), nil); rec.Code != http.StatusOK || !saved() {
		t.Errorf("scoped identity = %d %s", rec.Code, rec.Body)
	}
	if rec := putConnection(t, s, map[string]any{"identity": "none"}, nil); rec.Code != http.StatusOK || saved() {
		t.Errorf("a disconnect needs no probe: %d", rec.Code)
	}
}

func TestSubstrateTemplateDetailCarriesSpec(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.templates[0].Containers = []*ateapi.Container{{Name: "agent", Image: "ghcr.io/example/lang:7", Env: []*ateapi.EnvVar{{Name: "DB_PASSWORD", Value: "s3cret-value"}}}}
	connectFake(t, s, f, fullToken)
	var resp substrateTemplateResponse
	rec := call(t, s, s.handleSubstrateTemplate, substrateCall{path: "/api/substrate/templates/research/lang", params: map[string]string{"atespace": "research", "name": "lang"}}, &resp)
	if rec.Code != http.StatusOK || resp.Spec == nil || resp.Spec.Containers[0].Image != "ghcr.io/example/lang:7" {
		t.Fatalf("template detail = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cret-value") {
		t.Error("the spec leaked a secret env value")
	}
}

func TestSubstrateActorsNodeFilter(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	unstored := fakeActor("research", "unstored", "lang", ateapi.ActorState_ACTOR_STATE_SUSPENDED, time.Now())
	unstored.Status.Snapshots = []*ateapi.Snapshot{{Generation: 2, Locality: "node-1", DurableSnapshot: &ateapi.SnapshotStorage{State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING}}}
	f.actors = append(f.actors, unstored)
	connectFake(t, s, f, fullToken)
	names := func(path string) []string {
		var resp substrateActorsResponse
		if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: path}, &resp); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		out := []string{}
		for _, r := range resp.Rows {
			out = append(out, r.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names("/api/substrate/actors?node=node-1&nodeHas=running"); !slices.Equal(got, []string{"lit-0201", "ticket-1"}) {
		t.Errorf("running on node-1 = %v", got)
	}
	if got := names("/api/substrate/actors?node=node-1&nodeHas=localSnapshot"); !slices.Equal(got, []string{"unstored"}) {
		t.Errorf("state only on node-1 = %v", got)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?node=node-1"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("node without nodeHas = %d", rec.Code)
	}
}

func actorLine(atespace, actor, container, msg string) string {
	labels := fmt.Sprintf(`"ate.atespace":%q,"ate.actor.name":%q`, atespace, actor)
	if container != "" {
		labels += fmt.Sprintf(`,"ate.actor.container.name":%q`, container)
	}
	return `2026-11-17T15:48:00Z {"message":"` + msg + `","labels":{` + labels + `}}`
}

func workerPod(namespace, name, node string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{substrateWorkerPodLabel: "agent-pool"}},
		Spec: corev1.PodSpec{NodeName: node}}
}

// A crashed actor has no worker; its lines are searched for on the worker
// Pods of the node it was last on.
func TestSubstrateActorLogsFromLastNode(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.actors[1].Status.AssignedNode = "node-1" // research/lit-0193, crashed
	connectFake(t, s, f, fullToken)
	setPods := func(pods ...*corev1.Pod) {
		prev := substrateWorkerPodsOn
		substrateWorkerPodsOn = func(node string) []*corev1.Pod {
			if node != "node-1" {
				t.Errorf("searched node %s, want the actor's assigned node", node)
			}
			return pods
		}
		t.Cleanup(func() { substrateWorkerPodsOn = prev })
	}
	read := func() substrateLogsResponse {
		var resp substrateLogsResponse
		if rec := call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs", params: map[string]string{"atespace": "research", "name": "lit-0193"}}, &resp); rec.Code != http.StatusOK {
			t.Fatalf("logs = %d %s", rec.Code, rec.Body)
		}
		return resp
	}
	useLogs := func(logs map[string]string) {
		prev := k8s.SetTestClient(podLogsClient(t, logs))
		t.Cleanup(func() { k8s.SetTestClient(prev) })
	}

	t.Run("found on the last node", func(t *testing.T) {
		setPods(workerPod("agents", "agent-pool-old", "node-1"), workerPod("agents", "agent-pool-other", "node-1"))
		useLogs(map[string]string{
			"agents/agent-pool-old":   actorLine("research", "lit-0193", "agent", "OOM soon") + "\n" + actorLine("research", "lit-0193", "", "container agent exited: OOMKilled"),
			"agents/agent-pool-other": actorLine("research", "someone-else", "agent", "not mine"),
		})
		resp := read()
		if resp.Origin != substrateLogOriginLastNode || resp.ReasonCode != "candidates" || resp.Node != "node-1" || len(resp.Searched) != 2 {
			t.Fatalf("resp = %+v", resp)
		}
		if got := lineMessages(resp.Lines); !slices.Equal(got, []string{"OOM soon", "container agent exited: OOMKilled"}) || resp.Lines[0].Pod != "agent-pool-old" {
			t.Errorf("lines = %+v", resp.Lines)
		}
		if resp.Searched[0].Matched != 2 || resp.Searched[1].Matched != 0 {
			t.Errorf("searched = %+v", resp.Searched)
		}
	})
	t.Run("not found names the Pods searched", func(t *testing.T) {
		setPods(workerPod("agents", "agent-pool-other", "node-1"))
		useLogs(map[string]string{"agents/agent-pool-other": actorLine("research", "someone-else", "agent", "not mine")})
		resp := read()
		if resp.ReasonCode != "noMatch" || len(resp.Lines) != 0 || len(resp.Searched) != 1 || resp.Searched[0].Pod != "agent-pool-other" {
			t.Errorf("resp = %+v", resp)
		}
	})
	nodeGone := func(gone bool) {
		prev := substrateNodeGone
		substrateNodeGone = func(*Server, *http.Request, string) bool { return gone }
		t.Cleanup(func() { substrateNodeGone = prev })
	}
	t.Run("the node itself is gone", func(t *testing.T) {
		setPods()
		nodeGone(true)
		if resp := read(); resp.ReasonCode != "nodeGone" {
			t.Errorf("resp = %+v", resp)
		}
	})
	t.Run("no worker Pod on the node: the Pod it ran in is gone", func(t *testing.T) {
		setPods()
		nodeGone(false)
		resp := read()
		if resp.ReasonCode != "noWorkerPods" || !strings.Contains(resp.Reason, "is gone") {
			t.Errorf("resp = %+v", resp)
		}
	})
	t.Run("denied pods/log is skipped and said", func(t *testing.T) {
		setPods(workerPod("restricted", "agent-pool-r", "node-1"))
		prev := substratePodLogAllowed
		substratePodLogAllowed = func(*Server, *http.Request, string) bool { return false }
		t.Cleanup(func() { substratePodLogAllowed = prev })
		resp := read()
		if resp.ReasonCode != "denied" || resp.Searched[0].Outcome != "denied" || len(resp.Lines) != 0 {
			t.Errorf("resp = %+v", resp)
		}
	})
}

func TestSubstrateActorLogsWorkerPodGone(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	prev := k8s.SetTestClient(podLogsClient(t, map[string]string{}))
	t.Cleanup(func() { k8s.SetTestClient(prev) })
	var resp substrateLogsResponse
	rec := call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &resp)
	if rec.Code != http.StatusOK || resp.ReasonCode != "workerPodGone" || resp.Origin != substrateLogOriginWorker || strings.Contains(resp.Reason, "not found") {
		t.Errorf("a gone worker Pod = %d %+v", rec.Code, resp)
	}
}

func TestSubstrateActorDetailWorkerImpact(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.workers[0].Status.State = ateapi.WorkerState_WORKER_STATE_UNAVAILABLE
	connectFake(t, s, f, fullToken)
	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/a", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &detail)
	if detail.WorkerImpact == nil || detail.WorkerImpact.Kind != model.ImpactUnavailable || !strings.Contains(detail.WorkerImpact.Reading, "still records this actor as running") {
		t.Errorf("worker impact = %+v", detail.WorkerImpact)
	}
}

// What a worker's state means for its actors reads the same on every
// surface that lists them, and lifts them in the attention order.
func TestSubstrateRowsCarryWorkerImpact(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.workers[0].Status.State = ateapi.WorkerState_WORKER_STATE_UNAVAILABLE
	f.actors = append(f.actors, fakeActor("research", "stuck-1", "lang", ateapi.ActorState_ACTOR_STATE_RESUMING, time.Now().Add(-time.Hour)))
	prev := substrateWorkerPods
	substrateWorkerPods = func([]model.Worker) model.WorkerPods {
		return model.WorkerPods{"agents/agent-pool-abc": {Ready: true, HasIP: true}}
	}
	t.Cleanup(func() { substrateWorkerPods = prev })
	connectFake(t, s, f, fullToken)

	var list substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?sort=attention"}, &list)
	var order []string
	for _, r := range list.Rows {
		order = append(order, r.Name)
		onW1 := r.Worker != nil && r.Worker.Worker == "w-1"
		if onW1 != (r.WorkerImpact != nil) {
			t.Errorf("%s: worker impact %+v", r.Name, r.WorkerImpact)
		}
		if onW1 && (r.WorkerImpact.PodState != model.PodReady || !strings.Contains(r.WorkerImpact.Reading, "Kubernetes is not the cause")) {
			t.Errorf("%s: the Pod is Ready, so Kubernetes is not the cause: %+v", r.Name, r.WorkerImpact)
		}
	}
	if len(order) < 4 || order[0] != "lit-0193" || !slices.Equal(slices.Sorted(slices.Values(order[1:3])), []string{"lit-0201", "ticket-1"}) || order[3] != "stuck-1" {
		t.Errorf("crashed, then on an unavailable worker, then stuck: %v", order)
	}

	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/a", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &detail)
	if detail.WorkerImpact == nil || detail.WorkerImpact.Reading != list.Rows[slices.Index(order, "lit-0201")].WorkerImpact.Reading {
		t.Errorf("the actor page and the list must read the same: %+v", detail.WorkerImpact)
	}
	if detail.Worker == nil || detail.Worker.PodReady == nil || !*detail.Worker.PodReady {
		t.Errorf("worker row = %+v", detail.Worker)
	}

	var workers substrateWorkersResponse
	call(t, s, s.handleSubstrateWorkers, substrateCall{path: "/api/substrate/workers"}, &workers)
	for _, a := range workers.Workers[0].Actors {
		if a.WorkerImpact == nil || a.WorkerImpact.Kind != model.ImpactUnavailable {
			t.Errorf("assigned actor %s = %+v", a.Name, a.WorkerImpact)
		}
	}
}

// A worker whose process restarted under its actors: the Workers row says
// they lost their sandboxes, and the actor list narrows to exactly them.
func TestSubstrateRestartedWorkerNamesItsLostActors(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.workers[0].Status.RegisteredEpoch, f.workers[0].Status.ObservedEpoch = 4, 3
	for _, a := range f.actors {
		if wa := a.Status.GetWorkerAssignment(); wa != nil {
			wa.WorkerEpoch = 3
		}
	}
	connectFake(t, s, f, fullToken)

	var workers substrateWorkersResponse
	call(t, s, s.handleSubstrateWorkers, substrateCall{path: "/api/substrate/workers"}, &workers)
	ws := workers.Workers[0].WorkerState
	if ws == nil || ws.Kind != model.WorkerReconciling || ws.Lost == nil || *ws.Lost != 2 || !strings.HasPrefix(ws.Reading, "2 actors lost their sandboxes") {
		t.Errorf("worker state = %+v", ws)
	}
	var list substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?worker=agent-pool-abc&impact=restarted"}, &list)
	if list.Total == nil || *list.Total != 2 {
		t.Errorf("the lost actors = %+v", list.Rows)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?impact=lost"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown impact = %d", rec.Code)
	}
}

// A pool's readiness counts the worker Pods the caller can list, not the
// WorkerPool's own status.
func TestSubstratePoolCountsWorkerPods(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	prev := substrateNamespaceWorkerPods
	ready := func(name string, ready bool, deleting bool) *corev1.Pod {
		p := workerPod("agents", name, "node-1")
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
		if deleting {
			now := metav1.Now()
			p.DeletionTimestamp = &now
		}
		return p
	}
	substrateNamespaceWorkerPods = func(ns string) []*corev1.Pod {
		other := ready("other-0", true, false)
		other.Labels[substrateWorkerPodLabel] = "other-pool"
		return []*corev1.Pod{ready("w-0", true, false), ready("w-1", false, false), ready("w-2", true, true), other}
	}
	t.Cleanup(func() { substrateNamespaceWorkerPods = prev })
	pools := []model.Pool{{Namespace: "agents", Name: "agent-pool", Ready: 3}, {Namespace: "agents", Name: "empty-pool"}}
	s.withPoolPods(httptest.NewRequest(http.MethodGet, "/", nil), pools)
	if got := pools[0].Pods; got == nil || *got != (model.PoolPods{Created: 2, Ready: 1, Terminating: 1}) || pools[0].ReadyWorkers() != 1 {
		t.Errorf("agent-pool pods = %+v", got)
	}
	if got := pools[1].Pods; got == nil || *got != (model.PoolPods{}) {
		t.Errorf("a pool with no Pods counts 0 when Pods were read: %+v", got)
	}
	substrateNamespaceWorkerPods = func(string) []*corev1.Pod { return nil }
	unread := []model.Pool{{Namespace: "agents", Name: "agent-pool", Ready: 3}}
	s.withPoolPods(httptest.NewRequest(http.MethodGet, "/", nil), unread)
	if unread[0].Pods != nil || unread[0].ReadyWorkers() != 3 {
		t.Errorf("Pods not read: the status stands, and the count is absent: %+v", unread[0])
	}
}

func TestSubstrateActorsSort(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.actors = append(f.actors, fakeActor("research", "stuck-1", "lang", ateapi.ActorState_ACTOR_STATE_RESUMING, time.Now().Add(-time.Hour)),
		fakeActor("research", "stuck-2", "lang", ateapi.ActorState_ACTOR_STATE_RESUMING, time.Now().Add(-45*time.Minute)))
	connectFake(t, s, f, fullToken)
	names := func(query string) []string {
		var resp substrateActorsResponse
		if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?" + query}, &resp); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", query, rec.Code, rec.Body)
		}
		out := []string{}
		for _, r := range resp.Rows {
			out = append(out, r.Atespace+"/"+r.Name)
		}
		return out
	}
	// The fake's newest change is secret/hidden-1, then lit-0201, ticket-1,
	// lit-0193 (crashed), lit-0100 and stuck-1 (resuming for an hour).
	attention := names("sort=attention")
	// Stuck actors: the one resuming for an hour before the one for 45m.
	if attention[0] != "research/lit-0193" || attention[1] != "research/stuck-1" || attention[2] != "research/stuck-2" || attention[3] != "secret/hidden-1" {
		t.Errorf("attention order = %v", attention)
	}
	if got := names("sort=attention&limit=1&offset=1"); !slices.Equal(got, []string{"research/stuck-1"}) {
		t.Errorf("paging applies after the sort: %v", got)
	}
	byName := names("sort=name")
	if !slices.IsSorted(byName) {
		t.Errorf("name order = %v", byName)
	}
	if updated := names(""); updated[0] != "secret/hidden-1" {
		t.Errorf("the default stays newest change first: %v", updated)
	}
	if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?sort=size"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown sort = %d", rec.Code)
	}
}

// F01: each kind's availability is per atespace. An atespace the actor
// budget never reached has no actor count, not 0, and its templates still
// read; a capped read marks what it read as partial.
func TestSubstrateAtespaceRowsPerKind(t *testing.T) {
	prev := substrateapi.MaxActors
	substrateapi.MaxActors = 2
	t.Cleanup(func() { substrateapi.MaxActors = prev })
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	rows := map[string]substrateAtespaceRow{}
	for _, r := range ws.Atespaces {
		rows[r.Name] = r
	}
	if r := rows["research"]; r.ActorsRead != atespaceReadPartial || r.Actors == nil || r.TemplatesRead != atespaceReadFull || *r.Templates != 1 {
		t.Errorf("research = %+v: a capped read is partial, a lower bound", r)
	}
	if r := rows["support"]; r.ActorsRead != atespaceReadPartial || r.Actors == nil || r.Actors.Total != 0 || r.TemplatesRead != atespaceReadFull {
		t.Errorf("support = %+v", r)
	}

	// The scoped identity reads atespace by atespace, under the full budget.
	substrateapi.MaxActors = prev
	s = substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), scopedToken, "research", "secret")
	ws = substrateWorkspaceResponse{}
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	rows = map[string]substrateAtespaceRow{}
	for _, r := range ws.Atespaces {
		rows[r.Name] = r
	}
	if r := rows["research"]; r.ActorsRead != atespaceReadFull || r.Actors == nil || r.Actors.Total != 3 {
		t.Errorf("scoped research = %+v", r)
	}
	if r := rows["secret"]; r.ActorsRead != atespaceReadDenied || r.Actors != nil || r.Templates != nil {
		t.Errorf("scoped secret = %+v: denied, with no counts", r)
	}
}

// F02: pool detail carries the WorkerPool coverage, and says not-found only
// when absence is proven.
// Under --namespace-scope, a namespace outside the pin is refused before any
// Kubernetes read: the pool's direct get and the worker Pod's log both reach
// past the pinned cache.
func TestSubstrateReadsHonorTheNamespacePin(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("team-a")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	prevGet := substrateWorkerPoolGet
	substrateWorkerPoolGet = func(*http.Request, model.PoolRef) (*unstructured.Unstructured, error) {
		t.Error("the WorkerPool was read outside the pin")
		return nil, nil
	}
	t.Cleanup(func() { substrateWorkerPoolGet = prevGet })
	prevLogs := k8s.SetTestClient(podLogsClient(t, map[string]string{"agents/agent-pool-abc": actorLine("research", "lit-0201", "agent", "hello")}))
	t.Cleanup(func() { k8s.SetTestClient(prevLogs) })

	rec := call(t, s, s.handleSubstratePool, substrateCall{path: "/x", params: map[string]string{"namespace": "agents", "name": "agent-pool"}}, nil)
	if rec.Code != readStatusFor(readreason.OutsideNamespacePin) || errorCode(t, rec) != string(readreason.OutsideNamespacePin) {
		t.Errorf("pool outside the pin = %d %s", rec.Code, rec.Body)
	}
	rec = call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs", params: map[string]string{"atespace": "research", "name": "lit-0201"}}, nil)
	if rec.Code != readStatusFor(readreason.OutsideNamespacePin) || errorCode(t, rec) != string(readreason.OutsideNamespacePin) {
		t.Errorf("worker log outside the pin = %d %s", rec.Code, rec.Body)
	}
}

func TestSubstratePoolDetailAbsence(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	connectFake(t, s, defaultFake(), fullToken)
	get := func(found *unstructured.Unstructured, err error) {
		prev := substrateWorkerPoolGet
		substrateWorkerPoolGet = func(*http.Request, model.PoolRef) (*unstructured.Unstructured, error) { return found, err }
		t.Cleanup(func() { substrateWorkerPoolGet = prev })
	}
	read := func(name string) *httptest.ResponseRecorder {
		return call(t, s, s.handleSubstratePool, substrateCall{path: "/api/substrate/workerpools/agents/" + name, params: map[string]string{"namespace": "agents", "name": name}}, nil)
	}

	var pool substratePoolResponse
	if rec := call(t, s, s.handleSubstratePool, substrateCall{path: "/x", params: map[string]string{"namespace": "agents", "name": "agent-pool"}}, &pool); rec.Code != http.StatusOK || pool.Coverage.Pools.State == "" {
		t.Errorf("pool detail must carry coverage.pools: %d %+v", rec.Code, pool.Coverage)
	}

	listMissed := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ate.dev/v1", "kind": "WorkerPool",
		"metadata": map[string]any{"namespace": "agents", "name": "gpu-pool"}, "spec": map[string]any{"replicas": int64(2)}}}
	get(listMissed, nil)
	if rec := read("gpu-pool"); rec.Code != http.StatusOK {
		t.Errorf("a pool the list missed but a get finds = %d %s", rec.Code, rec.Body)
	}
	get(nil, apierrors.NewNotFound(schema.GroupResource{Group: "ate.dev", Resource: "workerpools"}, "nope"))
	if rec := read("nope"); rec.Code != http.StatusNotFound {
		t.Errorf("proven absent (get NotFound, every worker read) = %d", rec.Code)
	}
	get(nil, errors.New("watch still syncing"))
	if rec := read("nope"); rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != substrateapi.CodeIncomplete {
		t.Errorf("an unreadable pool is not proven absent: %d %s", rec.Code, rec.Body)
	}
}

func TestSubstrateWorkerHeldForAbsentWithoutUpdateTime(t *testing.T) {
	fields := func(v any) map[string]any {
		b, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	if _, ok := fields(substrateWorkerRowOf(model.Worker{Name: "w"}, nil, false, nil))["heldForSeconds"]; ok {
		t.Error("no update time: how long the state held is unknown, not 0")
	}
	if got := fields(substrateWorkerRowOf(model.Worker{Name: "w", UpdateTime: time.Now().Add(-time.Minute)}, nil, false, nil))["heldForSeconds"]; got == nil {
		t.Error("with an update time, heldForSeconds is set")
	}
}

func TestSubstrateSettingsCodesSplit(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	if rec := putConnection(t, s, map[string]any{"identity": "root"}, nil); errorCode(t, rec) != substrateapi.CodeInvalidSettings {
		t.Errorf("a bad request = %s", errorCode(t, rec))
	}
	if err := os.WriteFile(s.substrate.Store.Path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	var status substrateStatusResponse
	call(t, s, s.handleSubstrateStatus, substrateCall{path: "/api/substrate/status"}, &status)
	if status.APITier.State != substrateapi.TierFailed || status.APITier.Code != substrateapi.CodeSettingsFile {
		t.Errorf("an unreadable substrate.json = %+v", status.APITier)
	}
}

// A memory-limit kill is the crash's cause only when it is tied to the
// present crash; an earlier execution's kill stays a record.
func TestSubstrateMemoryKillIsTiedToTheCurrentCrash(t *testing.T) {
	crashAt := time.Date(2026, 11, 17, 15, 50, 0, 0, time.UTC)
	limit := int64(2 << 30)
	kill := func(at time.Time) substrateLogLine {
		return substrateLogLine{LogLine: model.LogLine{Kind: model.LogKindWorker, Time: at, Message: "Killing a sandbox over its memory limit", MemoryLimitBytes: &limit}, Pod: "p"}
	}
	event := func(msg string, at time.Time) substrateLogLine {
		return substrateLogLine{LogLine: model.LogLine{Kind: model.LogKindLifecycle, Time: at, Message: msg}, Pod: "p"}
	}
	crashed := model.Actor{Atespace: "research", Name: "lit", State: model.StateCrashed, Crash: &model.Crash{Message: "worker deleted", Time: crashAt}}
	running := model.Actor{Atespace: "research", Name: "lit", State: model.StateRunning}
	tests := []struct {
		name    string
		actor   model.Actor
		lines   []substrateLogLine
		current bool
	}{
		{"seconds before the crash", crashed, []substrateLogLine{kill(crashAt.Add(-5 * time.Second))}, true},
		{"a day before a worker-deletion crash", crashed, []substrateLogLine{kill(crashAt.Add(-24 * time.Hour))}, false},
		{"a restore came between", crashed, []substrateLogLine{kill(crashAt.Add(-2 * time.Minute)), event("Actor restored", crashAt.Add(-time.Minute))}, false},
		{"a checkpoint does not start an execution", crashed, []substrateLogLine{kill(crashAt.Add(-2 * time.Minute)), event("Actor checkpointing", crashAt.Add(-time.Minute))}, true},
		{"after the crash", crashed, []substrateLogLine{kill(crashAt.Add(time.Minute))}, false},
		{"the actor is running again", running, []substrateLogLine{kill(crashAt)}, false},
	}
	for _, tc := range tests {
		got := memoryKillOf(tc.actor, tc.lines)
		if got == nil || got.Current != tc.current || (got.NextStep == model.CrashedMemoryActorStep(tc.actor)) != tc.current || (got.Command == "kubectl ate update actor lit -a research --template <new-template>") != tc.current {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// The worker's own records about the actor (by its UID, with no labels)
// come back as source worker, and a memory-limit kill gives the crash its
// cause.
func TestSubstrateActorLogsWorkerRecords(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{})
	f := defaultFake()
	f.actors[0].Metadata.Uid = "uid-0201" // research/lit-0201, running on agent-pool-abc
	connectFake(t, s, f, fullToken)
	log := strings.Join([]string{
		actorLine("research", "lit-0201", "agent", "embedding batch 8/9"),
		`2026-11-17T15:48:11Z {"time":"2026-11-17T15:48:11Z","level":"WARN","msg":"Killing a sandbox over its memory limit","ate.atespace":"research","ate.actor.name":"lit-0201","ate.actor.uid":"uid-0201","memoryLimitBytes":2147483648,"ate.stats.memory.usage":2150000000}`,
		`2026-11-17T15:48:12Z {"time":"2026-11-17T15:48:12Z","level":"WARN","msg":"Sandbox died","actorUID":"uid-0201","pid":4100}`,
		`2026-11-17T15:48:13Z {"time":"2026-11-17T15:48:13Z","level":"WARN","msg":"Sandbox died","actorUID":"uid-someone-else","pid":4101}`,
	}, "\n")
	prev := k8s.SetTestClient(podLogClient(t, "agents", "agent-pool-abc", log))
	t.Cleanup(func() { k8s.SetTestClient(prev) })
	read := func(source string) substrateLogsResponse {
		var resp substrateLogsResponse
		if rec := call(t, s, s.handleSubstrateActorLogs, substrateCall{path: "/logs?source=" + source, params: map[string]string{"atespace": "research", "name": "lit-0201"}}, &resp); rec.Code != http.StatusOK {
			t.Fatalf("logs = %d %s", rec.Code, rec.Body)
		}
		return resp
	}
	worker := read("worker")
	if got := lineMessages(worker.Lines); !slices.Equal(got, []string{"Killing a sandbox over its memory limit", "Sandbox died"}) || worker.Lines[0].Kind != model.LogKindWorker {
		t.Errorf("worker lines = %+v", worker.Lines)
	}
	if k := worker.MemoryKill; k == nil || k.LimitBytes != 2147483648 || k.Pod != "agent-pool-abc" || k.UsageBytes == nil {
		t.Errorf("memory kill = %+v", worker.MemoryKill)
	}
	if all := read("all"); len(all.Lines) != 3 || all.MemoryKill == nil {
		t.Errorf("all = %+v", all.Lines)
	}
	if containers := read("containers"); len(containers.Lines) != 1 || containers.MemoryKill != nil {
		t.Errorf("containers = %+v", containers.Lines)
	}
}
