package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestStrimziDiagnoseDispatch(t *testing.T) {
	for _, kind := range []string{"KafkaConnector", "kafkaconnectors", "KafkaConnect", "kafkaconnects"} {
		if _, ok := strimziDiagnoseKind(kind); !ok {
			t.Fatalf("not supported %s", kind)
		}
		_, _, err := handleDiagnose(context.Background(), nil, diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: kind, Group: "collision.io", Namespace: "app", Name: "x"}})
		if err == nil || !strings.Contains(err.Error(), "invalid group") {
			t.Fatalf("group accepted: %v", err)
		}
	}
	if _, ok := strimziDiagnoseKind("Kafka"); ok {
		t.Fatal("broker is not Connect")
	}
}

func TestStrimziEvidenceFollowsNamespacedIssueReadRules(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkaconnectors"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KafkaConnectorList"})
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "KafkaConnector", Name: gvr.Resource, Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	ctx := withTestUserPerms(t, "reader", nil, []string{"app"})
	_, perms := resolveUserPerms(ctx)
	perms.SetCanI("get", gvr.Group, gvr.Resource, "app", false)
	ref := issues.Ref{Group: gvr.Group, Kind: "KafkaConnector", Namespace: "app", Name: "failed"}
	if !issueRelatedResourceAccess(ctx)(ref) {
		t.Fatal("curated connector finding did not follow namespace issue access")
	}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "app", true)
	if !issueRelatedResourceAccess(ctx)(ref) {
		t.Fatal("authorized connector omitted")
	}
	ref.Namespace = "other"
	if issueRelatedResourceAccess(ctx)(ref) {
		t.Fatal("namespace restriction bypassed")
	}
}

func TestStrimziDiagnoseOmitsParentConditionMessages(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	gvr := schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkaconnects"}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": "KafkaConnect",
		"metadata": map[string]any{"namespace": "alpha", "name": "connect", "generation": int64(1)},
		"spec":     map[string]any{"config": map[string]any{"password": "raw-condition-sentinel"}},
		"status":   map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "raw-condition-sentinel\nexception trace"}}},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KafkaConnectList"}, obj)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "KafkaConnect", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("fixture cache not synchronized")
	}
	result, _, err := handleDiagnose(context.Background(), nil, diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Group: gvr.Group, Kind: "KafkaConnect", Namespace: "alpha", Name: "connect"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := renderContent(result.Content)
	if strings.Contains(raw, "raw-condition-sentinel") || strings.Contains(raw, "exception trace") {
		t.Fatalf("raw data leaked: %s", raw)
	}
	var body struct {
		OperatorSnapshot struct {
			Readiness string `json:"readiness"`
		}
		Resource      map[string]any `json:"resource"`
		RelatedIssues []issues.Issue `json:"relatedIssues"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if body.OperatorSnapshot.Readiness != "not_ready" || body.Resource["spec"] != nil || body.Resource["status"] != nil || len(body.RelatedIssues) != 0 {
		t.Fatalf("minimal parent evidence: %s", raw)
	}
}
