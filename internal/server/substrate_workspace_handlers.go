package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/readscope"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/k8score"
	model "github.com/skyhook-io/radar/pkg/substrate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// Agent Substrate's actors, templates, workers and atespaces live in its own
// API server, which authorizes by Substrate's roles, not Kubernetes RBAC.
// Radar holds one connection per cluster, so a Radar several people use must
// not show one person what another's identity may see. substrateGate decides
// per request; the Kubernetes side (WorkerPools, worker Pods, logs) is read as
// the caller everywhere.
func (s *Server) substrateManager() *substrateapi.Manager {
	s.substrateOnce.Do(func() {
		if s.substrate == nil {
			s.substrate = substrateapi.NewManager(substrateapi.NewStore(config.Path()), currentSubstrateKube)
			s.substrate.CallerDial = s.substrateInstallation().Settings
		}
	})
	return s.substrate
}

// currentSubstrateKube captures the context's identity and its clients in one
// read consistent with context switches, so settings saved for one cluster are
// never paired with another cluster's clients.
func currentSubstrateKube() (substrateapi.Kube, error) {
	if !k8s.IsConnected() {
		return substrateapi.Kube{}, errors.New("not connected to a cluster")
	}
	var target k8s.ProfileTarget
	var targetErr error
	var client kubernetes.Interface
	var cfg *rest.Config
	var operation context.Context
	if !k8s.CaptureClusterReads(func() {
		operation = k8s.OperationContext()
		target, targetErr = k8s.CurrentProfileTarget()
		client, cfg, _, _ = k8s.GetClusterClientSnapshot()
	}) {
		return substrateapi.Kube{}, substrateapi.ErrClusterChanging
	}
	if targetErr != nil {
		return substrateapi.Kube{}, targetErr
	}
	return substrateapi.Kube{
		Client: client, Config: cfg, Context: target.Context, Binding: target.Binding, Fingerprint: target.Fingerprint,
		Generation: strconv.FormatUint(target.ClientGeneration, 10) + "." + strconv.FormatUint(target.OperationGeneration, 10),
		Operation:  operation,
	}, nil
}

func (s *Server) resetSubstrate() {
	s.substrateManager().Reset()
}

// substrateLocal: local CLI or Desktop, auth off, loopback listener. Only
// here are connection settings chosen and saved.
func (s *Server) substrateLocal() bool {
	return s.configManagement() == "local" && !s.authConfig.Enabled() && !s.sharedListener()
}

const substrateSharedReason = "This Radar has no sign-in, so it cannot tell who is asking to read Substrate's API. Enabling sign-in turns on access for people whose own Kubernetes access can mint ate-client's token."

// substrateInstallation is how an in-cluster Radar or Radar Hub reads
// Substrate, from the environment Helm sets; read once, as flags are.
func (s *Server) substrateInstallation() substrateapi.InstallationConfig {
	s.substrateInstallationOnce.Do(func() {
		s.substrateInstallationConfig = substrateapi.InstallationFromEnv()
		if err := s.substrateInstallationConfig.Err; err != nil {
			log.Printf("[substrate] Invalid installation settings: %v", err)
		}
	})
	return s.substrateInstallationConfig
}

func substrateSharedTier() substrateapi.APITier {
	return substrateapi.APITier{State: substrateapi.TierDisabled, Code: substrateapi.CodeSharedRadar, Reason: substrateSharedReason}
}

// substrateCallerClient is the requesting user's own (impersonated or
// user-token) Kubernetes client, which mints their token; tests replace it.
var substrateCallerClient = func(s *Server, r *http.Request) kubernetes.Interface { return s.getClientForRequest(r) }

// substrateGate decides whether this request reads Substrate's API, and as
// whom, before anything is minted, dialed or read from the cache:
//   - a local Radar on a private listener reads with this context's saved
//     settings;
//   - an operator who turned the API off refuses everyone (apiDisabled);
//   - a shared Radar without sign-in refuses everyone (sharedRadar): every
//     request would be Radar's own identity;
//   - otherwise (signed in, in-cluster or Radar Hub, any role) the request
//     reads as ate-system/ate-client, its token minted with the caller's own
//     Kubernetes client. The mint is the authorization: it succeeds only for
//     someone who could run kubectl ate themselves, so nobody sees more than
//     their own access already allows. Radar's ServiceAccount never mints.
func (s *Server) substrateGate(r *http.Request) (*http.Request, *substrateapi.APITier) {
	if s.substrateLocal() {
		return r, nil
	}
	installation := s.substrateInstallation()
	if !installation.Enabled {
		return r, &substrateapi.APITier{State: substrateapi.TierDisabled, Code: substrateapi.CodeAPIDisabled, Reason: "Substrate API access is disabled by the operator (Helm substrate.api.enabled=false)."}
	}
	if !cloudMode() && !s.authConfig.Enabled() {
		tier := substrateSharedTier()
		return r, &tier
	}
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return r, &substrateapi.APITier{State: substrateapi.TierFailed, Code: substrateapi.CodeMintDenied, Reason: "Radar has no signed-in Kubernetes identity for this request, so it cannot create Substrate's token for you."}
	}
	if installation.Err != nil {
		return r, &substrateapi.APITier{State: substrateapi.TierFailed, Code: substrateapi.CodeInvalidSettings, Reason: installation.Err.Error()}
	}
	client := substrateCallerClient(s, r)
	if client == nil {
		return r, &substrateapi.APITier{State: substrateapi.TierFailed, Code: substrateapi.CodeMintDenied, Reason: "Radar could not act as you in Kubernetes, so it cannot create Substrate's token for you.",
			Identity: &substrateapi.Identity{Mode: substrateapi.IdentitySharedAsCaller, Principal: substrateapi.SharedPrincipal}}
	}
	// The proof key is the identity Radar impersonates for this user, the
	// key of every other per-user cache.
	caller := substrateapi.Caller{Client: client, Identity: pkgauth.IdentityCacheKey(user.Username, user.Groups)}
	return r.WithContext(substrateapi.WithCaller(r.Context(), caller)), nil
}

// substrateSnapshot serves the API-tier reads: it writes the error and returns
// false when the tier is not connected. The request it returns is the one
// to make further API-tier calls with (substrateGate).
func (s *Server) substrateSnapshot(w http.ResponseWriter, r *http.Request) (*substrateapi.Snapshot, substrateapi.APITier, *http.Request, bool) {
	r, denied := s.substrateGate(r)
	if denied != nil {
		s.writeSubstrateTier(w, *denied)
		return nil, *denied, r, false
	}
	snap, tier := s.substrateManager().Snapshot(r.Context())
	if snap == nil {
		s.writeSubstrateTier(w, tier)
		return nil, tier, r, false
	}
	return snap, tier, r, true
}

func (s *Server) writeSubstrateTier(w http.ResponseWriter, tier substrateapi.APITier) {
	status := http.StatusServiceUnavailable
	switch tier.Code {
	case substrateapi.CodeNotConfigured, substrateapi.CodeTargetChanged, substrateapi.CodeSettingsFile, substrateapi.CodeContextChanged:
		status = http.StatusConflict
	case substrateapi.CodeSharedRadar, substrateapi.CodeAPIDisabled, substrateapi.CodeMintDenied:
		status = http.StatusForbidden
	}
	s.writeErrorCode(w, status, tier.Code, tier.Reason)
}

var workerPoolKind = integration.WorkspaceKind{Key: "workerpools", Group: "ate.dev", Kind: "WorkerPool", Resource: "workerpools"}

// substratePools reads WorkerPools as the caller, through the workspace
// coverage machinery: a watch still syncing, a namespace Radar does not
// collect and a namespace the caller may not list are each said, never read
// as no pools.
func (s *Server) substratePools(r *http.Request) ([]model.Pool, integration.KindCoverage) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, integration.KindCoverage{State: integration.KindCoverageSyncing}
	}
	access, items := s.readWorkspaceKind(r, cache, workerPoolKind, s.parseNamespacesForUser(r), []string{workerPoolKind.Group}, newSyncBudget(r.Context()))
	now := time.Now()
	pools := make([]model.Pool, 0, len(items))
	for _, u := range items {
		pools = append(pools, poolOf(u, now))
	}
	s.withPoolPods(r, pools)
	return pools, access.Coverage()
}

// substrateNamespaceWorkerPods lists the worker Pods in namespace from
// Radar's cache; nil when the cache does not list Pods. Tests replace it.
var substrateNamespaceWorkerPods = func(namespace string) []*corev1.Pod {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		return nil
	}
	selector, err := labels.Parse(substrateWorkerPodLabel)
	if err != nil {
		return nil
	}
	pods, err := cache.Pods().Pods(namespace).List(selector)
	if err != nil {
		return nil
	}
	return pods
}

// withPoolPods counts each pool's worker Pods from Radar's cache, in the
// namespaces where the caller may list Pods: the count is the caller's
// reading, not Radar's.
func (s *Server) withPoolPods(r *http.Request, pools []model.Pool) {
	byNamespace := map[string]map[string]*model.PoolPods{}
	for i := range pools {
		ns := pools[i].Namespace
		counts, seen := byNamespace[ns]
		if !seen {
			if s.canRead(r, "", "pods", ns, "list") {
				if pods := substrateNamespaceWorkerPods(ns); pods != nil {
					counts = map[string]*model.PoolPods{}
					for _, pod := range pods {
						c := counts[pod.Labels[substrateWorkerPodLabel]]
						if c == nil {
							c = &model.PoolPods{}
							counts[pod.Labels[substrateWorkerPodLabel]] = c
						}
						switch {
						case pod.DeletionTimestamp != nil:
							c.Terminating++
						case podIsReady(pod):
							c.Created++
							c.Ready++
						default:
							c.Created++
						}
					}
				}
			}
			byNamespace[ns] = counts
		}
		if counts == nil {
			continue
		}
		c := counts[pools[i].Name]
		if c == nil {
			c = &model.PoolPods{}
		}
		pods := *c
		pools[i].Pods = &pods
	}
}

// poolsUsable: some WorkerPools were read, or the kind is not installed, so
// there are none.
func poolsUsable(c integration.KindCoverage) bool {
	return c.State == integration.KindCoverageFull || c.State == integration.KindCoveragePartial || c.State == integration.KindCoverageNotInstalled
}

// poolsComplete: every WorkerPool was read, enough to say none matches.
func poolsComplete(c integration.KindCoverage) bool {
	return c.State == integration.KindCoverageFull || c.State == integration.KindCoverageNotInstalled
}

