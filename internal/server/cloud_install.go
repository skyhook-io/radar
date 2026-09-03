package server

// In-product Cloud Connect: the modal's "Connect this cluster" button drives
// the same device-flow install the `radar cloud install` CLI performs, through
// a server-side single-flight flow manager. The driver lane runs where the UI
// user already wields the server's kubeconfig through existing endpoints
// (/api/resources/apply, pods/exec): a local deployment, auth disabled, no
// existing Cloud tunnel. A non-loopback listener does not disable it — those
// endpoints are ungated there too — but the plan card names the exposure, so
// binding a shared cluster into someone's personal org takes a decision.
//
// Every other configuration routes to the Hub wizard instead. In-cluster it is
// not a policy choice: the ServiceAccount cannot self-grant the RBAC, and a
// successful helm upgrade restarts the very pod serving the flow, so an
// in-product install there cannot report its own outcome.
//
// Safety properties preserved from the CLI driver:
//   - No Hub request or token mint before the exact-manifest preflight passes.
//   - Per-flow clients are captured once at prepare time (typed + Helm bound to
//     a copied rest.Config), so an in-app kubeconfig context switch while the
//     flow waits for approval cannot retarget the Kubernetes or Helm writes.
//   - The rhc_ cluster token never appears in any API response or log: it
//     travels goroutine-local from the approval poll into the token Secret.
//   - Post-approval failures surface the same typed recovery guidance the CLI
//     prints (shared structs in internal/cloudinstall/recovery.go).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/contextname"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
)

// CloudConnectConfig is the Cloud-connect wiring resolved at startup.
type CloudConnectConfig struct {
	HubAPIURL             string // Hub API origin (default https://api.radarhq.io)
	HubAppURL             string // Hub frontend origin for wizard links (default https://app.radarhq.io)
	Kubeconfig            string // explicit kubeconfig override, for recovery command rendering
	CloudTunnelConfigured bool   // --cloud-url was set on this process
}

const (
	cloudFlowPreparing        = "preparing"
	cloudFlowReady            = "ready"
	cloudFlowStarting         = "starting"
	cloudFlowAwaitingApproval = "awaiting_approval"
	cloudFlowProvisioning     = "provisioning"
	cloudFlowWaitingTunnel    = "waiting_tunnel"
	cloudFlowConnected        = "connected"
	cloudFlowFailed           = "failed"
)

const (
	cloudFailConnect           = "connect_failed"
	cloudFailRejected          = "rejected"
	cloudFailExpired           = "expired"
	cloudFailPickupExpired     = "pickup_expired"
	cloudFailApprovalUnknown   = "approval_unknown"
	cloudFailCanceled          = "canceled"
	cloudFailCanceledApproved  = "canceled_after_approval"
	cloudFailProvision         = "provision_failed"
	cloudFailTunnelUnconfirmed = "tunnel_unconfirmed"
)

const (
	cloudInstallRequestTimeout  = 30 * time.Second
	cloudTunnelConfirmationWait = 5 * time.Minute
	// cloudInstallHandlerTimeout bounds prepare/start, which are registered
	// outside the router's blanket 60s timeout because chart download plus
	// preflight can legitimately exceed it. Generous enough for a slow link,
	// finite so a wedged download cannot pin the single-flight slot forever.
	cloudInstallHandlerTimeout = 5 * time.Minute
)

// preparedInstall is the slice of *cloudinstall.PreparedProvision the flow
// consumes — an interface so state-machine tests can fake preparation without
// downloading a chart.
type preparedInstall interface {
	Mode() cloudinstall.ProvisionMode
	Namespace() string
	ReleaseName() string
	ChartVersion() string
	AppVersion() string
	CurrentChartVersion() string
	CurrentRevision() int
	CurrentValues() helm.CloudUpgradeValuesSummary
	CurrentManifest() string
	TargetManifest() string
	Deployment() helm.DeploymentRef
}

type cloudConnectClient interface {
	Create(ctx context.Context, meta cloud.ConnectMetadata) (*cloud.CreateResponse, error)
	PollUntilApproved(ctx context.Context, cr *cloud.CreateResponse) (*cloud.PollResponse, error)
	WaitUntilConsumed(ctx context.Context, cr *cloud.CreateResponse, maxWait time.Duration) error
}

type cloudInstallClients struct {
	Kubernetes kubernetes.Interface
	Dynamic    dynamic.Interface
	Discovery  discovery.DiscoveryInterface
	Helm       *helm.Client
}

