package cnpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func operatorDeployment(replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cnpg-system", Name: "cnpg-controller-manager"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestCNPGOperatorLeading(t *testing.T) {
	ok := CNPGOperatorLeader{ReadSource: integration.ReadSource{State: cnpgReadOK}}
	stale := ok
	stale.Stale = true
	denied := CNPGOperatorLeader{ReadSource: integration.ReadSource{State: cnpgReadDenied, Grant: cnpgGrantGetLeases.In("cnpg-system").Ref()}}
	cases := []struct {
		name    string
		d       *appsv1.Deployment
		leader  CNPGOperatorLeader
		leading *bool
	}{
		{"held lease", operatorDeployment(1, 1), ok, boolPtr(true)},
		{"expired lease", operatorDeployment(1, 1), stale, boolPtr(false)},
		{"crash-looping Pod leads nothing whatever the lease says", operatorDeployment(1, 0), ok, boolPtr(false)},
		{"scaled to zero", operatorDeployment(0, 0), denied, boolPtr(false)},
		{"lease unreadable", operatorDeployment(1, 1), denied, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := cnpgOperatorLeading(tc.d, tc.leader)
			if (got == nil) != (tc.leading == nil) || (got != nil && *got != *tc.leading) {
				t.Fatalf("leading = %v (%s), want %v", got, reason, tc.leading)
			}
			if (got == nil || !*got) && reason == "" {
				t.Error("a not-leading or unknown verdict needs a reason")
			}
		})
	}
}

func TestCNPGWebhookRejects(t *testing.T) {
	zero, one := 0, 1
	cfg := func(policy string) []CNPGOperatorWebhookConfig {
		return []CNPGOperatorWebhookConfig{{
			Kind: "ValidatingWebhookConfiguration", Name: "cnpg-validating-webhook-configuration",
			ReadSource: integration.ReadSource{State: cnpgReadOK},
			Webhooks:   []CNPGOperatorWebhook{{Name: "vcluster.cnpg.io", FailurePolicy: policy, Service: "cnpg-system/cnpg-webhook-service"}},
		}}
	}
	svc := func(ready *int) []CNPGOperatorWebhookService {
		return []CNPGOperatorWebhookService{{Namespace: "cnpg-system", Name: "cnpg-webhook-service", ReadyEndpoints: ready}}
	}
	if got, reason, _ := cnpgWebhookRejects(cfg("Fail"), svc(&zero)); got == nil || !*got || !strings.Contains(reason, "cnpg-system/cnpg-webhook-service") {
		t.Errorf("fail-closed, no endpoint: %v %q", got, reason)
	}
	if got, _, _ := cnpgWebhookRejects(cfg("Ignore"), svc(&zero)); got == nil || *got {
		t.Errorf("fail-open webhook rejects nothing: %v", got)
	}
	if got, _, _ := cnpgWebhookRejects(cfg("Fail"), svc(&one)); got == nil || *got {
		t.Errorf("ready endpoint: %v", got)
	}
	if got, _, unknown := cnpgWebhookRejects(cfg("Fail"), svc(nil)); got != nil || unknown == "" {
		t.Errorf("unreadable endpoints must be unknown: %v %q", got, unknown)
	}
	denied := []CNPGOperatorWebhookConfig{{Kind: "ValidatingWebhookConfiguration", Name: "x", ReadSource: integration.ReadSource{State: cnpgReadDenied, Grant: cnpgGrantGetValidatingWH.Ref()}}}
	if got, _, unknown := cnpgWebhookRejects(denied, nil); got != nil || !strings.Contains(unknown, "get validatingwebhookconfigurations") {
		t.Errorf("denied config: %v %q", got, unknown)
	}
	absent := []CNPGOperatorWebhookConfig{{Kind: "ValidatingWebhookConfiguration", Name: "x", ReadSource: integration.ReadSource{State: cnpgReadNotFound}}}
	if got, _, _ := cnpgWebhookRejects(absent, nil); got == nil || *got {
		t.Errorf("absent config rejects nothing: %v", got)
	}
}