// substrateWorkerPoolGet reads one WorkerPool, confirming a cache miss with
// the API; tests replace it.
var substrateWorkerPoolGet = func(r *http.Request, ref model.PoolRef) (*unstructured.Unstructured, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, errors.New("Radar's cache is not ready")
	}
	return cache.GetDynamicWithGroupVerified(r.Context(), workerPoolKind.Kind, ref.Namespace, ref.Name, workerPoolKind.Group)
}

// substrateWorkerPool reads one WorkerPool directly. A nil pool with a nil
// error is proven absence: the API answered NotFound, or the cluster does not
// serve the kind.
func (s *Server) substrateWorkerPool(r *http.Request, ref model.PoolRef) (*unstructured.Unstructured, error) {
	got, err := substrateWorkerPoolGet(r, ref)
	switch {
	case err == nil:
		return got, nil
	case apierrors.IsNotFound(err), errors.Is(err, k8score.ErrResourceNotFound), errors.Is(err, k8s.ErrUnknownDynamicKind):
		return nil, nil
	}
	return nil, err
}

func poolOf(u *unstructured.Unstructured, now time.Time) model.Pool {
	p := model.Pool{Namespace: u.GetNamespace(), Name: u.GetName(), Labels: u.GetLabels(), CreateTime: u.GetCreationTimestamp().UTC()}
	if desired, ok, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); ok {
		p.Desired = &desired
	}
	p.Ready, _, _ = unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	for _, c := range health.WorkerPoolClasses(u) {
		if c.Name != "" {
			p.SandboxClasses = append(p.SandboxClasses, c.Name)
		}
	}
	v := health.SubstrateWorkerPool(u, now)
	p.Health, p.HealthReason = string(v.Level), v.Reason
	return p
}

type substrateAPIService struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	// Found is whether Radar's cache holds the Service; absent when the
	// caller cannot read Services there.
	Found *bool `json:"found,omitempty"`
}

type substrateStatusResponse struct {
	// Installed: the cluster serves Substrate's WorkerPool kind.
	Installed  bool                 `json:"installed"`
	APIService substrateAPIService  `json:"apiService"`
	APITier    substrateapi.APITier `json:"apiTier"`
	// Coverage.Pools is the coverage of the caller's WorkerPool read, under
	// the same name as on every other Substrate response.
	Coverage struct {
		Pools integration.KindCoverage `json:"pools"`
	} `json:"coverage"`
	// CanConfigure: this Radar may read Substrate's API, so the connection
	// settings can be changed here.
	CanConfigure bool `json:"canConfigure"`
}

func (s *Server) handleSubstrateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	resp := substrateStatusResponse{
		APIService:   substrateAPIService{Namespace: substrateapi.SystemNamespace, Name: substrateapi.APIService, Port: substrateapi.APIPort},
		CanConfigure: s.substrateLocal(),
	}
	if d := k8s.GetResourceDiscovery(); d != nil {
		_, resp.Installed = d.GetResourceWithGroup("WorkerPool", "ate.dev")
	}
	if cache := k8s.GetResourceCache(); cache != nil && cache.Services() != nil && s.canRead(r, "", "services", substrateapi.SystemNamespace, "get") {
		_, err := cache.Services().Services(substrateapi.SystemNamespace).Get(substrateapi.APIService)
		found := err == nil
		resp.APIService.Found = &found
	}
	_, resp.Coverage.Pools = s.substratePools(r)
	if gated, denied := s.substrateGate(r); denied != nil {
		resp.APITier = *denied
	} else {
		_, resp.APITier = s.substrateManager().Peek(gated.Context())
	}
	s.writeJSON(w, resp)
}

// SubstrateLocalConnectionView is a private local Radar's saved connection:
// paths, never a token. It is exported only so JSON flattens it into
// substrateConnectionView.
type SubstrateLocalConnectionView struct {
	Saved bool `json:"saved"`
	// TargetChanged: saved while this context pointed at another cluster, so
	// not used until reconfirmed.
	TargetChanged bool                   `json:"targetChanged"`
	Settings      *substrateapi.Settings `json:"settings"`
	// Target and Revision come back with a save or disconnect, which is
	// refused if the context has moved (targetChanged) or the file changed
	// (conflict): a grant reviewed for one cluster is never saved for another.
	Target   k8s.ProfileTarget `json:"target"`
	Revision string            `json:"revision"`
}

// substrateManagedConnectionView is how an in-cluster Radar or Radar Hub
// reads Substrate, read-only: safe values, never a path. Atespaces are shown
// only to a caller who is connected.
type substrateManagedConnectionView struct {
	Enabled    bool              `json:"enabled"`
	ReadsAs    string            `json:"readsAs"`
	Endpoint   string            `json:"endpoint"`
	ServerName string            `json:"serverName"`
	Trust      string            `json:"trust"`
	Atespaces  *[]string         `json:"atespaces,omitempty"`
	Helm       map[string]string `json:"helm"`
}

// substrateConnectionView answers GET /connection in every mode: view local
// (editable here) or managed (set by Helm), with this caller's own access.
type substrateConnectionView struct {
	View            string               `json:"view"`
	SharedPrincipal string               `json:"sharedPrincipal"`
	Access          substrateapi.APITier `json:"access"`
	*SubstrateLocalConnectionView
	Managed *substrateManagedConnectionView `json:"managed,omitempty"`
}

// requireSubstrateConfigurable admits only a private local Radar's settings
// writes. An installation's (in-cluster or Radar Hub) are its operator's, as
// for every other integration (operator_managed); a local Radar on a shared
// listener has no one person whose settings these would be (sharedRadar).
func (s *Server) requireSubstrateConfigurable(w http.ResponseWriter) bool {
	if !s.substrateLocal() {
		if s.configManagement() == "local" {
			s.writeErrorCode(w, http.StatusForbidden, substrateapi.CodeSharedRadar, "Substrate connection settings are editable only in a local Radar on a private listener without sign-in.")
		} else {
			s.writeErrorCode(w, http.StatusForbidden, "operator_managed", "This Radar reads Substrate as its operator configured it. Update the Helm substrate.api values and restart Radar.")
		}
		return false
	}
	return s.requireConnected(w)
}

func (s *Server) substrateConnectionView(r *http.Request) (substrateConnectionView, error) {
	view := substrateConnectionView{View: "managed", SharedPrincipal: substrateapi.SharedPrincipal}
	gated, denied := s.substrateGate(r)
	var snap *substrateapi.Snapshot
	if denied != nil {
		view.Access = *denied
	} else {
		snap, view.Access = s.substrateManager().Peek(gated.Context())
	}
	if !s.substrateLocal() {
		c := s.substrateInstallation()
		enabled := c.Enabled && (cloudMode() || s.authConfig.Enabled())
		readsAs := "none"
		if enabled {
			readsAs = "eachUser"
		}
		endpoint, serverName := c.Settings.Endpoint, c.Settings.ServerName
		if c.Err != nil {
			endpoint, serverName = "", ""
		}
		view.Managed = &substrateManagedConnectionView{Enabled: enabled, ReadsAs: readsAs, Endpoint: endpoint, ServerName: serverName, Trust: c.Trust(),
			Helm: map[string]string{"api": "substrate.api.enabled", "endpoint": "substrate.api.endpoint", "serverName": "substrate.api.serverName", "ca": "substrate.api.caConfigMap", "atespaces": "substrate.api.atespaces"}}
		if view.Access.State == substrateapi.TierConnected && snap != nil {
			names := append([]string{}, c.Settings.Atespaces...)
			view.Managed.Atespaces = &names
		}
		return view, nil
	}
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		return view, err
	}
	saved, ok, revision, err := s.substrateManager().Store.GetRevision(target.Binding)
	if err != nil {
		return view, err
	}
	view.View = "local"
	view.SubstrateLocalConnectionView = &SubstrateLocalConnectionView{Target: target, Revision: revision, Saved: ok}
	if ok {
		settings := saved.Settings
		view.Settings = &settings
		view.TargetChanged = saved.Target != target.Fingerprint
	}
	return view, nil
}

func (s *Server) handleGetSubstrateConnection(w http.ResponseWriter, r *http.Request) {
	if s.substrateLocal() && !s.requireConnected(w) {
		return
	}
	view, err := s.substrateConnectionView(r)
	if err != nil {
		log.Printf("[substrate] Failed to read the saved connection settings: %v", err)
		s.writeErrorCode(w, http.StatusInternalServerError, substrateapi.CodeSettingsFile, err.Error())
		return
	}
	s.writeJSON(w, view)
}

// substrateConnectionRequest replaces this context's settings. Identity none
// removes them. Target is the one GET /connection returned.
type substrateConnectionRequest struct {
	Target             k8s.ProfileTarget `json:"target"`
	Revision           string            `json:"revision"`
	ConfirmDestination bool              `json:"confirmDestination,omitempty"`
	Action             string            `json:"action,omitempty"`
	// Settings is shaped as GET returns it. Identity none removes this
	// context's settings and takes no other field.
	Settings *substrateapi.Settings `json:"settings"`
}

var errSubstrateTargetChanged = errors.New("the cluster connection changed since these settings were shown; nothing was saved. Review them again for the current context")

