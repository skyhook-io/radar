package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"strings"
	"testing"
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

func TestStrimziEvidenceRequiresExactCRRead(t *testing.T) {
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
	if strimziEvidenceAccess(ctx)(ref) {
		t.Fatal("namespace access exposed denied connector")
	}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "app", true)
	if !strimziEvidenceAccess(ctx)(ref) {
		t.Fatal("authorized connector omitted")
	}
	ref.Namespace = "other"
	if strimziEvidenceAccess(ctx)(ref) {
		t.Fatal("namespace restriction bypassed")
	}
}