// cloudInstallBackend is the seam between the flow manager's state machine and
// the real discovery/prepare/preflight/provision/Hub machinery.
type cloudInstallBackend struct {
	captureClients   func() (cloudInstallClients, string, error)
	contextSource    func(name string) (sourceFile, inFileName string, ok bool)
	inspectPlan      func(ctx context.Context, c cloudInstallClients, namespace, release string) (cloudinstall.InstallPlan, error)
	prepare          func(ctx context.Context, c cloudInstallClients, cfg cloudinstall.PrepareConfig) (preparedInstall, error)
	preflight        func(ctx context.Context, c cloudInstallClients, prepared preparedInstall) (cloudinstall.PreflightResult, error)
	provision        func(ctx context.Context, c cloudInstallClients, prepared preparedInstall, cfg cloudinstall.ProvisionConfig) error
	newConnectClient func() cloudConnectClient
	connectMetadata  func(ctx context.Context, c cloudInstallClients, clusterName string) cloud.ConnectMetadata
}

type cloudInstallConnected struct {
	ClusterID  string `json:"clusterId"`
	ClusterURL string `json:"clusterUrl"`
	// TrackCmd lets the operator watch the rollout locally; it already names
	// the Deployment, so the ref itself is not repeated on the wire.
	TrackCmd string                         `json:"trackCommand"`
	Rollback *cloudinstall.RecoveryGuidance `json:"rollback,omitempty"`
}

type cloudInstallFailure struct {
	Kind     string                         `json:"kind"`
	Message  string                         `json:"message"`
	Guidance *cloudinstall.RecoveryGuidance `json:"guidance,omitempty"`
	// RetrySafe tells the UI whether starting a fresh flow is harmless (no Hub
	// cluster was created) or needs the guidance followed first.
	RetrySafe bool `json:"retrySafe"`
}

type cloudInstallPlanSummary struct {
	Mode                     string   `json:"mode"`
	ContextName              string   `json:"contextName"`
	Namespace                string   `json:"namespace"`
	Release                  string   `json:"release"`
	DefaultClusterName       string   `json:"defaultClusterName"`
	TargetChartVersion       string   `json:"targetChartVersion"`
	TargetAppVersion         string   `json:"targetAppVersion"`
	CurrentChartVersion      string   `json:"currentChartVersion,omitempty"`
	CurrentRevision          int      `json:"currentRevision,omitempty"`
	CurrentImageTag          string   `json:"currentImageTag,omitempty"`
	PreservedImageRepository string   `json:"preservedImageRepository,omitempty"`
	Uncertainty              string   `json:"uncertainty,omitempty"`
	Advisories               []string `json:"advisories,omitempty"`
	// SharedListener is true when Radar answers beyond loopback: anyone who can
	// open this page can approve the connection into their own Radar org.
	SharedListener bool `json:"sharedListener,omitempty"`
}

// cloudInstallBlocked explains why the driver lane cannot serve this cluster.
// It is returned by prepare without retaining a flow.
type cloudInstallBlocked struct {
	Reason   string   `json:"reason"` // gitops | preflight | unsupported
	Message  string   `json:"message"`
	Blocking []string `json:"blocking,omitempty"`
}

type cloudInstallFlow struct {
	id            string
	state         string
	contextName   string
	commandTarget cloudinstall.CommandTarget
	clients       cloudInstallClients
	plan          cloudinstall.InstallPlan
	prepared      preparedInstall
	summary       cloudInstallPlanSummary

	clusterName string
	connectURL  string
	cancel      context.CancelFunc
	// canceling is set under the manager lock the moment a cancel is accepted,
	// so the run goroutine cannot claim the provisioning state afterwards.
	canceling bool

	connected *cloudInstallConnected
	failure   *cloudInstallFailure
}

func (f *cloudInstallFlow) terminal() bool {
	return f.state == cloudFlowConnected || f.state == cloudFlowFailed
}

type cloudInstallManager struct {
	// mu guards flow; every long-running step runs outside the lock and
	// re-checks flow identity before writing its result back.
	mu      sync.Mutex
	flow    *cloudInstallFlow
	backend cloudInstallBackend
	cfg     CloudConnectConfig
	// sharedListener reports whether Radar answers beyond loopback, so the
	// plan card can name that exposure before anyone approves.
	sharedListener func() bool
}