func (s *Server) handlePutSubstrateConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireSubstrateConfigurable(w) {
		return
	}
	var req substrateConnectionRequest
	if err := decodeBoundedJSONBody(w, r, 16*1024, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "Substrate connection request is too large")
			return
		}
		s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "invalid Substrate connection request")
		return
	}
	if req.Action != "" && req.Action != "save" && req.Action != "reconfirm" {
		s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "action must be save or reconfirm")
		return
	}
	if req.Settings == nil {
		s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "the request must carry settings, shaped as GET /api/substrate/connection returns them")
		return
	}
	var settings *substrateapi.Settings
	if in := *req.Settings; in.Identity == "none" {
		if in.TokenFile != "" || in.Endpoint != "" || in.CAFile != "" || in.ServerName != "" || len(in.Atespaces) > 0 {
			s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "identity none removes the settings and takes no other field")
			return
		}
	} else {
		settings = &substrateapi.Settings{
			Identity: in.Identity, TokenFile: strings.TrimSpace(in.TokenFile), Endpoint: strings.TrimSpace(in.Endpoint),
			CAFile: strings.TrimSpace(in.CAFile), ServerName: strings.TrimSpace(in.ServerName),
		}
		for _, a := range in.Atespaces {
			if a = strings.TrimSpace(a); a != "" {
				settings.Atespaces = append(settings.Atespaces, a)
			}
		}
	}
	if req.Target.Binding == "" || req.Target.Fingerprint == "" {
		s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "the request must carry the target GET /api/substrate/connection returned")
		return
	}
	current, err := k8s.CurrentProfileTarget()
	if err != nil || !current.Same(req.Target) {
		s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeTargetChanged, errSubstrateTargetChanged.Error())
		return
	}
	saved, exists, revision, err := s.substrateManager().Store.GetRevision(current.Binding)
	if err != nil {
		s.writeErrorCode(w, http.StatusInternalServerError, substrateapi.CodeSettingsFile, err.Error())
		return
	}
	if req.Revision == "" {
		s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "the request must carry the revision GET /api/substrate/connection returned")
		return
	}
	if revision != req.Revision {
		s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeConflict, substrateapi.ErrConflict.Error())
		return
	}
	if settings != nil && exists && saved.Target != current.Fingerprint && req.Action != "reconfirm" {
		s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeTargetChanged, "The saved Kubernetes target changed. Review the current Kubernetes target, then reconfirm these settings or disconnect.")
		return
	}
	if req.Action == "reconfirm" {
		if len(saved.Settings.Atespaces) == 0 {
			saved.Settings.Atespaces = nil
		}
		if !exists || settings == nil || !reflect.DeepEqual(saved.Settings, *settings) {
			s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, "reconfirm must carry the saved settings unchanged")
			return
		}
	}
	if settings != nil && exists && saved.Settings.Identity == settings.Identity && saved.Settings.TokenFile == settings.TokenFile &&
		(saved.Settings.Endpoint != settings.Endpoint || saved.Settings.CAFile != settings.CAFile || saved.Settings.ServerName != settings.ServerName) && !req.ConfirmDestination {
		s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeConfirmDestination, "The saved credential would go to a changed address: "+destinationChanges(saved.Settings, *settings)+".")
		return
	}
	if settings != nil {
		if err := settings.Validate(); err != nil {
			s.writeErrorCode(w, http.StatusBadRequest, substrateapi.CodeInvalidSettings, err.Error())
			return
		}
		// Probe with the submitted settings before persisting them, outside the
		// configuration lock so a slow API cannot hold up a context switch. The
		// target is checked again under the lock before the save.
		k, err := s.substrateManager().Kube()
		if err != nil {
			s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeContextChanged, err.Error())
			return
		}
		if k.Binding != req.Target.Binding || k.Fingerprint != req.Target.Fingerprint {
			s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeTargetChanged, errSubstrateTargetChanged.Error())
			return
		}
		if tierErr := s.substrateManager().Probe(r.Context(), k, *settings); tierErr != nil {
			s.writeErrorCode(w, http.StatusBadGateway, tierErr.Code, "Radar could not read Substrate's API with these settings, so nothing was saved: "+tierErr.Message)
			return
		}
	}
	err = k8s.TryClusterConfiguration(func() error {
		current, err := k8s.CurrentProfileTarget()
		if err != nil {
			return err
		}
		if !current.Same(req.Target) {
			return errSubstrateTargetChanged
		}
		return s.substrateManager().SaveRevision(r.Context(), substrateapi.Target{Binding: current.Binding, Context: current.Context, Fingerprint: current.Fingerprint, Identity: current.Identity}, req.Revision, settings)
	})
	if err != nil {
		var tier *substrateapi.TierError
		switch {
		case errors.Is(err, substrateapi.ErrConflict), errors.Is(err, substrateapi.ErrBusy):
			s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeConflict, err.Error())
		case errors.Is(err, errSubstrateTargetChanged):
			s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeTargetChanged, err.Error())
		case errors.Is(err, k8s.ErrContextConfigurationBusy):
			s.writeErrorCode(w, http.StatusConflict, substrateapi.CodeContextChanged, err.Error())
		case errors.As(err, &tier):
			s.writeErrorCode(w, http.StatusBadRequest, tier.Code, tier.Message)
		default:
			log.Printf("[substrate] Failed to save the connection settings: %v", err)
			s.writeErrorCode(w, http.StatusInternalServerError, substrateapi.CodeSettingsFile, err.Error())
		}
		return
	}
	view, err := s.substrateConnectionView(r)
	if err != nil {
		log.Printf("[substrate] Failed to read the saved connection settings: %v", err)
		s.writeErrorCode(w, http.StatusInternalServerError, substrateapi.CodeSettingsFile, err.Error())
		return
	}
	s.writeJSON(w, view)
}

// substrateInputs builds the derivations' inputs, using a kind only when it
// is at least partly read.
func substrateInputs(snap *substrateapi.Snapshot, pools []model.Pool, poolsCov integration.KindCoverage, now time.Time) model.Inputs {
	in := model.Inputs{Pools: pools, PoolsUsable: poolsUsable(poolsCov), PoolsComplete: poolsComplete(poolsCov), Now: now}
	if snap != nil {
		in.Actors, in.ActorsRead = snap.Actors, snap.Coverage.Actors.Usable()
		in.Templates, in.TemplatesRead = snap.Templates, snap.Coverage.Templates.Usable()
		in.TemplatesComplete = snap.Coverage.Templates.State == substrateapi.CoverageFull
		in.Workers, in.WorkersRead = snap.Workers, snap.Coverage.Workers.Usable()
		in.WorkersComplete = snap.Coverage.Workers.State == substrateapi.CoverageFull
		in.WorkerPods = substrateWorkerPods(snap.Workers)
	}
	return in
}

// substrateWorkerPods reads each worker's Pod from Radar's cache. A Pod of
// the worker's name but another UID is a later Pod, not the worker's, and is
// left out. nil when the cache does not list Pods. The Substrate API tier is
// read only after the caller proves their Substrate access. Tests replace it.
var substrateWorkerPods = func(workers []model.Worker) model.WorkerPods {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		return nil
	}
	out := make(model.WorkerPods, len(workers))
	for _, w := range workers {
		if pod := cachedPod(w.Namespace, w.Pod); pod != nil && (w.PodUID == "" || string(pod.UID) == w.PodUID) {
			out[w.Namespace+"/"+w.Pod] = workerPodOf(pod)
		}
	}
	return out
}

// workerPodOf reads what Substrate's controller tests before it marks a
// worker available (Ready with an IP), and a terminating Pod's
// deletionTimestamp: the deletion request plus the grace period, when the
// Pod is killed.
func workerPodOf(pod *corev1.Pod) model.WorkerPod {
	wp := model.WorkerPod{Ready: podIsReady(pod), HasIP: pod.Status.PodIP != "" || len(pod.Status.PodIPs) > 0}
	if pod.DeletionTimestamp != nil {
		wp.Deadline = pod.DeletionTimestamp.Time
	}
	return wp
}

// substrateServiceSource reads Substrate's metrics; tests replace it, since
// the Prometheus client is process-wide.
var substrateServiceSource = prometheus.SubstrateService

// substrateService asks Prometheus for Substrate's verdicts on the pools the
// caller read as WorkerPools and on the nodes running workers, when the
// caller may read Nodes. Those are the gates of the pool and node pages.
func (s *Server) substrateService(r *http.Request, rows []model.PoolRow, in model.Inputs) *model.Service {
	var pools []model.PoolRef
	for _, row := range rows {
		if row.Kubernetes != nil {
			pools = append(pools, row.Ref())
		}
	}
	var nodes []string
	if in.WorkersRead && s.canRead(r, "", "nodes", "", "get") {
		seen := map[string]bool{}
		for _, w := range in.Workers {
			if w.Node != "" && !seen[w.Node] {
				seen[w.Node] = true
				nodes = append(nodes, w.Node)
			}
		}
	}
	if len(pools) == 0 && len(nodes) == 0 {
		return &model.Service{Why: "no WorkerPool or worker node to ask about"}
	}
	return substrateServiceSource(r.Context(), pools, nodes)
}

// substrateNodeReady maps each Node in Radar's cache to its Ready condition,
// for the upload rule's "node NotReady or gone". Nil when the caller may not
// list Nodes or the cache does not hold them, so the rule says unknown.
func (s *Server) substrateNodeReady(r *http.Request) map[string]bool {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Nodes() == nil || !s.canRead(r, "", "nodes", "", "list") {
		return nil
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		return nil
	}
	ready := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		ready[n.Name] = false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				ready[n.Name] = c.Status == corev1.ConditionTrue
			}
		}
	}
	return ready
}

// templateUIDs maps each template read to its UID; nil when none were read.
// The map is the snapshot's own: callers must not change it.
func templateUIDs(snap *substrateapi.Snapshot) model.TemplateUIDs {
	if snap == nil {
		return nil
	}
	return snap.Index().TemplateUIDs
}

// substrateSummary is the status line. Each part is absent when what it
// counts was not read.
type substrateSummary struct {
	// Pools is absent unless WorkerPools or Substrate's workers were read;
	// with only a partial pool list it counts what was read.
	Pools *struct {
		Total int `json:"total"`
		Short int `json:"short"`
		Full  int `json:"full"`
	} `json:"pools,omitempty"`
	Workers *model.WorkerCounts `json:"workers,omitempty"`
	Slots   *model.Usage        `json:"slots,omitempty"`
	// FreeSlots is [] when workers were read and no pool has a free slot.
	FreeSlots []substratePoolFree `json:"freeSlots,omitzero"`
	Actors    *struct {
		Total   int `json:"total"`
		Crashed int `json:"crashed"`
	} `json:"actors,omitempty"`
	Templates *struct {
		Total        int `json:"total"`
		GoldenFailed int `json:"goldenFailed"`
		// NoMatchingPool counts templates whose selector matches no pool;
		// absent unless the pool list is complete.
		NoMatchingPool *int `json:"noMatchingPool,omitempty"`
	} `json:"templates,omitempty"`
}

// substratePoolFree is how many free actor slots one pool holds, largest
// first, for the "24 free, 21 of them on batch-pool" reading.
type substratePoolFree struct {
	Pool model.PoolRef `json:"pool"`
	Free int64         `json:"free"`
}

// Per-atespace read states of one kind.
const (
	atespaceReadFull = "full"
	// atespaceReadPartial: the read's budget ran out, so the count is a
	// lower bound.
	atespaceReadPartial = "partial"
	atespaceReadDenied  = "denied"
	// atespaceReadUnread: not read, as when the budget ran out first or the
	// settings' scope does not name it.
	atespaceReadUnread = "unread"
)

type substrateAtespaceRow struct {
	Name string `json:"name"`
	// ActorsRead and TemplatesRead say how much of each kind was read for
	// this atespace: full, partial, denied or unread. Actors and Templates
	// are present only when it is full or partial.
	ActorsRead    string             `json:"actorsRead"`
	Actors        *model.StateCounts `json:"actors,omitempty"`
	TemplatesRead string             `json:"templatesRead"`
	Templates     *int               `json:"templates,omitempty"`
}