func TestCNPGOperatorFactsVerdict(t *testing.T) {
	yes, no := true, false
	down := cnpgOperatorFact{namespace: "cnpg-system", name: "op", watch: CNPGOperatorWatch{All: true}, leading: &no, reason: "the operator Deployment cnpg-system/op has no ready Pod"}
	up := down
	up.leading, up.reason = &yes, ""
	scoped := up
	scoped.watch = CNPGOperatorWatch{Namespaces: []string{"team-a"}}
	unread := down
	unread.leading, unread.reason = nil, "the leader lease is not readable"

	f := cnpgOperatorFacts{operators: []cnpgOperatorFact{down}, webhookRejects: &no}
	if v := f.verdict("pg"); v.State != cnpgOperatorNotReconciling || len(v.Reasons) != 1 || v.Operator != "cnpg-system/op" {
		t.Errorf("down = %+v", v)
	}
	f.operators = []cnpgOperatorFact{up}
	if v := f.verdict("pg"); v.State != cnpgOperatorReconciling || v.Unknown != "" {
		t.Errorf("up = %+v", v)
	}
	f.operators = []cnpgOperatorFact{scoped}
	if v := f.verdict("pg"); v.State != cnpgOperatorNotWatched {
		t.Errorf("not watched = %+v", v)
	}
	f.operators = []cnpgOperatorFact{unread}
	if v := f.verdict("pg"); v.State != cnpgOperatorUnknown || !strings.Contains(v.Unknown, "lease") {
		t.Errorf("unreadable lease = %+v", v)
	}
	// WATCH_NAMESPACE from a ConfigMap Radar does not read: a healthy leader
	// may be restricted to another namespace, so it proves nothing here.
	fromConfigMap := up
	fromConfigMap.name = "op-cm"
	fromConfigMap.watch = CNPGOperatorWatch{Source: "env WATCH_NAMESPACE", Unresolved: "set from ConfigMap cnpg-config key WATCH_NAMESPACE"}
	f.operators = []cnpgOperatorFact{fromConfigMap}
	if v := f.verdict("team-b"); v.State != cnpgOperatorUnknown || !strings.Contains(v.Unknown, "ConfigMap cnpg-config") {
		t.Errorf("unresolved watch scope = %+v, want unknown naming the ConfigMap", v)
	}
	f.operators = []cnpgOperatorFact{fromConfigMap, up}
	if v := f.verdict("team-b"); v.State != cnpgOperatorReconciling || v.Operator != "cnpg-system/op" {
		t.Errorf("confirmed watcher beside an unresolved one = %+v", v)
	}
	f.operators = []cnpgOperatorFact{fromConfigMap, down}
	if v := f.verdict("team-b"); v.State != cnpgOperatorUnknown {
		t.Errorf("down confirmed watcher beside a leading unresolved one = %+v, want unknown", v)
	}
	f = cnpgOperatorFacts{deploymentsUnknown: "Radar cannot list Deployments in every namespace"}
	if v := f.verdict("pg"); v.State != cnpgOperatorUnknown || v.Unknown == "" {
		t.Errorf("no visible operator = %+v", v)
	}
}

func TestCNPGApplyOperatorGuard(t *testing.T) {
	allowed := integration.ActionCapability{Allowed: true, Permission: integration.PermissionAllowed}
	refused := integration.ActionCapability{Reason: "The cluster is hibernated", Permission: integration.PermissionAllowed}
	rejects := true
	resp := &CNPGClusterCapabilitiesResponse{
		Operator: CNPGOperatorVerdict{State: cnpgOperatorNotReconciling, WebhookRejects: &rejects, WebhookReason: "the admission webhook Service cnpg-system/cnpg-webhook-service has no ready endpoint"},
		Actions: CNPGClusterActions{
			Backup: allowed, Switchover: allowed, Restart: allowed, RestartInstance: allowed, Fence: refused, Psql: allowed,
		},
		InstanceActions: map[string]CNPGInstanceActions{"pg-1": {Fence: allowed, Restart: allowed}},
	}
	cnpgApplyOperatorGuard(resp)
	a := resp.Actions
	if a.Backup.Allowed || !strings.Contains(a.Backup.Reason, "no ready endpoint") || a.Restart.Allowed || resp.InstanceActions["pg-1"].Fence.Allowed {
		t.Errorf("webhook-bound writes stay allowed: %+v", resp)
	}
	if !a.Switchover.Allowed || !a.RestartInstance.Allowed || !a.Psql.Allowed || !resp.InstanceActions["pg-1"].Restart.Allowed {
		t.Errorf("status patches, Pod deletes and exec do not pass the webhook: %+v", a)
	}
	if a.Fence.Reason != "The cluster is hibernated" {
		t.Errorf("an earlier refusal keeps its reason: %q", a.Fence.Reason)
	}

	resp.Operator.WebhookRejects = nil
	resp.Actions.Backup = allowed
	cnpgApplyOperatorGuard(resp)
	if !resp.Actions.Backup.Allowed {
		t.Error("an unknown webhook state must not block")
	}
}

