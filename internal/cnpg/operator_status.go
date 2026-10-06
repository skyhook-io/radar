package cnpg

import (
	"context"
	"log"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	cnpgOperatorReconciling    = "reconciling"
	cnpgOperatorNotReconciling = "notReconciling"
	cnpgOperatorUnknown        = "unknown"
	// cnpgOperatorNotWatched: an operator is visible but none watches the
	// namespace, which the Operator screen reports on its own.
	cnpgOperatorNotWatched = "notWatched"

	cnpgOperatorStatusTTL = 10 * time.Second
	// Bounds the detached operator read; a slow API server reads as unknown.
	cnpgOperatorFactsTimeout = 10 * time.Second
)

// CNPGOperatorVerdict is whether the operator watching one namespace is acting
// on it. While it is not, CNPG status in that namespace is whatever the
// operator last wrote, and writes Radar makes wait for it (or, with the
// admission webhook down, are rejected outright).
type CNPGOperatorVerdict struct {
	State string `json:"state"`
	// Reasons are the observations behind notReconciling, as sentences.
	Reasons []string `json:"reasons,omitempty"`
	// Unknown says what could not be read when the state is unknown, or what
	// is still unread next to a known state.
	Unknown string `json:"unknown,omitempty"`
	// WebhookRejects is true when a CNPG admission webhook fails closed and its
	// Service has no ready endpoint, so the API server rejects writes to CNPG
	// objects; nil when that could not be read.
	WebhookRejects *bool  `json:"webhookRejects"`
	WebhookReason  string `json:"webhookReason,omitempty"`
	// Operator is namespace/name of the operator Deployment the verdict is about.
	Operator string `json:"operator,omitempty"`
}

// CNPGOperatorStatusResponse is GET /api/cnpg/operator/status.
type CNPGOperatorStatusResponse struct {
	Namespaces map[string]CNPGOperatorVerdict `json:"namespaces"`
}

type cnpgOperatorFact struct {
	namespace, name string
	watch           CNPGOperatorWatch
	leading         *bool
	reason          string
}

type cnpgOperatorFacts struct {
	operators []cnpgOperatorFact
	// deploymentsUnknown is set when the operator Deployments could not be
	// fully listed, so "none found" is not an answer.
	deploymentsUnknown string
	webhookRejects     *bool
	webhookReason      string
	webhookUnknown     string
}

var (
	cnpgOperatorStatusMu   sync.Mutex
	cnpgOperatorStatusMemo = map[string]cnpgOperatorStatusMemoEntry{}
)

type cnpgOperatorStatusMemoEntry struct {
	expires time.Time
	facts   cnpgOperatorFacts
	cache   *k8s.ResourceCache
	scope   []string
	access  integration.KindAccess
	grants  map[auth.Grant]string
}

// cnpgOperatorVerdictFor is the verdict for one namespace, or unknown when
// the facts cannot be gathered at all.
func (s *Reader) operatorVerdictFor(ctx context.Context, namespace string) CNPGOperatorVerdict {
	return s.operatorFactsFor(ctx).verdict(namespace)
}

func (s *Reader) operatorFactsFor(ctx context.Context) cnpgOperatorFacts {
	if s.Observations.Cache == nil || s.Clients.Typed == nil {
		return s.readCNPGOperatorFacts(ctx)
	}
	scope := s.Observations.OperatorScope(ctx)
	access, _ := s.Observations.TypedScope(ctx, s.Observations.Cache, scope, "apps", "deployments")
	identity := s.Identity
	now := time.Now()
	cnpgOperatorStatusMu.Lock()
	entry, hit := cnpgOperatorStatusMemo[identity]
	cnpgOperatorStatusMu.Unlock()
	if hit && now.Before(entry.expires) && entry.cache == s.Observations.Cache && reflect.DeepEqual(entry.scope, scope) && reflect.DeepEqual(entry.access, access) {
		authorized := true
		for grant, permission := range entry.grants {
			if s.Access.Permission(ctx, grant) != permission {
				authorized = false
				break
			}
		}
		if authorized {
			return entry.facts
		}
	}
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), cnpgOperatorFactsTimeout)
	defer cancel()
	grants := map[auth.Grant]string{}
	reader := *s
	reader.Access.Permission = func(ctx context.Context, grant auth.Grant) string {
		permission := s.Access.Permission(ctx, grant)
		grants[grant] = permission
		return permission
	}
	facts := reader.readCNPGOperatorFacts(detached)
	cnpgOperatorStatusMu.Lock()
	defer cnpgOperatorStatusMu.Unlock()
	for key, entry := range cnpgOperatorStatusMemo {
		if now.After(entry.expires) {
			delete(cnpgOperatorStatusMemo, key)
		}
	}
	cnpgOperatorStatusMemo[identity] = cnpgOperatorStatusMemoEntry{expires: now.Add(cnpgOperatorStatusTTL), facts: facts, cache: s.Observations.Cache, scope: append([]string(nil), scope...), access: access, grants: grants}
	return facts
}