// substrateWorkspaceCoverage is a response's coverage: each API-tier kind,
// absent unless the API tier is connected, and the WorkerPool read, always
// present.
type substrateWorkspaceCoverage struct {
	*substrateapi.SnapshotCoverage
	Pools integration.KindCoverage `json:"pools"`
}

type substrateWorkspaceResponse struct {
	APITier        substrateapi.APITier       `json:"apiTier"`
	Coverage       substrateWorkspaceCoverage `json:"coverage"`
	Summary        substrateSummary           `json:"summary"`
	ActorStates    *model.StateCounts         `json:"actorStates,omitempty"`
	NeedsAttention []model.Attention          `json:"needsAttention"`
	Pools          []model.PoolRow            `json:"pools"`
	// Checks are the needs-attention rules and whether each ran, so an
	// empty NeedsAttention says what was checked; NotAssessed are the health
	// surfaces no check reads.
	Checks      []model.Check `json:"checks"`
	NotAssessed []string      `json:"notAssessed"`
	// Service is Substrate's own measurements from Prometheus: Read false,
	// with Why, when they were not read. Per pool it is also on Pools[].Service.
	Service model.ServiceSummary `json:"service"`
	// Atespaces is absent unless atespaces were listed or read through the
	// settings' scope; [] when there are none.
	Atespaces []substrateAtespaceRow `json:"atespaces,omitzero"`
}

func (s *Server) handleSubstrateWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	pools, poolsCov := s.substratePools(r)
	var snap *substrateapi.Snapshot
	gated, denied := s.substrateGate(r)
	var tier substrateapi.APITier
	if denied != nil {
		tier = *denied
	} else {
		snap, tier = s.substrateManager().Snapshot(gated.Context())
	}
	now := time.Now()
	in := substrateInputs(snap, pools, poolsCov, now)
	in.NodeReady = s.substrateNodeReady(r)
	rows := model.JoinPools(pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead)
	in.Service = s.substrateService(r, rows, in)
	for i := range rows {
		if ps, ok := in.Service.Pools[rows[i].Ref().Key()]; ok {
			rows[i].Service = &ps
		}
	}
	resp := substrateWorkspaceResponse{
		APITier: tier, Coverage: substrateWorkspaceCoverage{Pools: poolsCov}, NeedsAttention: model.NeedsAttention(in), Pools: rows,
		Checks: model.Checks(in), NotAssessed: model.NotAssessed, Service: model.SummarizeService(in.Service),
	}
	if in.PoolsUsable || in.WorkersRead {
		resp.Summary.Pools = &struct {
			Total int `json:"total"`
			Short int `json:"short"`
			Full  int `json:"full"`
		}{Total: len(rows)}
		for _, p := range rows {
			switch p.Capacity {
			case model.CapacityShort:
				resp.Summary.Pools.Short++
			case model.CapacityFull:
				resp.Summary.Pools.Full++
			}
		}
	}
	if snap == nil {
		s.writeJSON(w, resp)
		return
	}
	resp.Coverage.SnapshotCoverage = &snap.Coverage
	if in.WorkersRead {
		p := model.PlacementOf(snap.Workers)
		resp.Summary.Workers, resp.Summary.Slots = &p.Workers, &p.Slots
		resp.Summary.FreeSlots = []substratePoolFree{}
		for _, row := range rows {
			if row.Placement != nil && row.Placement.Slots.Total > row.Placement.Slots.Used {
				resp.Summary.FreeSlots = append(resp.Summary.FreeSlots, substratePoolFree{Pool: row.Ref(), Free: row.Placement.Slots.Total - row.Placement.Slots.Used})
			}
		}
		slices.SortStableFunc(resp.Summary.FreeSlots, func(a, b substratePoolFree) int { return int(b.Free - a.Free) })
	}
	if in.ActorsRead {
		counts := snap.Index().States
		resp.ActorStates = &counts
		resp.Summary.Actors = &struct {
			Total   int `json:"total"`
			Crashed int `json:"crashed"`
		}{counts.Total, counts.ByState[model.StateCrashed]}
	}
	if in.TemplatesRead {
		known := model.KnownPools(pools, in.Workers)
		trows := model.TemplateRows(snap.Templates, nil, false, known, in.PoolsUsable || in.WorkersRead, in.PoolsComplete, rows)
		t := &struct {
			Total          int  `json:"total"`
			GoldenFailed   int  `json:"goldenFailed"`
			NoMatchingPool *int `json:"noMatchingPool,omitempty"`
		}{Total: len(trows)}
		noPool := 0
		for _, tr := range trows {
			if tr.Golden.State == model.GoldenFailed {
				t.GoldenFailed++
			}
			if tr.MatchingPoolsComplete && len(tr.MatchingPools) == 0 {
				noPool++
			}
		}
		if in.PoolsComplete {
			t.NoMatchingPool = &noPool
		}
		resp.Summary.Templates = t
	}
	if snap.Coverage.Atespaces.Usable() || snap.ScopedAtespaces != nil {
		resp.Atespaces = atespaceRows(snap)
	}
	s.writeJSON(w, resp)
}

func atespaceRows(snap *substrateapi.Snapshot) []substrateAtespaceRow {
	names := snap.ScopedAtespaces
	if snap.Coverage.Atespaces.Usable() {
		names = nil
		for _, a := range snap.Atespaces {
			names = append(names, a.Name)
		}
	}
	templates := map[string]int{}
	for _, t := range snap.Templates {
		templates[t.Atespace]++
	}
	byAtespace := snap.Index().ByAtespace
	rows := make([]substrateAtespaceRow, 0, len(names))
	for _, name := range names {
		row := substrateAtespaceRow{
			Name:          name,
			ActorsRead:    atespaceKindRead(snap.Coverage.Actors, name),
			TemplatesRead: atespaceKindRead(snap.Coverage.Templates, name),
		}
		if row.ActorsRead == atespaceReadFull || row.ActorsRead == atespaceReadPartial {
			c := model.NewStateCounts()
			if counted := byAtespace[name]; counted != nil {
				c = model.StateCounts{Total: counted.Total, ByState: maps.Clone(counted.ByState)}
			}
			row.Actors = &c
		}
		if row.TemplatesRead == atespaceReadFull || row.TemplatesRead == atespaceReadPartial {
			n := templates[name]
			row.Templates = &n
		}
		rows = append(rows, row)
	}
	return rows
}

// atespaceKindRead says how much of one kind was read for one atespace.
func atespaceKindRead(cov substrateapi.Coverage, name string) string {
	switch {
	case !cov.Usable() && cov.State == substrateapi.CoverageDenied:
		return atespaceReadDenied
	case !cov.Usable():
		return atespaceReadUnread
	case slices.Contains(cov.DeniedAtespaces, name):
		return atespaceReadDenied
	case slices.Contains(cov.UnreadAtespaces, name):
		return atespaceReadUnread
	case len(cov.ReadAtespaces) > 0 && !slices.Contains(cov.ReadAtespaces, name):
		return atespaceReadUnread
	case cov.Code == substrateapi.CodeCapExceeded:
		return atespaceReadPartial
	}
	return atespaceReadFull
}