func newCloudInstallManager(cfg CloudConnectConfig) *cloudInstallManager {
	m := &cloudInstallManager{cfg: cfg}
	m.backend = cloudInstallBackend{
		contextSource: k8s.GetContextSource,
		captureClients: func() (cloudInstallClients, string, error) {
			base, contextName := k8s.GetConfigSnapshot()
			if base == nil {
				return cloudInstallClients{}, "", errors.New("no Kubernetes connection is available")
			}
			// Copy + bound every request so a dead apiserver connection cannot
			// hang the flow, and later context switches cannot retarget it.
			cfgCopy := rest.CopyConfig(base)
			if cfgCopy.Timeout <= 0 || cfgCopy.Timeout > cloudInstallRequestTimeout {
				cfgCopy.Timeout = cloudInstallRequestTimeout
			}
			kc, err := kubernetes.NewForConfig(cfgCopy)
			if err != nil {
				return cloudInstallClients{}, "", fmt.Errorf("kube client: %w", err)
			}
			dc, err := dynamic.NewForConfig(cfgCopy)
			if err != nil {
				return cloudInstallClients{}, "", fmt.Errorf("dynamic kube client: %w", err)
			}
			return cloudInstallClients{
				Kubernetes: kc,
				Dynamic:    dc,
				Discovery:  kc.Discovery(),
				Helm:       helm.NewStandaloneClient(cfgCopy),
			}, contextName, nil
		},
		inspectPlan: func(ctx context.Context, c cloudInstallClients, namespace, release string) (cloudinstall.InstallPlan, error) {
			return cloudinstall.InspectInstallPlan(ctx, c.Kubernetes, c.Dynamic, c.Helm, namespace, release, false)
		},
		prepare: func(ctx context.Context, c cloudInstallClients, cfg cloudinstall.PrepareConfig) (preparedInstall, error) {
			return cloudinstall.Prepare(ctx, c.Helm, c.Kubernetes, cfg)
		},
		preflight: func(ctx context.Context, c cloudInstallClients, prepared preparedInstall) (cloudinstall.PreflightResult, error) {
			if prepared.Mode() == cloudinstall.ProvisionAdopt {
				return cloudinstall.AdoptionPreflight(ctx, c.Kubernetes, c.Dynamic, c.Discovery, cloudinstall.AdoptionPreflightOptions{
					Namespace:       prepared.Namespace(),
					ReleaseName:     prepared.ReleaseName(),
					CurrentRevision: prepared.CurrentRevision(),
					CurrentManifest: prepared.CurrentManifest(),
					TargetManifest:  prepared.TargetManifest(),
				})
			}
			return cloudinstall.FreshInstallPreflight(ctx, c.Kubernetes, c.Dynamic, c.Discovery, cloudinstall.FreshInstallPreflightOptions{
				Namespace:      prepared.Namespace(),
				ReleaseName:    prepared.ReleaseName(),
				TargetManifest: prepared.TargetManifest(),
			})
		},
		provision: func(ctx context.Context, c cloudInstallClients, prepared preparedInstall, cfg cloudinstall.ProvisionConfig) error {
			concrete, ok := prepared.(*cloudinstall.PreparedProvision)
			if !ok {
				return fmt.Errorf("unexpected prepared install type %T", prepared)
			}
			return cloudinstall.ProvisionPrepared(ctx, c.Kubernetes, concrete, cfg)
		},
		newConnectClient: func() cloudConnectClient {
			return cloud.NewConnectClient(cfg.HubAPIURL)
		},
		connectMetadata: func(ctx context.Context, c cloudInstallClients, clusterName string) cloud.ConnectMetadata {
			meta := cloud.ConnectMetadata{
				DeploymentMode: "in-cluster",
				ClusterName:    clusterName,
				RadarVersion:   cloud.Version,
				Scope:          "cluster",
			}
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if v, err := c.Discovery.ServerVersion(); err == nil && v != nil {
				meta.K8sVersion = v.GitVersion
			}
			if nodes, err := c.Kubernetes.CoreV1().Nodes().List(probeCtx, metav1.ListOptions{Limit: 500}); err == nil {
				n := len(nodes.Items)
				meta.NodeCount = &n
			}
			return meta
		},
	}
	return m
}

func newCloudFlowID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("flow-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (m *cloudInstallManager) commandTarget(contextName string) cloudinstall.CommandTarget {
	if m.backend.contextSource != nil {
		if sourceFile, inFileName, ok := m.backend.contextSource(contextName); ok {
			return cloudinstall.CommandTarget{Context: inFileName, Kubeconfig: sourceFile}
		}
	}
	return cloudinstall.CommandTarget{Context: contextName, Kubeconfig: m.cfg.Kubeconfig}
}

// prepare runs discovery → classification → chart prepare → preflight, with no
// Hub contact. On success the flow is retained in "ready"; a blocked result
// retains nothing.
func (m *cloudInstallManager) prepare(ctx context.Context) (*cloudInstallFlow, *cloudInstallBlocked, error) {
	flow := &cloudInstallFlow{id: newCloudFlowID(), state: cloudFlowPreparing}
	m.mu.Lock()
	if m.flow != nil && !m.flow.terminal() {
		active := m.flow
		m.mu.Unlock()
		return active, nil, errFlowActive
	}
	m.flow = flow
	m.mu.Unlock()

	blocked, err := m.runPrepare(ctx, flow)
	if blocked != nil || err != nil {
		m.clearFlow(flow)
		return nil, blocked, err
	}
	return flow, nil, nil
}

