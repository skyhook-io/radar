package cnpg

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestOperatorMemoRechecksCallerGrants(t *testing.T) {
	policy := admissionv1.Fail
	typed := k8sfake.NewSimpleClientset(cnpgOperatorDeployment(), &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: cnpgValidatingWebhookConfig},
		Webhooks:   []admissionv1.ValidatingWebhook{{Name: "vcluster.cnpg.io", FailurePolicy: &policy, ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "cnpg-system", Name: "webhook"}}}},
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: typed, ResourceTypes: map[string]bool{"deployments": true}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	reader := newTestReader(nil)
	reader.Identity = t.Name()
	reader.Clients.Typed = typed
	reader.Observations.Cache = &k8s.ResourceCache{ResourceCache: core}
	reader.Observations.OperatorScope = func(context.Context) []string { return nil }
	reader.Observations.TypedScope = func(context.Context, *k8s.ResourceCache, []string, string, string) (integration.KindAccess, []string) {
		return integration.AccessFromScope(nil, false), nil
	}
	denied := false
	checks := 0
	reader.Access.Permission = func(_ context.Context, g auth.Grant) string {
		if g == cnpgGrantGetValidatingWH {
			checks++
			if denied {
				return integration.PermissionDenied
			}
		}
		return integration.PermissionAllowed
	}
	ctx := context.Background()
	first := reader.OperatorStatus(ctx, []string{"db"}).Namespaces["db"]
	if first.WebhookRejects == nil || !*first.WebhookRejects {
		t.Fatalf("first webhook verdict=%+v", first)
	}
	before := len(typed.Actions())
	reader.OperatorStatus(ctx, []string{"db"})
	if len(typed.Actions()) != before {
		t.Fatal("unchanged caller did not reuse operator memo")
	}
	if checks < 2 {
		t.Fatal("warm memo skipped caller's webhook grant")
	}
	denied = true
	after := reader.OperatorStatus(ctx, []string{"db"}).Namespaces["db"]
	if after.WebhookRejects != nil || after.Unknown == "" {
		t.Fatalf("memo disclosed revoked webhook facts: %+v", after)
	}
}
