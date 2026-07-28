package server

// In-product Cloud Connect: the modal's "Connect this cluster" button drives
// the same device-flow install the `radar cloud install` CLI performs, through
// a server-side single-flight flow manager. The driver lane is deliberately
// narrow — local deployment, auth disabled, loopback listener, no existing
// Cloud tunnel — the one configuration where whoever reaches this UI already
// holds the operator's kubeconfig power (parity: /api/resources/apply, exec).
// Every other configuration routes to the Hub wizard instead (see
// docs/cloud-connect.md for the full scenario matrix).
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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	cloudFailConnect            = "connect_failed"
	cloudFailRejected           = "rejected"
	cloudFailExpired            = "expired"
	cloudFailPickupExpired      = "pickup_expired"
	cloudFailApprovalUnknown    = "approval_unknown"
	cloudFailCanceledApproved   = "canceled_after_approval"
	cloudFailProvision          = "provision_failed"
	cloudFailTunnelUnconfirmed  = "tunnel_unconfirmed"
)

const (
	cloudInstallRequestTimeout  = 30 * time.Second
	cloudTunnelConfirmationWait = 5 * time.Minute
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
	inspectPlan      func(ctx context.Context, c cloudInstallClients, namespace, release string) (cloudinstall.InstallPlan, error)
	prepare          func(ctx context.Context, c cloudInstallClients, cfg cloudinstall.PrepareConfig) (preparedInstall, error)
	preflight        func(ctx context.Context, c cloudInstallClients, prepared preparedInstall) (cloudinstall.PreflightResult, error)
	provision        func(ctx context.Context, c cloudInstallClients, prepared preparedInstall, cfg cloudinstall.ProvisionConfig) error
	newConnectClient func() cloudConnectClient
	connectMetadata  func(ctx context.Context, c cloudInstallClients, clusterName string) cloud.ConnectMetadata
}

type cloudInstallConnected struct {
	ClusterID  string                        `json:"clusterId"`
	ClusterURL string                        `json:"clusterUrl"`
	Deployment helm.DeploymentRef            `json:"deployment"`
	TrackCmd   string                        `json:"trackCommand"`
	Rollback   *cloudinstall.RecoveryGuidance `json:"rollback,omitempty"`
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
}

// cloudInstallBlocked explains why the driver lane cannot serve this cluster.
// It is returned by prepare without retaining a flow.
type cloudInstallBlocked struct {
	Reason   string   `json:"reason"` // gitops | preflight | unsupported
	Message  string   `json:"message"`
	Blocking []string `json:"blocking,omitempty"`
}

type cloudInstallFlow struct {
	id          string
	state       string
	contextName string
	clients     cloudInstallClients
	plan        cloudinstall.InstallPlan
	prepared    preparedInstall
	summary     cloudInstallPlanSummary

	clusterName       string
	connectURL        string
	approvalExpiresAt time.Time
	cancel            context.CancelFunc
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
}

func newCloudInstallManager(cfg CloudConnectConfig) *cloudInstallManager {
	m := &cloudInstallManager{cfg: cfg}
	m.backend = cloudInstallBackend{
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
		flow.state = cloudFlowFailed
		flow.failure = &cloudInstallFailure{
			Kind:      cloudFailApprovalUnknown,
			Message:   "Connection canceled while the approval request was being created. If the request reached the Hub, its approval page may still be live.",
			Guidance:  &cloudinstall.RecoveryGuidance{Summary: "Check for a pending cluster before starting a fresh connection:", ClusterURL: cloud.ClustersURL(cr.ConnectURL)},
			RetrySafe: false,
		}
		return flow, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	flow.cancel = cancel
	flow.connectURL = cr.ConnectURL
	flow.approvalExpiresAt = time.Now().Add(time.Duration(cr.ExpiresIn) * time.Second)
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
	target := m.commandTarget(flow.contextName)
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
		log.Printf("[cloud-install] provisioning failed for cluster %s: %v", pr.ClusterID, err)
		g := cloudinstall.PostApprovalProvisionGuidance(pr.ClusterID, clusterURL, recovery, err, target)
		// The summary carries the "do not rerun the installer" instruction, so
		// it must be the headline; the raw Helm error rides along as detail.
		g.Lines = append([]string{fmt.Sprintf("Provisioning failed: %v", err)}, g.Lines...)
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
		fail(cloudFailTunnelUnconfirmed, g.Summary, &g, false)
		return
	}

	connected := &cloudInstallConnected{
		ClusterID:  pr.ClusterID,
		ClusterURL: clusterURL,
		Deployment: prepared.Deployment(),
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
	FlowID            string                   `json:"flowId,omitempty"`
	State             string                   `json:"state"`
	Plan              *cloudInstallPlanSummary `json:"plan,omitempty"`
	ClusterName       string                   `json:"clusterName,omitempty"`
	ConnectURL        string                   `json:"connectUrl,omitempty"`
	ApprovalExpiresAt string                   `json:"approvalExpiresAt,omitempty"`
	Connected         *cloudInstallConnected   `json:"connected,omitempty"`
	Failure           *cloudInstallFailure     `json:"failure,omitempty"`
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
	if !flow.approvalExpiresAt.IsZero() {
		st.ApprovalExpiresAt = flow.approvalExpiresAt.UTC().Format(time.RFC3339)
	}
	return st
}

// --- HTTP layer ---

// cloudConnectDriverEnabled is the whole security boundary for the driver
// lane: local process, no auth, loopback-only listener, no existing tunnel.
// In that configuration every UI user already wields the server's kubeconfig
// through existing endpoints (apply, exec, Helm writes).
// cloudConnectDeploymentMode is a test seam: the test process has no
// kubeconfig, which the detection heuristic reads as in-cluster.
var cloudConnectDeploymentMode = deploymentMode

func (s *Server) cloudConnectDriverEnabled() bool {
	return !cloudMode() &&
		cloudConnectDeploymentMode() == k8s.DeploymentModeLocal &&
		!s.authConfig.Enabled() &&
		cloud.IsLoopbackHostname(s.listenAddress) &&
		!s.cloudConnectCfg.CloudTunnelConfigured
}

func (s *Server) requireCloudConnectDriver(w http.ResponseWriter, r *http.Request, mutating bool) bool {
	if s.cloudInstall == nil || !s.cloudConnectDriverEnabled() {
		s.writeError(w, http.StatusNotFound, "Cloud connect is not available on this deployment")
		return false
	}
	if mutating && !localOriginOK(r) {
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
	flow, blocked, err := s.cloudInstall.prepare(r.Context())
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

// cloudConnectCapability picks the connect lane the frontend should render.
// Embedded/cloud deployments hide the funnel entirely client-side; everything
// else gets either the in-product driver or a Hub-wizard link.
func (s *Server) cloudConnectCapability() *k8s.CloudConnectCapability {
	lane := "wizard"
	if s.cloudConnectDriverEnabled() {
		lane = "driver"
	}
	return &k8s.CloudConnectCapability{Lane: lane, AppURL: s.cloudConnectCfg.HubAppURL}
}