var errFlowActive = errors.New("a Cloud connection flow is already in progress")

func (m *cloudInstallManager) runPrepare(ctx context.Context, flow *cloudInstallFlow) (*cloudInstallBlocked, error) {
	clients, contextName, err := m.backend.captureClients()
	if err != nil {
		return nil, err
	}
	flow.clients = clients
	flow.contextName = contextName
	flow.commandTarget = m.commandTarget(contextName)

	plan, err := m.backend.inspectPlan(ctx, clients, cloudinstall.DefaultInstallNamespace, cloudinstall.DefaultReleaseName)
	if err != nil {
		var multiple *cloudinstall.MultipleTargetsError
		if errors.As(err, &multiple) {
			return &cloudInstallBlocked{
				Reason:  "unsupported",
				Message: "Multiple Radar installations were found in this cluster. Use `radar cloud install --namespace <ns> --release <name>` in a terminal to pick one explicitly.",
			}, nil
		}
		return &cloudInstallBlocked{Reason: "unsupported", Message: err.Error()}, nil
	}
	flow.plan = plan

	if plan.Mode == cloudinstall.InstallModeGitOps {
		target := ""
		if plan.Target != nil {
			target = fmt.Sprintf(" (%s/%s)", plan.Target.Namespace, plan.Target.DeploymentName)
		}
		return &cloudInstallBlocked{
			Reason: "gitops",
			Message: fmt.Sprintf(
				"This Radar install%s is managed by a GitOps controller, so connecting it means changing its source of truth — not applying a live mutation. Run `radar cloud install` in a terminal: it generates the exact values and token-Secret handoff for your Git workflow.",
				target,
			),
		}, nil
	}

	prepared, err := m.backend.prepare(ctx, clients, cloudinstall.PrepareConfig{
		Namespace:     plan.Namespace,
		ReleaseName:   plan.Release,
		AdoptExisting: plan.Mode == cloudinstall.InstallModeAdopt,
	})
	if err != nil {
		return &cloudInstallBlocked{Reason: "unsupported", Message: err.Error()}, nil
	}
	flow.prepared = prepared

	pf, err := m.backend.preflight(ctx, clients, prepared)
	if err != nil {
		return nil, fmt.Errorf("permission preflight failed: %w", err)
	}
	if !pf.OK() {
		return &cloudInstallBlocked{
			Reason:   "preflight",
			Message:  "Your current Kubernetes identity cannot perform the exact planned Radar operation. Ask a platform operator to connect this cluster instead.",
			Blocking: pf.Blocking,
		}, nil
	}

	summary := cloudInstallPlanSummary{
		Mode:               string(plan.Mode),
		ContextName:        contextName,
		Namespace:          prepared.Namespace(),
		Release:            prepared.ReleaseName(),
		DefaultClusterName: defaultCloudClusterName(contextName),
		TargetChartVersion: prepared.ChartVersion(),
		TargetAppVersion:   prepared.AppVersion(),
		Advisories:         pf.Advisory,
		SharedListener:     m.sharedListener != nil && m.sharedListener(),
	}
	if plan.Mode == cloudinstall.InstallModeAdopt {
		current := prepared.CurrentValues()
		summary.CurrentChartVersion = prepared.CurrentChartVersion()
		summary.CurrentRevision = prepared.CurrentRevision()
		summary.CurrentImageTag = current.ImageTag
		summary.PreservedImageRepository = current.ImageRepository
	}
	if plan.ClusterWideScanError != nil {
		summary.Uncertainty = fmt.Sprintf(
			"Radar could not inspect all visible namespaces for an existing installation: %v. Another installation outside namespace %q could not be ruled out.",
			plan.ClusterWideScanError, prepared.Namespace(),
		)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != flow {
		return nil, errors.New("the connection flow was replaced while preparing")
	}
	flow.summary = summary
	flow.state = cloudFlowReady
	return nil, nil
}

func defaultCloudClusterName(contextName string) string {
	if name := strings.TrimSpace(contextname.ShortName(contextName)); name != "" {
		return name
	}
	return "my-cluster"
}

type cloudInstallStartRequest struct {
	FlowID                         string `json:"flowId"`
	ClusterName                    string `json:"clusterName"`
	AcceptAdoption                 bool   `json:"acceptAdoption"`
	AcknowledgeIncompleteDiscovery bool   `json:"acknowledgeIncompleteDiscovery"`
	AcknowledgeSharedListener      bool   `json:"acknowledgeSharedListener"`
}

// start creates the Hub connect request and launches the manager-owned
// approval → provision → tunnel goroutine.
func (m *cloudInstallManager) start(req cloudInstallStartRequest) (*cloudInstallFlow, error) {
	m.mu.Lock()
	flow := m.flow
	if flow == nil || flow.id != req.FlowID {
		m.mu.Unlock()
		return nil, errFlowStale
	}
	if flow.state != cloudFlowReady {
		m.mu.Unlock()
		return nil, fmt.Errorf("the connection flow is %s, not ready to start", flow.state)
	}
	if flow.plan.Mode == cloudinstall.InstallModeAdopt && !req.AcceptAdoption {
		m.mu.Unlock()
		return nil, errors.New("adopting the existing installation requires explicit consent")
	}
	if flow.summary.Uncertainty != "" && !req.AcknowledgeIncompleteDiscovery {
		m.mu.Unlock()
		return nil, errors.New("incomplete installation discovery requires explicit acknowledgement")
	}
	// On a shared listener the person approving may not be the operator, and
	// the binding this creates outlives the request — make it a decision
	// rather than a warning someone scrolls past.
	if flow.summary.SharedListener && !req.AcknowledgeSharedListener {
		m.mu.Unlock()
		return nil, errors.New("connecting from a non-loopback listener requires explicit acknowledgement")
	}
	clusterName := strings.TrimSpace(req.ClusterName)
	if clusterName == "" {
		clusterName = flow.summary.DefaultClusterName
	}
	flow.clusterName = clusterName
	flow.state = cloudFlowStarting
	m.mu.Unlock()

	client := m.backend.newConnectClient()
	metaCtx, cancelMeta := context.WithTimeout(context.Background(), cloudInstallRequestTimeout)
	meta := m.backend.connectMetadata(metaCtx, flow.clients, clusterName)
	cancelMeta()

	createCtx, cancelCreate := context.WithTimeout(context.Background(), cloudInstallRequestTimeout)
	cr, err := client.Create(createCtx, meta)
	cancelCreate()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != flow {
		return nil, errFlowStale
	}
	if err != nil {
		flow.state = cloudFlowFailed
		flow.failure = &cloudInstallFailure{
			Kind:      cloudFailConnect,
			Message:   fmt.Sprintf("couldn't start the connect flow: %v", err),
			RetrySafe: true,
		}
		return flow, nil
	}

	// A cancel accepted while the Hub request was in flight has no context to
	// cancel yet (flow.cancel is nil until here), so honor it before launching
	// the run goroutine — otherwise the flow would proceed to approval despite
	// a successful cancel response.
	if flow.canceling {
		// Safe to retry: the approval page was never shown (the browser tab is
		// still blank), so nobody could have approved, and an unapproved connect
		// request expires at the Hub without creating a cluster.
		flow.state = cloudFlowFailed
		flow.failure = &cloudInstallFailure{
			Kind:      cloudFailCanceled,
			Message:   "Connection canceled before the approval page opened. No cluster was created.",
			RetrySafe: true,
		}
		return flow, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	flow.cancel = cancel
	flow.connectURL = cr.ConnectURL
	flow.state = cloudFlowAwaitingApproval
	go m.run(runCtx, flow, client, cr)
	return flow, nil
}

var errFlowStale = errors.New("this connection flow is no longer current")

// run is the manager-owned continuation: approval poll → provision → tunnel
// confirmation. It deliberately runs under its own context (not any HTTP
// request's) so navigation or modal close cannot kill a live flow.
func (m *cloudInstallManager) run(ctx context.Context, flow *cloudInstallFlow, client cloudConnectClient, cr *cloud.CreateResponse) {
	fail := func(kind, message string, guidance *cloudinstall.RecoveryGuidance, retrySafe bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.flow != flow {
			return
		}
		flow.state = cloudFlowFailed
		flow.failure = &cloudInstallFailure{Kind: kind, Message: message, Guidance: guidance, RetrySafe: retrySafe}
	}

	pr, err := client.PollUntilApproved(ctx, cr)
	if err != nil {
		clustersURL := cloud.ClustersURL(cr.ConnectURL)
		switch {
		case errors.Is(err, cloud.ErrConnectExpired):
			fail(cloudFailExpired, "The approval window elapsed before anyone approved this connection. No cluster was created.", nil, true)
		case errors.Is(err, cloud.ErrConnectRejected):
			fail(cloudFailRejected, "The connection request was rejected in the browser. No cluster was created.", nil, true)
		case errors.Is(err, cloud.ErrConnectPickupExpired):
			fail(cloudFailPickupExpired, "The approval was granted but its pickup window lapsed before this Radar could collect credentials. Remove the pending cluster before starting a fresh connection.", &cloudinstall.RecoveryGuidance{
				Summary:    "An organization owner can remove the pending cluster:",
				ClusterURL: clustersURL,
			}, false)
		case errors.Is(err, cloud.ErrConnectRecoveryTimeout):
			fail(cloudFailApprovalUnknown, "Radar lost contact with the Hub before learning whether the connection was approved. An approval may have committed; check the clusters list before retrying.", &cloudinstall.RecoveryGuidance{
				Summary:    "Check for a pending cluster before starting a fresh connection:",
				ClusterURL: clustersURL,
			}, false)
		case ctx.Err() != nil:
			// PollUntilApproved runs a final poll after cancellation and only
			// reaches here when that poll could NOT rule out an approval (its
			// error says so explicitly). Definitive outcomes — expired,
			// rejected, pickup-expired — matched the cases above, so a plain
			// cancellation is ambiguous, not harmless.
			fail(cloudFailApprovalUnknown, "Connection canceled. If the browser approval was already completed, a pending cluster may exist — the approval page stays valid until it expires.", &cloudinstall.RecoveryGuidance{
				Summary:    "Check for a pending cluster before starting a fresh connection:",
				ClusterURL: clustersURL,
			}, false)
		default:
			fail(cloudFailConnect, fmt.Sprintf("connect failed: %v", err), &cloudinstall.RecoveryGuidance{
				Summary:    "Check the clusters list before retrying:",
				ClusterURL: clustersURL,
			}, false)
		}
		return
	}

	clusterURL := cloud.ClusterURL(cr.ConnectURL, pr.ClusterID)
	if ctx.Err() != nil {
		g := cloudinstall.CanceledAfterApprovalGuidance(pr.ClusterID, clusterURL, "Start a new connection to try again.")
		fail(cloudFailCanceledApproved, g.Summary, &g, false)
		return
	}

	m.mu.Lock()
	if m.flow != flow {
		m.mu.Unlock()
		return
	}
	// Claiming the provisioning state and observing cancellation happen under
	// the same lock cancelFlow takes, so there is no window where cancel
	// returns success and Helm still runs: either cancel wins (canceling is
	// set, we bail here) or this transition wins (cancelFlow then sees
	// provisioning and refuses).
	if flow.canceling {
		m.mu.Unlock()
		g := cloudinstall.CanceledAfterApprovalGuidance(pr.ClusterID, clusterURL, "Start a new connection to try again.")
		fail(cloudFailCanceledApproved, g.Summary, &g, false)
		return
	}
	flow.state = cloudFlowProvisioning
	prepared := flow.prepared
	clients := flow.clients
	target := flow.commandTarget
	m.mu.Unlock()

	recovery := cloudinstall.ProvisionRecovery{
		Mode:            prepared.Mode(),
		ReleaseName:     prepared.ReleaseName(),
		Namespace:       prepared.Namespace(),
		Deployment:      prepared.Deployment(),
		CurrentRevision: prepared.CurrentRevision(),
	}

	// Provisioning is the non-cancelable critical section: the cancel endpoint
	// refuses while state == provisioning, and the context is detached so a
	// racing cancel from the approval phase cannot abort a Helm apply midway.
	provisionCtx, cancelProvision := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	err = m.backend.provision(provisionCtx, clients, prepared, cloudinstall.ProvisionConfig{
		Namespace:    prepared.Namespace(),
		ReleaseName:  prepared.ReleaseName(),
		ChartVersion: prepared.ChartVersion(),
		CloudURL:     pr.WSSURL,
		ClusterID:    pr.ClusterID,
		Token:        pr.Token,
	})
	cancelProvision()
	if err != nil {
		// The token is inside the Secret this step submits, so a rejection can
		// echo it back — admission webhooks routinely quote the object they
		// denied. Scrub before the message reaches a log or the status API,
		// both of which are readable by anyone who can reach this Radar.
		safeErr := redactCloudToken(err.Error(), pr.Token)
		log.Printf("[cloud-install] provisioning failed for cluster %s: %s", pr.ClusterID, safeErr)
		g := cloudinstall.PostApprovalProvisionGuidance(pr.ClusterID, clusterURL, recovery, err, target)
		g.Summary = redactCloudToken(g.Summary, pr.Token)
		for i, line := range g.Lines {
			g.Lines[i] = redactCloudToken(line, pr.Token)
		}
		// The summary carries the "do not rerun the installer" instruction, so
		// it must be the headline; the scrubbed Helm error rides along.
		g.Lines = append([]string{fmt.Sprintf("Provisioning failed: %s", safeErr)}, g.Lines...)
		fail(cloudFailProvision, g.Summary, &g, false)
		return
	}

	m.mu.Lock()
	if m.flow != flow {
		m.mu.Unlock()
		return
	}
	flow.state = cloudFlowWaitingTunnel
	m.mu.Unlock()

	if err := client.WaitUntilConsumed(ctx, cr, cloudTunnelConfirmationWait); err != nil {
		g := cloudinstall.TunnelConfirmationGuidance(err, pr.ClusterID, pr.WSSURL, clusterURL, prepared.Deployment(), target)
		g.Summary = redactCloudToken(g.Summary, pr.Token)
		for i, line := range g.Lines {
			g.Lines[i] = redactCloudToken(line, pr.Token)
		}
		fail(cloudFailTunnelUnconfirmed, g.Summary, &g, false)
		return
	}

	connected := &cloudInstallConnected{
		ClusterID:  pr.ClusterID,
		ClusterURL: clusterURL,
		TrackCmd:   fmt.Sprintf("%s -n %s rollout status deployment/%s", target.Kubectl(), prepared.Deployment().Namespace, prepared.Deployment().Name),
	}
	if recovery.Mode == cloudinstall.ProvisionAdopt {
		g := cloudinstall.AdoptionRollbackGuidance(recovery, clusterURL, target)
		connected.Rollback = &g
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != flow {
		return
	}
	flow.state = cloudFlowConnected
	flow.connected = connected
}

// cancel aborts an approval or tunnel wait. Provisioning cannot be canceled —
// the Helm critical section resolves via its own atomic rollback semantics.
func (m *cloudInstallManager) cancelFlow(flowID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow := m.flow
	if flow == nil || flow.id != flowID {
		return errFlowStale
	}
	switch flow.state {
	case cloudFlowReady, cloudFlowPreparing:
		m.flow = nil
		return nil
	case cloudFlowStarting, cloudFlowAwaitingApproval, cloudFlowWaitingTunnel:
		flow.canceling = true
		if flow.cancel != nil {
			flow.cancel()
		}
		return nil
	case cloudFlowProvisioning:
		return errors.New("provisioning is in progress and cannot be canceled; it resolves atomically")
	default:
		return fmt.Errorf("the connection flow is already %s", flow.state)
	}
}

func (m *cloudInstallManager) dismiss(flowID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow == nil || m.flow.id != flowID {
		return errFlowStale
	}
	if !m.flow.terminal() {
		return errors.New("only a finished connection flow can be dismissed")
	}
	m.flow = nil
	return nil
}

func (m *cloudInstallManager) clearFlow(flow *cloudInstallFlow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow == flow {
		m.flow = nil
	}
}

type cloudInstallStatus struct {
	FlowID      string                   `json:"flowId,omitempty"`
	State       string                   `json:"state"`
	Plan        *cloudInstallPlanSummary `json:"plan,omitempty"`
	ClusterName string                   `json:"clusterName,omitempty"`
	ConnectURL  string                   `json:"connectUrl,omitempty"`
	Connected   *cloudInstallConnected   `json:"connected,omitempty"`
	Failure     *cloudInstallFailure     `json:"failure,omitempty"`
}

func (m *cloudInstallManager) status() cloudInstallStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *cloudInstallManager) statusLocked() cloudInstallStatus {
	flow := m.flow
	if flow == nil {
		return cloudInstallStatus{State: "idle"}
	}
	st := cloudInstallStatus{
		FlowID:      flow.id,
		State:       flow.state,
		ClusterName: flow.clusterName,
		ConnectURL:  flow.connectURL,
		Connected:   flow.connected,
		Failure:     flow.failure,
	}
	if flow.state != cloudFlowPreparing {
		summary := flow.summary
		if summary.Mode != "" {
			st.Plan = &summary
		}
	}
	return st
}

// --- HTTP layer ---

// cloudConnectDeploymentMode is a test seam: the test process has no
// kubeconfig, which the detection heuristic reads as in-cluster.
var cloudConnectDeploymentMode = deploymentMode

// cloudConnectDriverEnabled is the security boundary for the driver lane: a
// local process, no auth, and no existing tunnel. Anyone who can reach an
// unauthenticated Radar already wields the server's kubeconfig through
// /api/resources/apply and pods/exec — strictly more power than installing a
// chart — so gating this lane on the listener address too would hold the
// weaker capability to a higher bar than the stronger ones. A non-loopback
// listener is surfaced as an exposure note on the plan card instead; mutating
// endpoints still require a same-origin request.
func (s *Server) cloudConnectDriverEnabled() bool {
	return !cloudMode() &&
		cloudConnectDeploymentMode() == k8s.DeploymentModeLocal &&
		!s.authConfig.Enabled() &&
		!s.cloudConnectCfg.CloudTunnelConfigured
}

// sharedListener reports whether Radar answers beyond loopback, meaning
// someone other than the operator could reach the approval flow.
func (s *Server) sharedListener() bool {
	return !cloud.IsLoopbackHostname(s.listenAddress)
}

func (s *Server) requireCloudConnectDriver(w http.ResponseWriter, r *http.Request, mutating bool) bool {
	if s.cloudInstall == nil || !s.cloudConnectDriverEnabled() {
		s.writeError(w, http.StatusNotFound, "Cloud connect is not available on this deployment")
		return false
	}
	if mutating && !sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
		return false
	}
	return true
}

func (s *Server) handleCloudInstallPrepare(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudConnectDriver(w, r, true) {
		return
	}
	if !s.requireConnected(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cloudInstallHandlerTimeout)
	defer cancel()
	flow, blocked, err := s.cloudInstall.prepare(ctx)
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case errors.Is(err, errFlowActive):
		s.writeCloudInstallJSON(w, http.StatusConflict, s.cloudInstall.status())
	case err != nil:
		log.Printf("[cloud-install] prepare failed: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	case blocked != nil:
		s.writeJSON(w, map[string]any{"state": "blocked", "blocked": blocked})
	default:
		_ = flow
		s.writeJSON(w, s.cloudInstall.status())
	}
}

func (s *Server) handleCloudInstallStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudConnectDriver(w, r, true) {
		return
	}
	var req cloudInstallStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.cloudInstall.start(req); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errFlowStale) {
			status = http.StatusConflict
		}
		s.writeError(w, status, err.Error())
		return
	}
	s.writeJSON(w, s.cloudInstall.status())
}

