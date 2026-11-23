package context

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const kagentSentinel = "KAGENT-LITERAL-SENTINEL"

func kagentObj(kind string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "api.kagent.dev/v1alpha3", "kind": kind,
		"metadata": map[string]any{"name": "x", "namespace": "agents", "generation": int64(1)},
		"spec":     spec,
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// Header and environment values can be credentials under any name; they never
// reach AI context at any level, while their names and Secret references do.
func TestKagentLiteralValuesNeverReachAIContext(t *testing.T) {
	objects := []*unstructured.Unstructured{
		kagentObj("RemoteMCPServer", map[string]any{"url": "https://mcp.example.com/", "headersFrom": []any{
			map[string]any{"name": "X-Api-Key", "value": kagentSentinel},
			map[string]any{"name": "Authorization", "valueFrom": map[string]any{"type": "Secret", "name": "gh", "key": "token"}},
		}}, nil),
		kagentObj("ModelConfig", map[string]any{"model": "gpt-4o", "defaultHeaders": map[string]any{"X-Api-Key": kagentSentinel}, "azureOpenAI": map[string]any{"azureAdToken": kagentSentinel}}, nil),
		kagentObj("Harness", map[string]any{"env": []any{map[string]any{"name": "MODE", "value": kagentSentinel}}}, nil),
		kagentObj("SandboxTemplate", map[string]any{"env": []any{map[string]any{"name": "MODE", "value": kagentSentinel}}}, nil),
	}
	for _, obj := range objects {
		for _, level := range []VerbosityLevel{LevelSummary, LevelDetail, LevelCompact} {
			raw, _ := json.Marshal(MinifyUnstructured(obj.DeepCopy(), level))
			if strings.Contains(string(raw), kagentSentinel) {
				t.Errorf("%s level %d leaks a literal value: %s", obj.GetKind(), level, raw)
			}
		}
	}
	raw, _ := json.Marshal(MinifyUnstructured(objects[0].DeepCopy(), LevelDetail))
	if !strings.Contains(string(raw), `"X-Api-Key"`) || !strings.Contains(string(raw), `"gh"`) {
		t.Errorf("header names and Secret references stay: %s", raw)
	}
	foreign := objects[2].DeepCopy()
	foreign.SetAPIVersion("example.com/v1")
	raw, _ = json.Marshal(MinifyUnstructured(foreign, LevelDetail))
	if !strings.Contains(string(raw), kagentSentinel) {
		t.Errorf("another group's Harness is not this redaction's to touch: %s", raw)
	}
}

// The audit's other carriers: URL user info and queries, artifact sources,
// credential flags, a discovery error quoting a URL, and ModelConfig's
// secretHash, which confirms a guessed key.
func TestKagentURLsArgsAndSecretHashNeverReachAIContext(t *testing.T) {
	cred := "https://user:" + kagentSentinel + "@host.example.com/v1?api_key=" + kagentSentinel
	objects := []*unstructured.Unstructured{
		kagentObj("ModelConfig", map[string]any{
			"openAI": map[string]any{"baseUrl": cred}, "ollama": map[string]any{"host": "u:" + kagentSentinel + "@ollama:11434"},
			"azureOpenAI": map[string]any{"azureEndpoint": cred}, "sapAICore": map[string]any{"authUrl": cred}, "foundry": map[string]any{"endpoint": cred},
		}, map[string]any{"secretHash": kagentSentinel}),
		kagentObj("ModelProviderConfig", map[string]any{"endpoint": cred}, nil),
		kagentObj("RemoteMCPServer", map[string]any{"url": cred}, map[string]any{"conditions": []any{
			map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": "Get \"" + cred + "\": 401"},
		}}),
		kagentObj("Harness", map[string]any{
			"workload":  map[string]any{"args": []any{"--api-key=" + kagentSentinel, "--token", kagentSentinel}},
			"substrate": map[string]any{"snapshotPolicy": map[string]any{"location": "s3://AKID:" + kagentSentinel + "@bucket/snap"}},
		}, nil),
		kagentObj("AgentTemplate", map[string]any{"skills": []any{map[string]any{"name": "s", "source": map[string]any{"git": map[string]any{"url": "https://" + kagentSentinel + "@git.example.com/x.git", "commit": "abc"}}}}}, nil),
		kagentObj("Agent", map[string]any{"template": map[string]any{"inline": map[string]any{"plugins": []any{map[string]any{"source": map[string]any{"bucket": map[string]any{"s3": map[string]any{"endpoint": cred}}}}}}}}, nil),
	}
	annotated := objects[0].DeepCopy()
	annotated.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"spec":{"defaultHeaders":{"k":"` + kagentSentinel + `"}}}`})
	objects = append(objects, annotated)
	for _, obj := range objects {
		for _, level := range []VerbosityLevel{LevelSummary, LevelDetail, LevelCompact} {
			raw, _ := json.Marshal(MinifyUnstructured(obj.DeepCopy(), level))
			if strings.Contains(string(raw), kagentSentinel) {
				t.Errorf("%s level %d leaks: %s", obj.GetKind(), level, raw)
			}
		}
	}
	raw, _ := json.Marshal(MinifyUnstructured(objects[2].DeepCopy(), LevelDetail))
	if !strings.Contains(string(raw), "host.example.com/v1") {
		t.Errorf("a URL keeps its host and path: %s", raw)
	}
}

func TestKagentAgentSummaryUsesKagentReading(t *testing.T) {
	agent := kagentObj("Agent", map[string]any{"harnessRef": map[string]any{"name": "default"}}, map[string]any{
		"observedGeneration": int64(1), "latestSuccessfulRevision": "a1b2c3d4e5f6a1b2c3d4e5f6",
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "observedGeneration": int64(1)},
			map[string]any{"type": "EgressPoliciesApplied", "status": "False", "reason": "EgressPolicyNotFound", "message": "agents/github: not found", "observedGeneration": int64(1)},
		},
	})
	s := summarizeUnstructured(agent)
	if s.Status != "Egress not applied" || s.Ready != "Ready to start sessions" || s.Issue != "EgressPolicyNotFound" || s.Version != "a1b2c3d4e5f6" {
		t.Fatalf("summary = %+v", s)
	}
	elastic := agent.DeepCopy()
	elastic.SetAPIVersion("agent.k8s.elastic.co/v1alpha1")
	if s := summarizeUnstructured(elastic); s.Status == "Egress not applied" {
		t.Fatalf("another group's Agent got kagent's reading: %+v", s)
	}
}

// MCP reads some dynamic objects through the typed path (Minify, not
// MinifyUnstructured); the CRD redaction applies there too.
func TestKagentRedactionOnTheTypedMinifyPath(t *testing.T) {
	server := kagentObj("RemoteMCPServer", map[string]any{"url": "http://user:FAKEPASS@mcp.example:9999/mcp?token=FAKEQUERY"}, map[string]any{"conditions": []any{
		map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": `Post "http://user:***@mcp.example:9999/mcp?token=FAKEQUERY": dial tcp`},
	}})
	for _, level := range []VerbosityLevel{LevelDetail, LevelCompact} {
		out, err := Minify(server.DeepCopy(), level)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "FAKEPASS") || strings.Contains(string(raw), "FAKEQUERY") {
			t.Errorf("level %d leaks: %s", level, raw)
		}
	}
}