type substrateActorsResponse struct {
	APITier substrateapi.APITier `json:"apiTier"`
	// Coverage is the coverage of the source that answered.
	Coverage substrateapi.Coverage `json:"coverage"`
	// Sources says what answered: snapshot (the shared read), atespace (a
	// direct read of the atespace filter, when the snapshot does not hold
	// all of it), and actor (a direct read of the exact name searched, when
	// that atespace read is itself capped).
	Sources []string `json:"sources"`
	// Total and Counts are absent when actors could not be read. Counts are
	// per state, after every filter but state and before paging.
	Total  *int               `json:"total,omitempty"`
	Counts *model.StateCounts `json:"counts,omitempty"`
	Rows   []model.ActorRow   `json:"rows"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

func splitRef(value string) (string, string) {
	if a, b, ok := strings.Cut(value, "/"); ok {
		return a, b
	}
	return "", value
}

func (s *Server) handleSubstrateActors(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	// state takes a comma-separated list, so one segment can cover several
	// states (the transitional ones).
	stateFilter := map[string]bool{}
	if v := q.Get("state"); v != "" {
		for _, part := range strings.Split(v, ",") {
			state, valid := substrateapi.ParseActorState(part)
			if !valid {
				s.writeError(w, http.StatusBadRequest, "state must be one or more of "+strings.Join(model.ActorStates, ", ")+", separated by commas")
				return
			}
			stateFilter[state] = true
		}
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "updated", "attention", "name":
	default:
		s.writeError(w, http.StatusBadRequest, "sort must be updated, attention or name")
		return
	}
	limit := boundedInt(q.Get("limit"), 100, 1, 1000)
	offset := boundedInt(q.Get("offset"), 0, 0, substrateapi.MaxActors)
	resp := substrateActorsResponse{APITier: tier, Coverage: snap.Coverage.Actors, Sources: []string{"snapshot"}, Rows: []model.ActorRow{}, Limit: limit, Offset: offset}
	tplAtespace, tplName := splitRef(q.Get("template"))
	poolNS, poolName := splitRef(q.Get("pool"))
	worker := q.Get("worker")
	impact := q.Get("impact")
	switch impact {
	case "", model.ImpactUnavailable, model.ImpactDraining, model.ImpactRestarted:
	default:
		s.writeError(w, http.StatusBadRequest, "impact must be unavailable, draining or restarted")
		return
	}
	node, nodeHas := q.Get("node"), q.Get("nodeHas")
	if node != "" && nodeHas != "running" && nodeHas != "localSnapshot" {
		s.writeError(w, http.StatusBadRequest, "node needs nodeHas=running or nodeHas=localSnapshot")
		return
	}
	atespace := q.Get("atespace")
	rawSearch := strings.TrimSpace(q.Get("q"))
	search := strings.ToLower(rawSearch)

	actors := snap.Index().Newest
	if atespace != "" && snapshotMissesAtespace(snap.Coverage.Actors, atespace) {
		direct, cov, tierErr := s.substrateManager().AtespaceActors(r.Context(), atespace)
		if tierErr != nil {
			s.writeSubstrateTier(w, *tierErr)
			return
		}
		resp.Coverage, resp.Sources = cov, []string{"atespace"}
		actors = sortedNewest(direct)
		if rawSearch != "" && cov.Code == substrateapi.CodeCapExceeded && !slices.ContainsFunc(actors, func(a model.Actor) bool { return a.Name == rawSearch }) {
			if exact := s.substrateExactActor(r, model.Ref{Atespace: atespace, Name: rawSearch}); exact != nil {
				actors = sortedNewest(append(slices.Clone(actors), *exact))
				resp.Sources = append(resp.Sources, "actor")
			}
		}
	}
	if !resp.Coverage.Usable() {
		s.writeJSON(w, resp)
		return
	}
	counts := model.NewStateCounts()
	var matched []model.Actor
	for _, a := range actors {
		if atespace != "" && a.Atespace != atespace {
			continue
		}
		if tplName != "" && (a.Template.Name != tplName || tplAtespace != "" && a.Template.Atespace != tplAtespace) {
			continue
		}
		if poolName != "" && (a.Assignment == nil || a.Assignment.Pool != poolName || poolNS != "" && a.Assignment.Namespace != poolNS) {
			continue
		}
		if worker != "" && (a.Assignment == nil || a.Assignment.Pod != worker && a.Assignment.Worker != worker) {
			continue
		}
		if node != "" && !onNode(a, node, nodeHas) {
			continue
		}
		if impact != "" && (a.Assignment == nil || model.WorkerImpactKind(a, snap.Index().Workers[a.Assignment.Worker]) != impact) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(a.Name), search) {
			continue
		}
		counts.Add(a.State)
		if len(stateFilter) > 0 && !stateFilter[a.State] {
			continue
		}
		matched = append(matched, a)
	}
	now := time.Now()
	workers := snap.Index().Workers
	sortActors(matched, sortBy, now, workers)
	total := len(matched)
	resp.Total, resp.Counts = &total, &counts
	rc := model.NewResumeContext(templateUIDs(snap), snap.Workers, snap.Coverage.Workers.State == substrateapi.CoverageFull)
	pods := substrateWorkerPods(snap.Workers)
	for _, a := range matched[min(offset, total):min(offset+limit, total)] {
		row := model.RowOf(a, now, rc)
		if as := a.Assignment; as != nil {
			if wk, ok := workers[as.Worker]; ok {
				row.WorkerImpact = model.WorkerImpactOf(a, wk, pods)
			}
		}
		resp.Rows = append(resp.Rows, row)
	}
	s.writeJSON(w, resp)
}

// onNode is the node risk filter. running: the actor's worker is on node.
// localSnapshot: its newest snapshot is not durably stored and its files are on node,
// so losing the node loses that state.
func onNode(a model.Actor, node, has string) bool {
	if has == "running" {
		return a.Assignment != nil && a.Assignment.Node == node
	}
	newest := model.NewestSnapshot(a)
	return newest != nil && newest.Durable != model.DurableCompleted && newest.Locality == node
}

// sortActors orders actors, which arrive newest change first. attention:
// crashed; then those whose worker is unavailable or restarted under them,
// which may not be served or have lost their sandbox; then stuck in a
// transition past StuckAfter; then the rest, each newest first. name: by
// atespace and name. updated (the default) keeps them.
func sortActors(actors []model.Actor, by string, now time.Time, workers map[string]model.Worker) {
	switch by {
	case "attention":
		rank := func(a model.Actor) int {
			if a.State == model.StateCrashed {
				return 0
			}
			if a.Assignment != nil {
				if wk, ok := workers[a.Assignment.Worker]; ok {
					switch model.WorkerImpactKind(a, wk) {
					case model.ImpactUnavailable, model.ImpactRestarted:
						return 1
					}
				}
			}
			if model.IsStuck(a, now) {
				return 2
			}
			return 3
		}
		slices.SortStableFunc(actors, func(a, b model.Actor) int {
			ra, rb := rank(a), rank(b)
			if ra != rb {
				return ra - rb
			}
			// Stuck actors: the longest waiting first.
			if ra == 2 {
				return a.UpdateTime.Compare(b.UpdateTime)
			}
			return 0
		})
	case "name":
		slices.SortFunc(actors, func(a, b model.Actor) int { return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name) })
	}
}

// snapshotMissesAtespace reports whether the shared snapshot may not hold
// every actor of atespace: it was not read in full, and that atespace was not
// one read completely on its own.
func snapshotMissesAtespace(cov substrateapi.Coverage, atespace string) bool {
	if cov.State == substrateapi.CoverageFull {
		return false
	}
	return cov.Code == substrateapi.CodeCapExceeded || !slices.Contains(cov.ReadAtespaces, atespace)
}

func sortedNewest(actors []model.Actor) []model.Actor {
	out := slices.Clone(actors)
	slices.SortFunc(out, func(a, b model.Actor) int {
		if c := b.UpdateTime.Compare(a.UpdateTime); c != 0 {
			return c
		}
		return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name)
	})
	return out
}

// substrateExactActor reads one actor by name; nil when Substrate has none
// or the read fails.
func (s *Server) substrateExactActor(r *http.Request, ref model.Ref) *model.Actor {
	var actor *ateapi.Actor
	tierErr, code, _ := s.substrateManager().Call(r.Context(), func(ctx context.Context, c ateapi.ControlClient) error {
		var err error
		actor, err = c.GetActor(ctx, &ateapi.GetActorRequest{Actor: &ateapi.ObjectRef{Atespace: ref.Atespace, Name: ref.Name}})
		return err
	})
	if tierErr != nil || code != "" || actor == nil {
		return nil
	}
	a := substrateapi.ActorOf(actor)
	return &a
}

func boundedInt(raw string, def, lo, hi int) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return max(lo, min(n, hi))
}

type substrateActorDetail struct {
	APITier substrateapi.APITier `json:"apiTier"`
	// Actor is read fresh from Substrate, not from the shared snapshot.
	Actor model.Actor    `json:"actor"`
	Row   model.ActorRow `json:"row"`
	// Template is absent when the template list was not read or does not
	// hold it.
	Template *model.TemplateRow `json:"template,omitempty"`
	// Worker is the assigned worker's Substrate state; absent when the
	// actor has no worker or workers were not read.
	Worker *substrateWorkerRow `json:"worker,omitempty"`
	// WorkerImpact is what the worker's state means for this actor:
	// unavailable, draining or restarted; absent when the worker is active
	// and its process has not restarted under the actor.
	WorkerImpact *model.WorkerImpact        `json:"workerImpact,omitempty"`
	Egress       substrateapi.EgressSummary `json:"egress"`
	// Related links: template, pool, worker Pod, node.
	Related []model.Subject `json:"related"`
	// Attention is the needs-attention rows that cover this actor, judged
	// on the fresh read.
	Attention []model.Attention `json:"attention"`
	// Recovery is what a revert would leave; absent when Substrate would
	// refuse to revert the actor.
	Recovery *model.Recovery `json:"recovery,omitempty"`
	// MatchingPools are the pools matching both the template's selector and
	// the actor's own (Actor.WorkerSelector); null when the template or the
	// pool list was not read, or a partial list matches none.
	MatchingPools         []model.PoolRef `json:"matchingPools"`
	MatchingPoolsComplete bool            `json:"matchingPoolsComplete"`
	// Coverage of the reads the template, worker, pool and attention
	// readings were derived from.
	Coverage substrateDetailCoverage `json:"coverage"`
}

type substrateDetailCoverage struct {
	Actors    substrateapi.Coverage    `json:"actors"`
	Templates substrateapi.Coverage    `json:"templates"`
	Workers   substrateapi.Coverage    `json:"workers"`
	Pools     integration.KindCoverage `json:"pools"`
}

func (s *Server) readSubstrateActor(w http.ResponseWriter, r *http.Request, target model.Ref) (*ateapi.Actor, substrateapi.APITier, bool) {
	var actor *ateapi.Actor
	tierErr, code, msg := s.substrateManager().Call(r.Context(), func(ctx context.Context, c ateapi.ControlClient) error {
		var err error
		actor, err = c.GetActor(ctx, &ateapi.GetActorRequest{Actor: &ateapi.ObjectRef{Atespace: target.Atespace, Name: target.Name}})
		return err
	})
	if tierErr != nil {
		s.writeSubstrateTier(w, *tierErr)
		return nil, substrateapi.APITier{}, false
	}
	switch code {
	case "":
	case substrateapi.CodeNotFound:
		s.writeErrorCode(w, http.StatusNotFound, code, "Substrate has no actor "+target.Atespace+"/"+target.Name)
		return nil, substrateapi.APITier{}, false
	case substrateapi.CodeDenied:
		s.writeErrorCode(w, http.StatusForbidden, code, msg)
		return nil, substrateapi.APITier{}, false
	default:
		s.writeErrorCode(w, http.StatusBadGateway, code, msg)
		return nil, substrateapi.APITier{}, false
	}
	return actor, substrateapi.APITier{State: substrateapi.TierConnected}, true
}

// substrateTemplate finds a template in the snapshot or, when the snapshot's
// template list is incomplete, asks Substrate for it. found is false on a
// confirmed miss; code and msg carry a failed fetch.
func (s *Server) substrateTemplate(r *http.Request, snap *substrateapi.Snapshot, ref model.Ref) (t model.Template, found bool, tier *substrateapi.APITier, code, msg string) {
	if snap.Coverage.Templates.Usable() {
		for _, t := range snap.Templates {
			if t.Ref() == ref {
				return t, true, nil, "", ""
			}
		}
		if snap.Coverage.Templates.State == substrateapi.CoverageFull {
			return model.Template{}, false, nil, substrateapi.CodeNotFound, ""
		}
	}
	tier, code, msg = s.substrateManager().Call(r.Context(), func(ctx context.Context, c ateapi.ControlClient) error {
		got, err := c.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: &ateapi.ObjectRef{Atespace: ref.Atespace, Name: ref.Name}})
		if err == nil {
			t = substrateapi.TemplateOf(got)
		}
		return err
	})
	return t, tier == nil && code == "", tier, code, msg
}

func (s *Server) handleSubstrateActor(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	target := model.Ref{Atespace: chi.URLParam(r, "atespace"), Name: chi.URLParam(r, "name")}
	raw, _, ok := s.readSubstrateActor(w, r, target)
	if !ok {
		return
	}
	actor := substrateapi.ActorOf(raw)
	now := time.Now()
	pools, poolsCov := s.substratePools(r)
	resp := substrateActorDetail{APITier: tier, Actor: actor, Attention: []model.Attention{}, Coverage: substrateDetailCoverage{
		Actors: snap.Coverage.Actors, Templates: snap.Coverage.Templates, Workers: snap.Coverage.Workers, Pools: poolsCov,
	}}

	tierErr, code, msg := s.substrateManager().Call(r.Context(), func(ctx context.Context, c ateapi.ControlClient) error {
		policy, err := c.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: &ateapi.ObjectRef{Atespace: target.Atespace, Name: target.Name}, Name: "default"})
		if err == nil {
			resp.Egress = substrateapi.EgressOf(policy)
		} else if status.Code(err) == codes.NotFound {
			resp.Egress = substrateapi.EgressSummary{State: "none"}
			return nil
		}
		return err
	})
	switch {
	case tierErr != nil:
		resp.Egress = substrateapi.EgressSummary{State: "failed", Message: tierErr.Reason}
	case code == substrateapi.CodeDenied:
		resp.Egress = substrateapi.EgressSummary{State: "denied", Message: msg}
	case code != "":
		resp.Egress = substrateapi.EgressSummary{State: "failed", Message: msg}
	}

	in := substrateInputs(snap, pools, poolsCov, now)
	in.NodeReady = s.substrateNodeReady(r)
	poolRows := model.JoinPools(pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead)
	uids := templateUIDs(snap)
	if t, found, _, _, _ := s.substrateTemplate(r, snap, actor.Template); found {
		row := model.TemplateRows([]model.Template{t}, in.Actors, in.ActorsRead, model.KnownPools(pools, in.Workers), in.PoolsUsable || in.WorkersRead, in.PoolsComplete, poolRows)[0]
		resp.Template = &row
		if in.PoolsUsable || in.WorkersRead {
			resp.MatchingPoolsComplete = in.PoolsComplete
			if m := model.ActorMatchingPools(actor, t, model.KnownPools(pools, in.Workers)); len(m) > 0 || in.PoolsComplete {
				resp.MatchingPools = m
			}
		}
		uids = maps.Clone(uids)
		if uids == nil {
			uids = model.TemplateUIDs{}
		}
		uids[t.Ref()] = t.UID
	}
	rc := model.NewResumeContext(uids, in.Workers, in.WorkersComplete)
	resp.Row = model.RowOf(actor, now, rc)
	resp.Recovery = model.RecoveryOf(actor, rc)
	resp.Related = []model.Subject{{Kind: "ActorTemplate", Atespace: actor.Template.Atespace, Name: actor.Template.Name}}
	if as := actor.Assignment; as != nil {
		resp.Related = append(resp.Related,
			model.Subject{Kind: "WorkerPool", Namespace: as.Namespace, Name: as.Pool},
			model.Subject{Kind: "Pod", Namespace: as.Namespace, Name: as.Pod})
		if in.WorkersRead {
			for _, wk := range snap.Workers {
				if wk.Name == as.Worker {
					row := substrateWorkerRowOf(wk, nil, false, in.WorkerPods)
					resp.Worker = &row
					resp.WorkerImpact = model.WorkerImpactOf(actor, wk, in.WorkerPods)
				}
			}
		}
	}
	if node := model.ActorNode(actor); node != "" {
		resp.Related = append(resp.Related, model.Subject{Kind: "Node", Name: node})
	}
	resp.Attention = model.ActorAttention(model.NeedsAttention(in), actor, now)
	s.writeJSON(w, resp)
}

const (
	substrateLogScanLines   = 20_000
	substrateLogMaxLine     = 1 << 20
	substrateLogDefaultTail = 500
	substrateLogMaxTail     = 5000
)

// substrateLogLine is one selected line and the Pod it came from.
type substrateLogLine struct {
	model.LogLine
	Pod string `json:"pod"`
}

// substrateSearchedPod is one worker Pod whose log was searched.
type substrateSearchedPod struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// Outcome is read, denied (the caller may not get pods/log there), gone
	// (the Pod no longer exists) or failed.
	Outcome    string `json:"outcome"`
	Message    string `json:"message,omitempty"`
	Scanned    int    `json:"scanned"`
	WindowFull bool   `json:"windowFull"`
	Matched    int    `json:"matched"`
}

// Origins of an actor's log lines.
const (
	// substrateLogOriginWorker: the worker Pod Substrate assigns it now.
	substrateLogOriginWorker = "worker"
	// substrateLogOriginLastNode: no worker now; worker Pods on the node it
	// was last on, searched for its lines. Substrate does not record which
	// Pod it ran in, so these are candidates.
	substrateLogOriginLastNode = "lastNode"
	substrateLogOriginNone     = "none"
)

type substrateLogsResponse struct {
	Actor model.Ref `json:"actor"`
	State string    `json:"state"`
	// Origin is worker, lastNode or none.
	Origin string `json:"origin"`
	// Worker is the assigned worker whose Pod was read (origin worker).
	Worker *model.WorkerPodRef `json:"worker,omitempty"`
	// Node is the worker's node, or the node searched (origin lastNode).
	Node string `json:"node,omitempty"`
	// ReasonCode and Reason explain an answer with no lines, or where the
	// lines came from: noWorker (no worker and no node recorded), nodeGone,
	// noWorkerPods (no worker Pod on that node now), workerPodGone, denied,
	// noMatch, candidates (lines from Pods on the last node).
	ReasonCode string `json:"reasonCode,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Source     string `json:"source"`
	// Lines are the actor's newest lines, oldest first, at most tailLines,
	// each with its Pod.
	Lines []substrateLogLine `json:"lines"`
	// Searched are the Pods read. Each read covers its Pod's last
	// substrateLogScanLines lines; WindowFull there means older lines of
	// this actor may exist beyond them. MorePods counts Pods on the node
	// left unsearched.
	Searched []substrateSearchedPod `json:"searched"`
	MorePods int                    `json:"morePods,omitempty"`
	// Scanned and WindowFull total the reads, as before.
	Scanned    int  `json:"scanned"`
	WindowFull bool `json:"windowFull"`
	// MemoryKill is the newest worker record among the lines of the worker
	// killing this actor's sandbox over its memory limit; absent when none
	// of the lines read says so. Current says whether it is the cause of the
	// actor's present crash.
	MemoryKill *substrateMemoryKill `json:"memoryKill,omitempty"`
}