func (s *Server) handleCloudInstallStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudConnectDriver(w, r, false) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, s.cloudInstall.status())
}

func (s *Server) handleCloudInstallCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudConnectDriver(w, r, true) {
		return
	}
	var req struct {
		FlowID string `json:"flowId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := s.cloudInstall.cancelFlow(req.FlowID); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errFlowStale) {
			status = http.StatusGone
		}
		s.writeError(w, status, err.Error())
		return
	}
	s.writeJSON(w, s.cloudInstall.status())
}

func (s *Server) handleCloudInstallDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudConnectDriver(w, r, true) {
		return
	}
	var req struct {
		FlowID string `json:"flowId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := s.cloudInstall.dismiss(req.FlowID); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errFlowStale) {
			status = http.StatusGone
		}
		s.writeError(w, status, err.Error())
		return
	}
	s.writeJSON(w, s.cloudInstall.status())
}

func (s *Server) writeCloudInstallJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

// cloudConnectCapability picks the connect lane the frontend should render:
// the in-product driver, a Hub-wizard link, or nil — nothing to pitch. nil
// covers any tunnel-configured run, not just full cloud mode, so a binary
// started with --cloud-url alone is not pitched a connection it already has.
// (Embedded mode additionally hides the funnel client-side as chrome policy.)
func (s *Server) cloudConnectCapability() *k8s.CloudConnectCapability {
	if cloudMode() || s.cloudConnectCfg.CloudTunnelConfigured {
		return nil
	}
	if !cloudFunnelInCohort() {
		return nil
	}
	lane := "wizard"
	if s.cloudConnectDriverEnabled() {
		lane = "driver"
	}
	return &k8s.CloudConnectCapability{
		Lane:   lane,
		AppURL: s.cloudConnectCfg.HubAppURL,
		APIURL: s.cloudConnectCfg.HubAPIURL,
	}
}

