package server

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func operatorDeployment(replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cnpg-system", Name: "cnpg-controller-manager"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestCNPGOperatorLeading(t *testing.T) {
	ok := CNPGOperatorLeader{CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadOK}}
	stale := ok
	stale.Stale = true
	denied := CNPGOperatorLeader{CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadDenied, Grant: "get leases in cnpg-system"}}
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
			CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadOK},
			Webhooks:         []CNPGOperatorWebhook{{Name: "vcluster.cnpg.io", FailurePolicy: policy, Service: "cnpg-system/cnpg-webhook-service"}},
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
	denied := []CNPGOperatorWebhookConfig{{Kind: "ValidatingWebhookConfiguration", Name: "x", CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadDenied, Grant: "get validatingwebhookconfigurations"}}}
	if got, _, unknown := cnpgWebhookRejects(denied, nil); got != nil || !strings.Contains(unknown, "get validatingwebhookconfigurations") {
		t.Errorf("denied config: %v %q", got, unknown)
	}
	absent := []CNPGOperatorWebhookConfig{{Kind: "ValidatingWebhookConfiguration", Name: "x", CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadNotFound}}}
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
	f = cnpgOperatorFacts{deploymentsUnknown: "Radar cannot list Deployments in every namespace"}
	if v := f.verdict("pg"); v.State != cnpgOperatorUnknown || v.Unknown == "" {
		t.Errorf("no visible operator = %+v", v)
	}
}

func TestCNPGApplyOperatorGuard(t *testing.T) {
	allowed := CNPGActionCapability{Allowed: true, Permission: cnpgPermAllowed}
	refused := CNPGActionCapability{Reason: "The cluster is hibernated", Permission: cnpgPermAllowed}
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
	held := CNPGOperatorLeader{CNPGReadCoverage: CNPGReadCoverage{State: cnpgReadOK}, HolderPod: "op-1", HolderIsCurrentPod: true}
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