type substrateMemoryKill struct {
	Time       time.Time `json:"time,omitzero"`
	Pod        string    `json:"pod"`
	LimitBytes int64     `json:"limitBytes"`
	// UsageBytes is the usage that crossed the limit; absent when the record
	// does not carry it.
	UsageBytes *int64 `json:"usageBytes,omitempty"`
	// Current: the kill is tied to the actor's present crash. It came at
	// most substrateKillCrashWindow before the crash, and no start or restore
	// of the actor among the lines read came between them. Otherwise it is
	// an earlier execution's, a record and not the cause.
	Current bool `json:"current"`
	// NextStep, Command and ThenCommand are the remedy, the actor page's
	// crash step: move the actor to a new template, then revert it. Absent
	// unless Current.
	NextStep    string `json:"nextStep,omitempty"`
	Command     string `json:"command,omitempty"`
	ThenCommand string `json:"thenCommand,omitempty"`
}

// substrateKillCrashWindow bounds how long after a memory-limit kill the
// crash it caused is recorded: the worker reports the crash right after the
// kill, retrying for up to five minutes.
const substrateKillCrashWindow = 10 * time.Minute

// substrateExecutionStarts are the lifecycle events that begin a new
// execution of the actor on a worker.
var substrateExecutionStarts = map[string]bool{"Actor starting": true, "Actor started": true, "Actor restoring": true, "Actor restored": true}

// memoryKillOf finds the newest memory-limit kill of actor among lines, in
// time order, and whether it caused the actor's present crash.
func memoryKillOf(actor model.Actor, lines []substrateLogLine) *substrateMemoryKill {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l.Kind != model.LogKindWorker || l.MemoryLimitBytes == nil {
			continue
		}
		kill := &substrateMemoryKill{Time: l.Time, Pod: l.Pod, LimitBytes: *l.MemoryLimitBytes, UsageBytes: l.MemoryUsageBytes}
		kill.Current = actor.State == model.StateCrashed && actor.Crash != nil && !actor.Crash.Time.IsZero() && !l.Time.IsZero() &&
			!l.Time.After(actor.Crash.Time) && actor.Crash.Time.Sub(l.Time) <= substrateKillCrashWindow &&
			!slices.ContainsFunc(lines[i+1:], func(later substrateLogLine) bool {
				return later.Kind == model.LogKindLifecycle && substrateExecutionStarts[later.Message] && !later.Time.After(actor.Crash.Time)
			})
		if kill.Current {
			kill.NextStep, kill.Command, kill.ThenCommand = model.CrashedMemoryActorStep(actor), model.CrashedMemoryCommand(actor), model.RevertCommand(actor)
		}
		return kill
	}
	return nil
}

// substrateLastNodeMaxPods bounds the worker Pods a last-node search reads.
const substrateLastNodeMaxPods = 6

// substrateWorkerPodsOn lists the worker Pods (Substrate labels them with
// their pool) on node, from Radar's cache; tests replace it.
var substrateWorkerPodsOn = func(node string) []*corev1.Pod {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		return nil
	}
	selector, err := labels.Parse(substrateWorkerPodLabel)
	if err != nil {
		return nil
	}
	pods, err := cache.Pods().List(selector)
	if err != nil {
		return nil
	}
	var out []*corev1.Pod
	for _, p := range pods {
		if p.Spec.NodeName == node {
			out = append(out, p)
		}
	}
	return out
}

// substrateWorkerPodLabel is the label Substrate's controller puts on every
// worker Pod, naming its WorkerPool.
const substrateWorkerPodLabel = "ate.dev/worker-pool"

// substrateNodeGone reports that Radar's Node list, read in full, does not
// hold node. An empty or unread list proves nothing; tests replace it.
var substrateNodeGone = func(s *Server, r *http.Request, node string) bool {
	ready := s.substrateNodeReady(r)
	if len(ready) == 0 {
		return false
	}
	_, ok := ready[node]
	return !ok
}

// substratePodLogAllowed reports whether the caller may read Pod logs in
// namespace; tests replace it.
var substratePodLogAllowed = func(s *Server, r *http.Request, namespace string) bool {
	if allowed := s.getUserNamespaces(r, []string{namespace}); readscope.NoNamespaceAccess(allowed) {
		return false
	}
	return s.canReadSubresource(r, "", "pods", "log", namespace, "get")
}