// sameOriginOK is CSRF protection for the connect and AI-diagnose endpoints: a
// page on another origin must not be able to drive an install or spawn an agent.
// It compares the Origin against the authority the client actually used, rather
// than an allowlist of loopback names — a loopback-only allowlist would 403 the
// legitimate browser on a non-loopback listener (a supported deployment) while
// still admitting a scripted caller that simply omits the header.
//
// Loopback-to-loopback is additionally allowed for the Vite dev proxy, which
// forwards its own :9273 origin to the backend on :9280.
func sameOriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // same-origin navigation or a non-browser client
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return browserLoopbackHostname(u.Hostname()) && requestHostIsLoopback(r)
}

// redactCloudToken removes a cluster token that an upstream error may have
// echoed back. Post-approval failures carry Kubernetes messages that can quote
// the Secret being applied, and those messages land in server logs and the
// status API — neither of which the token may ever reach.
//
// Both representations must go. The Secret is submitted via stringData, which
// the apiserver folds into base64 `data` before admission runs, so a webhook
// quoting the object it denied reports the ENCODED token, not the raw one.
func redactCloudToken(text, token string) string {
	if token == "" {
		return text
	}
	text = strings.ReplaceAll(text, token, cloudTokenRedaction)
	for _, enc := range []string{
		base64.StdEncoding.EncodeToString([]byte(token)),
		base64.RawStdEncoding.EncodeToString([]byte(token)),
		base64.URLEncoding.EncodeToString([]byte(token)),
		base64.RawURLEncoding.EncodeToString([]byte(token)),
	} {
		text = strings.ReplaceAll(text, enc, cloudTokenRedaction)
	}
	return text
}

const cloudTokenRedaction = "[REDACTED]"