func TestCNPGOperatorLeadingPod(t *testing.T) {
	held := CNPGOperatorLeader{ReadSource: integration.ReadSource{State: cnpgReadOK}, HolderPod: "op-1", HolderIsCurrentPod: true}
	if got := cnpgOperatorLeadingPod(held); got != "op-1" {
		t.Errorf("held = %q", got)
	}
	expired := held
	expired.Stale = true
	if got := cnpgOperatorLeadingPod(expired); got != "" {
		t.Errorf("an expired holder is labelled leader: %q", got)
	}
	gone := held
	gone.HolderIsCurrentPod = false
	if got := cnpgOperatorLeadingPod(gone); got != "" {
		t.Errorf("a holder that is no current Pod is labelled leader: %q", got)
	}
}

func TestCNPGOperatorFactsReadOutlivesTheCaller(t *testing.T) {
	type key struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key{}, "alice"))
	detached, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Minute)
	defer cancel()
	cancelParent()
	if err := detached.Err(); err != nil {
		t.Fatalf("a caller hanging up cancelled the shared operator read: %v", err)
	}
	if detached.Value(key{}) != "alice" {
		t.Error("the caller's identity was not kept")
	}
}

func TestCNPGOperatorLeadingHidesRawLeaseErrors(t *testing.T) {
	leader := CNPGOperatorLeader{ReadSource: integration.ReadSource{State: cnpgReadError, Reason: `Get "https://127.0.0.1:55484/apis/coordination.k8s.io/v1/namespaces/cnpg-system/leases/db9c8771.cnpg.io": context canceled`}}
	got, reason := cnpgOperatorLeading(operatorDeployment(1, 1), leader)
	if got != nil || reason != "couldn't read its leader lease" {
		t.Errorf("leading = %v, reason = %q", got, reason)
	}
	if plain, ok := cnpgTransportSentence(errors.New(leader.Reason), 0, time.Second); !ok || strings.Contains(plain, "127.0.0.1") {
		t.Errorf("transport sentence = %q (%v)", plain, ok)
	}
}

func TestOperatorFactsExplainUnreadDeployments(t *testing.T) {
	for _, tc := range []struct {
		state  string
		reason string
	}{
		{integration.KindCoverageSyncing, "still syncing"},
		{integration.KindCoverageError, "could not load Deployments"},
		{integration.KindCoverageUncached, "outside Radar's cache scope"},
		{integration.KindCoverageDenied, "cannot list Deployments"},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			reader := Reader{
				Clients: ReadClients{Typed: k8sfake.NewSimpleClientset()},
				Observations: Observations{
					Cache:         &k8s.ResourceCache{},
					OperatorScope: func(context.Context) []string { return nil },
					TypedScope: func(context.Context, *k8s.ResourceCache, []string, string, string) (integration.KindAccess, []string) {
						return integration.KindAccess{State: tc.state}, nil
					},
				},
				Access: Access{Permission: func(context.Context, auth.Grant) string { return integration.PermissionDenied }},
			}
			facts := reader.readCNPGOperatorFacts(context.Background())
			lease := reader.hAOperatorLease(context.Background(), context.Background(), HAClients{}, &k8s.ResourceCache{}, time.Now())
			leaseReason := tc.reason
			if tc.state == integration.KindCoverageSyncing {
				leaseReason = "still loading"
			}
			if tc.state == integration.KindCoverageUncached {
				leaseReason = "does not watch"
			}
			if tc.state == integration.KindCoverageDenied {
				leaseReason = "not visible to you"
			}
			if !strings.Contains(lease.Reason, leaseReason) {
				t.Fatalf("operator lease lost cause: %+v, want %q", lease, leaseReason)
			}

			if !strings.Contains(facts.deploymentsUnknown, tc.reason) {
				t.Fatalf("unread Deployment reason = %q, want %q", facts.deploymentsUnknown, tc.reason)
			}
		})
	}
}