func (s *Reader) readCNPGOperatorFacts(ctx context.Context) cnpgOperatorFacts {
	out := cnpgOperatorFacts{}
	cache := s.Observations.Cache
	typed := s.Clients.Typed
	if cache == nil || typed == nil {
		out.deploymentsUnknown = "Radar is not connected to the cluster"
		out.webhookUnknown = out.deploymentsUnknown
		return out
	}
	acc, deployments := s.operatorDeployments(ctx, cache, s.Observations.OperatorScope(ctx))
	if acc.State != integration.KindCoverageFull {
		out.deploymentsUnknown = "Radar cannot list Deployments in every namespace, so the operator may be out of view"
		if acc.State == integration.KindCoverageSyncing {
			out.deploymentsUnknown = "Deployments are still syncing"
		}
	}
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] != cnpgOperatorNameValue {
			continue
		}
		c := cnpgOperatorContainerOf(d)
		fact := cnpgOperatorFact{namespace: d.Namespace, name: d.Name, watch: cnpgOperatorWatchOf(c, d.Namespace)}
		leader := s.operatorLeader(ctx, typed, d, c, nil)
		fact.leading, fact.reason = cnpgOperatorLeading(d, leader)
		out.operators = append(out.operators, fact)
	}
	configs, services := s.operatorWebhooks(ctx, typed)
	out.webhookRejects, out.webhookReason, out.webhookUnknown = cnpgWebhookRejects(configs, services)
	return out
}

// cnpgOperatorLeading judges one operator Deployment: leading is nil when the
// lease could not be read and the Deployment alone does not settle it.
func cnpgOperatorLeading(d *appsv1.Deployment, leader CNPGOperatorLeader) (*bool, string) {
	f, t := false, true
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	if desired == 0 {
		return &f, "the operator Deployment " + d.Namespace + "/" + d.Name + " is scaled to zero"
	}
	if d.Status.ReadyReplicas == 0 {
		return &f, "the operator Deployment " + d.Namespace + "/" + d.Name + " has no ready Pod"
	}
	switch leader.State {
	case "disabled":
		return &t, ""
	case cnpgReadOK:
		if leader.Stale {
			return &f, "no operator instance has renewed the leader lease " + d.Namespace + "/" + cnpgOperatorLeaseName
		}
		return &t, ""
	case cnpgReadDenied:
		return nil, "its leader lease is not readable (needs " + integration.GrantText(leader.Grant) + ")"
	default:
		if leader.Reason != "" {
			log.Printf("[cnpg] Operator %s/%s leader lease unread: %s", d.Namespace, d.Name, leader.Reason)
		}
		return nil, "couldn't read its leader lease"
	}
}