func (s *Server) handleSubstrateActorLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	r, denied := s.substrateGate(r)
	if denied != nil {
		s.writeSubstrateTier(w, *denied)
		return
	}
	target := model.Ref{Atespace: chi.URLParam(r, "atespace"), Name: chi.URLParam(r, "name")}
	q := r.URL.Query()
	filter, err := model.NewLogFilter(target, q.Get("source"), q.Get("container"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tail := boundedInt(q.Get("tailLines"), substrateLogDefaultTail, 1, substrateLogMaxTail)
	raw, _, ok := s.readSubstrateActor(w, r, target)
	if !ok {
		return
	}
	actor := substrateapi.ActorOf(raw)
	filter.UID = actor.UID
	resp := substrateLogsResponse{Actor: target, State: actor.State, Source: filter.Source, Origin: substrateLogOriginNone,
		Lines: []substrateLogLine{}, Searched: []substrateSearchedPod{}}
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	if as := actor.Assignment; as != nil && as.Pod != "" && as.Namespace != "" {
		resp.Origin, resp.Node = substrateLogOriginWorker, as.Node
		resp.Worker = &model.WorkerPodRef{Worker: as.Worker, Namespace: as.Namespace, Pool: as.Pool, Pod: as.Pod}
		// The log is read directly, not through the pinned cache.
		if failure := outsideNamespacePin(as.Namespace); failure != nil {
			s.writeReadFailure(w, "pods", failure, nil)
			return
		}
		if !substratePodLogAllowed(s, r, as.Namespace) {
			s.writeError(w, http.StatusForbidden, "no access to pod logs in namespace "+as.Namespace+": requires get pods/log")
			return
		}
		read := s.searchSubstratePod(r, client, as.Namespace, as.Pod, filter, tail)
		switch read.pod.Outcome {
		case "gone":
			resp.ReasonCode = "workerPodGone"
			resp.Reason = "The worker Pod " + as.Namespace + "/" + as.Pod + " no longer exists, so its log is gone. Substrate still lists this actor on that worker."
		case "failed":
			s.writeErrorCode(w, http.StatusBadGateway, substrateapi.CodeLogsUnavailable, "cannot read worker Pod "+as.Namespace+"/"+as.Pod+"'s log: "+read.pod.Message)
			return
		}
		resp.Searched = []substrateSearchedPod{read.pod}
		resp.Lines, resp.Scanned, resp.WindowFull = read.lines, read.pod.Scanned, read.pod.WindowFull
		if resp.Lines == nil {
			resp.Lines = []substrateLogLine{}
		}
		resp.MemoryKill = memoryKillOf(actor, resp.Lines)
		s.writeJSON(w, resp)
		return
	}

	node := actor.AssignedNode
	if newest := model.NewestSnapshot(actor); node == "" && newest != nil {
		node = newest.Locality
	}
	if node == "" {
		resp.ReasonCode = "noWorker"
		resp.Reason = "This actor is " + actor.State + " with no worker and no node recorded, so there is no worker Pod log to read. Its lines return when it next runs."
		s.writeJSON(w, resp)
		return
	}
	resp.Origin, resp.Node = substrateLogOriginLastNode, node
	pods := substrateWorkerPodsOn(node)
	slices.SortFunc(pods, func(a, b *corev1.Pod) int { return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name) })
	if len(pods) == 0 && substrateNodeGone(s, r, node) {
		resp.ReasonCode = "nodeGone"
		resp.Reason = "This actor has no worker now, and node " + node + ", where it was last, is gone, so the worker Pod logs there are gone too."
		s.writeJSON(w, resp)
		return
	}
	if len(pods) == 0 {
		resp.ReasonCode = "noWorkerPods"
		resp.Reason = "This actor has no worker now, and no worker Pod runs on node " + node + ", where it was last: the Pod it ran in is gone, and its log with it."
		s.writeJSON(w, resp)
		return
	}
	if len(pods) > substrateLastNodeMaxPods {
		resp.MorePods = len(pods) - substrateLastNodeMaxPods
		pods = pods[:substrateLastNodeMaxPods]
	}
	var all []substrateLogLine
	readable := 0
	for _, p := range pods {
		if !substratePodLogAllowed(s, r, p.Namespace) {
			resp.Searched = append(resp.Searched, substrateSearchedPod{Namespace: p.Namespace, Pod: p.Name, Outcome: "denied", Message: "requires get pods/log in " + p.Namespace})
			continue
		}
		readable++
		read := s.searchSubstratePod(r, client, p.Namespace, p.Name, filter, tail)
		resp.Searched = append(resp.Searched, read.pod)
		resp.Scanned += read.pod.Scanned
		resp.WindowFull = resp.WindowFull || read.pod.WindowFull
		all = append(all, read.lines...)
	}
	slices.SortStableFunc(all, func(a, b substrateLogLine) int { return a.Time.Compare(b.Time) })
	if len(all) > tail {
		all = all[len(all)-tail:]
	}
	resp.Lines = append(resp.Lines, all...)
	resp.MemoryKill = memoryKillOf(actor, resp.Lines)
	switch {
	case readable == 0:
		resp.ReasonCode = "denied"
		resp.Reason = "This actor has no worker now. Radar found worker Pods on node " + node + ", where it was last, but may not read their logs."
	case len(all) == 0:
		resp.ReasonCode = "noMatch"
		resp.Reason = fmt.Sprintf("This actor has no worker now. None of the %s on node %s, where it was last, holds its lines in the part of the log read.", plural(len(resp.Searched), "worker Pod", "worker Pods"), node)
	default:
		resp.ReasonCode = "candidates"
		resp.Reason = "This actor has no worker now. These lines come from worker Pods on node " + node + ", where it was last; Substrate does not record which Pod it ran in, so each line names the Pod it was found in."
	}
	s.writeJSON(w, resp)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

type substratePodRead struct {
	pod   substrateSearchedPod
	lines []substrateLogLine
}

// searchSubstratePod reads one worker Pod's last substrateLogScanLines lines
// and keeps the newest tail the filter selects.
func (s *Server) searchSubstratePod(r *http.Request, client kubernetes.Interface, namespace, pod string, filter model.LogFilter, tail int) substratePodRead {
	out := substratePodRead{pod: substrateSearchedPod{Namespace: namespace, Pod: pod, Outcome: "read"}}
	scanLines := int64(substrateLogScanLines)
	// No container, as kubectl ate reads it: the Pod's default container.
	stream, err := k8score.GetContainerLogs(r.Context(), client, namespace, pod, "", k8score.LogOptions{TailLines: &scanLines, Timestamps: true})
	if err != nil {
		if apierrors.IsNotFound(err) {
			out.pod.Outcome = "gone"
			return out
		}
		log.Printf("[substrate] Failed to read worker Pod log %s/%s: %v", namespace, pod, err)
		out.pod.Outcome, out.pod.Message = "failed", err.Error()
		return out
	}
	defer stream.Close()
	lines, scanned, err := filterSubstrateLog(stream, filter, tail)
	if err != nil {
		out.pod.Outcome, out.pod.Message = "failed", err.Error()
		return out
	}
	out.pod.Scanned, out.pod.WindowFull, out.pod.Matched = scanned, scanned >= substrateLogScanLines, len(lines)
	for _, l := range lines {
		out.lines = append(out.lines, substrateLogLine{LogLine: l, Pod: pod})
	}
	return out
}

// filterSubstrateLog keeps the newest tail lines the filter selects. A line
// longer than the buffer is skipped whole, so one oversized record cannot end
// the scan before the newer lines behind it.
func filterSubstrateLog(r io.Reader, filter model.LogFilter, tail int) ([]model.LogLine, int, error) {
	reader := bufio.NewReaderSize(r, substrateLogMaxLine)
	ring := make([]model.LogLine, 0, tail)
	scanned := 0
	skipping := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			skipping = true
			continue
		}
		if len(chunk) > 0 && !skipping {
			scanned++
			stamp, line, _ := model.SplitKubeletTimestamp(strings.TrimRight(string(chunk), "\r\n"))
			if got, ok := model.FilterLine(line, filter, stamp); ok {
				if len(ring) == tail {
					ring = append(ring[1:], got)
				} else {
					ring = append(ring, got)
				}
			}
		} else if len(chunk) > 0 {
			scanned++
		}
		skipping = false
		if err == io.EOF {
			return ring, scanned, nil
		}
		if err != nil {
			return nil, scanned, err
		}
	}
}

// substrateWorkerRow is one worker with Radar's readings beside Substrate's.
type substrateWorkerRow struct {
	model.Worker
	// HasFreeSlot: the worker has an actor slot left (model.HasFreeSlot);
	// not a claim that any given actor fits.
	HasFreeSlot bool `json:"hasFreeSlot"`
	// HeldForSeconds is how long the state has held at least: since
	// Substrate's last update to the worker. Absent when Substrate reported
	// no update time.
	HeldForSeconds *int64 `json:"heldForSeconds,omitempty"`
	// PodReady is the worker Pod's Ready condition in Radar's cache; absent
	// when the Pod is not cached.
	PodReady *bool `json:"podReady,omitempty"`
	// PodTerminating: the cached Pod is being deleted. A Deployment's ready
	// count leaves a terminating Pod out even while its Ready condition holds.
	PodTerminating bool `json:"podTerminating,omitempty"`
	// WorkerState is what its state means for its actors (WorkerReadingOf);
	// absent for an active worker whose registration Substrate has observed.
	WorkerState *model.WorkerReading `json:"workerState,omitempty"`
	// Actors are the actors assigned to it: absent when actors were not
	// read, [] when it has none.
	Actors []substrateAssignedActor `json:"actors,omitzero"`
}

type substrateAssignedActor struct {
	Atespace string    `json:"atespace"`
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Template model.Ref `json:"template"`
	// UpdateTime is the actor's last change, which tells a stuck transition
	// from one still within its budget.
	UpdateTime time.Time `json:"updateTime,omitzero"`
	// WorkerImpact is what this worker's state means for the actor; absent
	// when it means nothing.
	WorkerImpact *model.WorkerImpact `json:"workerImpact,omitempty"`
}

// substrateWorkerRowOf reads one worker; assigned is its actors from the
// snapshot's index, used only when actorsRead.
func substrateWorkerRowOf(wk model.Worker, assigned []model.Actor, actorsRead bool, pods model.WorkerPods) substrateWorkerRow {
	row := substrateWorkerRow{Worker: wk, HasFreeSlot: model.HasFreeSlot(wk)}
	if !wk.UpdateTime.IsZero() {
		held := int64(model.HeldFor(wk.UpdateTime, time.Now()).Seconds())
		row.HeldForSeconds = &held
	}
	if pod, ok := pods.Of(wk); ok {
		row.PodReady = &pod.Ready
		row.PodTerminating = !pod.Deadline.IsZero()
	}
	row.WorkerState = model.WorkerReadingOf(wk, assigned, actorsRead, pods)
	if actorsRead {
		row.Actors = make([]substrateAssignedActor, 0, len(assigned))
		for _, a := range assigned {
			row.Actors = append(row.Actors, substrateAssignedActor{Atespace: a.Atespace, Name: a.Name, State: a.State, Template: a.Template, UpdateTime: a.UpdateTime, WorkerImpact: model.WorkerImpactOf(a, wk, pods)})
		}
	}
	return row
}

func podIsReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

type substrateWorkersResponse struct {
	APITier substrateapi.APITier `json:"apiTier"`
	// Coverage of workers and of actors (their assignments).
	Coverage struct {
		Workers substrateapi.Coverage `json:"workers"`
		Actors  substrateapi.Coverage `json:"actors"`
	} `json:"coverage"`
	// Counts are absent when workers could not be read.
	Counts  *model.WorkerCounts  `json:"counts,omitempty"`
	Workers []substrateWorkerRow `json:"workers"`
}

func (s *Server) handleSubstrateWorkers(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	resp := substrateWorkersResponse{APITier: tier, Workers: []substrateWorkerRow{}}
	resp.Coverage.Workers, resp.Coverage.Actors = snap.Coverage.Workers, snap.Coverage.Actors
	if !snap.Coverage.Workers.Usable() {
		s.writeJSON(w, resp)
		return
	}
	poolNS, poolName := splitRef(r.URL.Query().Get("pool"))
	counts := model.WorkerCounts{}
	resp.Counts = &counts
	pods := substrateWorkerPods(snap.Workers)
	for _, wk := range snap.Workers {
		if poolName != "" && (wk.Pool != poolName || poolNS != "" && wk.Namespace != poolNS) {
			continue
		}
		resp.Counts.Total++
		switch wk.State {
		case model.WorkerActive:
			resp.Counts.Active++
		case model.WorkerDraining:
			resp.Counts.Draining++
		case model.WorkerUnavailable:
			resp.Counts.Unavailable++
		}
		resp.Workers = append(resp.Workers, substrateWorkerRowOf(wk, snap.Index().ByWorker[wk.Name], snap.Coverage.Actors.Usable(), pods))
	}
	slices.SortFunc(resp.Workers, func(a, b substrateWorkerRow) int {
		return strings.Compare(a.Namespace+"/"+a.Pool+"/"+a.Pod, b.Namespace+"/"+b.Pool+"/"+b.Pod)
	})
	s.writeJSON(w, resp)
}

type substrateTemplatesResponse struct {
	APITier  substrateapi.APITier `json:"apiTier"`
	Coverage struct {
		Templates substrateapi.Coverage    `json:"templates"`
		Actors    substrateapi.Coverage    `json:"actors"`
		Pools     integration.KindCoverage `json:"pools"`
	} `json:"coverage"`
	Templates []model.TemplateRow `json:"templates"`
}

func (s *Server) substrateTemplateRows(r *http.Request, snap *substrateapi.Snapshot, templates []model.Template) ([]model.TemplateRow, integration.KindCoverage) {
	pools, poolsCov := s.substratePools(r)
	in := substrateInputs(snap, pools, poolsCov, time.Now())
	poolRows := model.JoinPools(pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead)
	return model.TemplateRows(templates, in.Actors, in.ActorsRead, model.KnownPools(pools, in.Workers), in.PoolsUsable || in.WorkersRead, in.PoolsComplete, poolRows), poolsCov
}

func (s *Server) handleSubstrateTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	resp := substrateTemplatesResponse{APITier: tier, Templates: []model.TemplateRow{}}
	resp.Coverage.Templates, resp.Coverage.Actors = snap.Coverage.Templates, snap.Coverage.Actors
	if snap.Coverage.Templates.Usable() {
		resp.Templates, resp.Coverage.Pools = s.substrateTemplateRows(r, snap, snap.Templates)
	} else {
		_, resp.Coverage.Pools = s.substratePools(r)
	}
	s.writeJSON(w, resp)
}

type substrateTemplateResponse struct {
	APITier  substrateapi.APITier `json:"apiTier"`
	Template model.TemplateRow    `json:"template"`
	// Spec is the template's definition, read fresh and redacted; absent
	// when that read failed, and SpecUnavailable says why.
	Spec            *substrateapi.TemplateSpec `json:"spec,omitempty"`
	SpecUnavailable string                     `json:"specUnavailable,omitempty"`
	// Coverage of the reads its actor counts and eligible pools come from.
	Coverage struct {
		Templates substrateapi.Coverage    `json:"templates"`
		Actors    substrateapi.Coverage    `json:"actors"`
		Pools     integration.KindCoverage `json:"pools"`
	} `json:"coverage"`
}

func (s *Server) handleSubstrateTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	target := model.Ref{Atespace: chi.URLParam(r, "atespace"), Name: chi.URLParam(r, "name")}
	t, found, tierErr, code, msg := s.substrateTemplate(r, snap, target)
	switch {
	case tierErr != nil:
		s.writeSubstrateTier(w, *tierErr)
		return
	case code == substrateapi.CodeNotFound:
		s.writeErrorCode(w, http.StatusNotFound, code, "Substrate has no actor template "+target.Atespace+"/"+target.Name)
		return
	case code == substrateapi.CodeDenied:
		s.writeErrorCode(w, http.StatusForbidden, code, msg)
		return
	case !found:
		s.writeErrorCode(w, http.StatusBadGateway, code, msg)
		return
	}
	resp := substrateTemplateResponse{APITier: tier}
	specTier, specCode, specMsg := s.substrateManager().Call(r.Context(), func(ctx context.Context, c ateapi.ControlClient) error {
		raw, err := c.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: &ateapi.ObjectRef{Atespace: target.Atespace, Name: target.Name}})
		if err == nil {
			spec := substrateapi.TemplateSpecOf(raw)
			resp.Spec = &spec
		}
		return err
	})
	switch {
	case specTier != nil:
		resp.SpecUnavailable = specTier.Reason
	case specCode != "":
		resp.SpecUnavailable = specMsg
	}
	var rows []model.TemplateRow
	rows, resp.Coverage.Pools = s.substrateTemplateRows(r, snap, []model.Template{t})
	resp.Template = rows[0]
	resp.Coverage.Templates, resp.Coverage.Actors = snap.Coverage.Templates, snap.Coverage.Actors
	s.writeJSON(w, resp)
}

type substratePoolResponse struct {
	APITier substrateapi.APITier `json:"apiTier"`
	Pool    model.PoolRow        `json:"pool"`
	// Workers are the pool's workers with the actors in their slots.
	Workers []substrateWorkerRow `json:"workers"`
	// Templates are those whose selector matches this pool: absent when
	// templates were not read, [] when none matches.
	Templates []model.Ref `json:"templates,omitzero"`
	Coverage  struct {
		Workers   substrateapi.Coverage    `json:"workers"`
		Actors    substrateapi.Coverage    `json:"actors"`
		Templates substrateapi.Coverage    `json:"templates"`
		Pools     integration.KindCoverage `json:"pools"`
	} `json:"coverage"`
}

func (s *Server) handleSubstratePool(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	ref := model.PoolRef{Namespace: chi.URLParam(r, "namespace"), Name: chi.URLParam(r, "name")}
	// Before any Kubernetes read: the direct get below would otherwise reach
	// past the pinned cache.
	if failure := outsideNamespacePin(ref.Namespace); failure != nil {
		s.writeReadFailure(w, "workerpools", failure, nil)
		return
	}
	if !s.canRead(r, "ate.dev", "workerpools", ref.Namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to WorkerPools in namespace "+ref.Namespace)
		return
	}
	pools, poolsCov := s.substratePools(r)
	in := substrateInputs(snap, pools, poolsCov, time.Now())
	var row *model.PoolRow
	for _, p := range model.JoinPools(pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead) {
		if p.Ref() == ref {
			row = &p
		}
	}
	if row == nil {
		// The list may not hold it (a namespace not collected, a watch still
		// syncing, list denied while get is allowed): ask for it directly
		// before saying it does not exist.
		got, err := s.substrateWorkerPool(r, ref)
		switch {
		case got != nil:
			found := []model.Pool{poolOf(got, time.Now())}
			s.withPoolPods(r, found)
			pools = append(pools, found...)
			for _, p := range model.JoinPools(pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead) {
				if p.Ref() == ref {
					row = &p
				}
			}
		case err != nil:
			s.writeErrorCode(w, http.StatusServiceUnavailable, substrateapi.CodeIncomplete, "Radar could not read WorkerPool "+ref.Key()+": "+err.Error())
			return
		case snap.Coverage.Workers.State != substrateapi.CoverageFull:
			s.writeErrorCode(w, http.StatusServiceUnavailable, substrateapi.CodeIncomplete, "Kubernetes has no WorkerPool "+ref.Key()+", and Substrate's workers were not all read, so Radar cannot say whether a worker names it")
			return
		default:
			s.writeErrorCode(w, http.StatusNotFound, substrateapi.CodeNotFound, "no WorkerPool "+ref.Key()+" in Kubernetes or Substrate's workers")
			return
		}
	}
	resp := substratePoolResponse{APITier: tier, Pool: *row, Workers: []substrateWorkerRow{}}
	resp.Coverage.Pools = poolsCov
	resp.Coverage.Workers, resp.Coverage.Actors, resp.Coverage.Templates = snap.Coverage.Workers, snap.Coverage.Actors, snap.Coverage.Templates
	if in.WorkersRead {
		for _, wk := range snap.Workers {
			if wk.PoolRef() == ref {
				resp.Workers = append(resp.Workers, substrateWorkerRowOf(wk, snap.Index().ByWorker[wk.Name], in.ActorsRead, in.WorkerPods))
			}
		}
		slices.SortFunc(resp.Workers, func(a, b substrateWorkerRow) int { return strings.Compare(a.Pod, b.Pod) })
	}
	if in.TemplatesRead {
		resp.Templates = []model.Ref{}
		known := model.KnownPools(pools, in.Workers)
		for _, t := range snap.Templates {
			if slices.Contains(model.MatchingPools(t, known), ref) {
				resp.Templates = append(resp.Templates, t.Ref())
			}
		}
	}
	s.writeJSON(w, resp)
}

// destinationChanges names only what moved between two settings' endpoint,
// CA file and server name, with the default an empty value stands for.
func destinationChanges(from, to substrateapi.Settings) string {
	var parts []string
	add := func(label, a, b, empty string) {
		if a == b {
			return
		}
		or := func(v string) string {
			if v == "" {
				return empty
			}
			return v
		}
		parts = append(parts, fmt.Sprintf("%s %s → %s", label, or(a), or(b)))
	}
	add("endpoint", from.Endpoint, to.Endpoint, "port-forward to ate-system/api")
	add("CA file", from.CAFile, to.CAFile, "the cluster's trust bundle")
	add("server name", from.ServerName, to.ServerName, substrateapi.APIServerName)
	return strings.Join(parts, ", ")
}