// cnpgWebhookRejects reports whether a fail-closed CNPG webhook has no ready
// endpoint behind it. A configuration that is absent rejects nothing.
func cnpgWebhookRejects(configs []CNPGOperatorWebhookConfig, services []CNPGOperatorWebhookService) (*bool, string, string) {
	bySvc := map[string]CNPGOperatorWebhookService{}
	for _, svc := range services {
		bySvc[svc.Namespace+"/"+svc.Name] = svc
	}
	var unknown []string
	var rejecting []string
	checked := 0
	for _, cfg := range configs {
		switch cfg.State {
		case cnpgReadOK:
		case cnpgReadNotFound:
			continue
		case cnpgReadDenied:
			unknown = append(unknown, cfg.Kind+" "+cfg.Name+" is not readable (needs "+integration.GrantText(cfg.Grant)+")")
			continue
		default:
			unknown = append(unknown, cfg.Kind+" "+cfg.Name+" could not be read")
			continue
		}
		for _, wh := range cfg.Webhooks {
			if wh.FailurePolicy != "Fail" || wh.Service == "" {
				continue
			}
			svc, ok := bySvc[wh.Service]
			switch {
			case !ok || svc.ReadyEndpoints == nil:
				grant := ""
				if ok && svc.Grant != nil {
					grant = " (needs " + svc.Grant.String() + ")"
				}
				unknown = append(unknown, "the endpoints of webhook Service "+wh.Service+" are not readable"+grant)
			case *svc.ReadyEndpoints == 0:
				rejecting = append(rejecting, wh.Service)
			default:
				checked++
			}
		}
	}
	rejecting = dedupeSorted(rejecting)
	unknown = dedupeSorted(unknown)
	if len(rejecting) > 0 {
		t := true
		return &t, "the admission webhook Service " + strings.Join(rejecting, ", ") + " has no ready endpoint and fails closed, so the API server rejects writes to CloudNativePG objects", strings.Join(unknown, "; ")
	}
	if len(unknown) > 0 {
		return nil, "", strings.Join(unknown, "; ")
	}
	f := false
	return &f, "", ""
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func (f cnpgOperatorFacts) verdict(namespace string) CNPGOperatorVerdict {
	v := CNPGOperatorVerdict{State: cnpgOperatorUnknown, WebhookRejects: f.webhookRejects, WebhookReason: f.webhookReason}
	// An operator whose WATCH_NAMESPACE Radar cannot resolve may or may not
	// watch this namespace: it can neither confirm reconciliation nor rule it out.
	var watching []cnpgOperatorFact
	var unresolved []string
	couldReconcile := false
	for _, op := range f.operators {
		switch {
		case op.watch.Unresolved != "":
			unresolved = append(unresolved, "whether "+op.namespace+"/"+op.name+" watches "+namespace+" is unknown: its WATCH_NAMESPACE is "+op.watch.Unresolved)
			if op.leading == nil || *op.leading {
				couldReconcile = true
			}
		case op.watch.All || slices.Contains(op.watch.Namespaces, namespace):
			watching = append(watching, op)
		}
	}
	unknown := []string{}
	switch {
	case len(watching) == 0 && len(unresolved) > 0:
		unknown = append(unknown, unresolved...)
		if f.deploymentsUnknown != "" {
			unknown = append(unknown, f.deploymentsUnknown)
		}
	case len(watching) == 0 && f.deploymentsUnknown != "":
		unknown = append(unknown, f.deploymentsUnknown)
	case len(watching) == 0 && len(f.operators) == 0:
		unknown = append(unknown, "No operator Deployment labelled "+cnpgOperatorNameLabel+"="+cnpgOperatorNameValue+" was found")
	case len(watching) == 0:
		v.State = cnpgOperatorNotWatched
	default:
		v.Operator = watching[0].namespace + "/" + watching[0].name
		leading, anyUnknown := false, false
		var notLeading []string
		for _, op := range watching {
			switch {
			case op.leading == nil:
				anyUnknown = true
				unknown = append(unknown, op.reason)
			case *op.leading:
				leading = true
				v.Operator = op.namespace + "/" + op.name
			default:
				notLeading = append(notLeading, op.reason)
			}
		}
		switch {
		case leading:
			v.State = cnpgOperatorReconciling
			unknown = nil
		case anyUnknown:
		case couldReconcile:
			unknown = append(unknown, unresolved...)
		default:
			v.State = cnpgOperatorNotReconciling
			v.Reasons = notLeading
		}
	}
	if f.webhookRejects == nil && f.webhookUnknown != "" {
		unknown = append(unknown, f.webhookUnknown)
	}
	v.Unknown = strings.Join(unknown, "; ")
	return v
}

// cnpgOperatorWebhookGuard refuses a write that goes through a CNPG admission
// webhook the API server cannot reach; status subresource patches and Pod
// deletes do not, so those actions keep their verdict.
func cnpgOperatorWebhookGuard(v CNPGOperatorVerdict, capability integration.ActionCapability) integration.ActionCapability {
	if !capability.Allowed || v.WebhookRejects == nil || !*v.WebhookRejects {
		return capability
	}
	capability.Allowed = false
	capability.ReasonCode = "operator_webhook"
	capability.Reason = "The API server would reject it: " + v.WebhookReason
	return capability
}

func (s *Reader) OperatorStatus(ctx context.Context, namespaces []string) CNPGOperatorStatusResponse {
	resp := CNPGOperatorStatusResponse{Namespaces: map[string]CNPGOperatorVerdict{}}
	if len(namespaces) == 0 {
		return resp
	}
	facts := s.operatorFactsFor(ctx)
	for _, namespace := range namespaces {
		resp.Namespaces[namespace] = facts.verdict(namespace)
	}
	return resp
}
