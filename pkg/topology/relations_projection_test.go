package topology

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func configFamilyGraph() *Topology {
	return &Topology{
		Nodes: []Node{
			{ID: "configmap/shop/settings", Kind: KindConfigMap, Name: "settings", Data: map[string]any{"namespace": "shop"}},
			{ID: "configmap/team/settings-mirror", Kind: KindConfigMap, Name: "settings-mirror", Data: map[string]any{"namespace": "team"}},
			{ID: "deployment/shop/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "shop"}},
		},
		Edges: []Edge{
			{ID: "cm-to-web", Source: "configmap/shop/settings", Target: "deployment/shop/web", Type: EdgeConfigures, Family: ConfigReferenceFamily},
			{ID: "cm-to-mirror", Source: "configmap/shop/settings", Target: "configmap/team/settings-mirror", Type: EdgeConfigures, Label: "Reflects to"},
		},
	}
}

func TestAnsweredFamiliesLeaveOtherFamiliesInTheSameField(t *testing.T) {
	topo := configFamilyGraph()
	all := GetRelationshipsWithOptions("ConfigMap", "shop", "settings", nil, topo, nil, nil, nil, RelationshipOptions{})
	if got := relationshipRefNames(all.Consumers); !slices.Contains(got, "Deployment shop/web") || !slices.Contains(got, "ConfigMap team/settings-mirror") {
		t.Fatalf("from the graph alone, consumers = %v", got)
	}
	answered := GetRelationshipsWithOptions("ConfigMap", "shop", "settings", nil, topo, nil, nil, nil, RelationshipOptions{Answered: []string{ConfigReferenceFamily}})
	if got := relationshipRefNames(answered.Consumers); !slices.Equal(got, []string{"ConfigMap team/settings-mirror"}) {
		t.Fatalf("with the family answered elsewhere, consumers = %v; want the mirror only", got)
	}
	ApplyConfigReferences(answered, resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}, relations.ConfigReferencesAnswer{},
		relations.ConfigConsumersAnswer{Consumers: []relations.ConfigConsumer{{Ref: resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}}}})
	if got := relationshipRefNames(answered.Consumers); !slices.Equal(got, []string{"ConfigMap team/settings-mirror", "Deployment shop/web"}) {
		t.Fatalf("after the cache's answer, consumers = %v", got)
	}
}

func relationshipRefNames(refs []ResourceRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Kind+" "+r.Namespace+"/"+r.Name)
	}
	return out
}

func TestApplyConfigReferencesListsWhatTheCallerMayReadAndExists(t *testing.T) {
	rel := &Relationships{ConfigRefs: []ResourceRef{{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}}}
	subject := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	gap := ownership.Gap{Kind: resourceid.GroupKind{Kind: "PersistentVolumeClaim"}, Namespace: "shop", Reason: readreason.KindSyncPending}
	ApplyConfigReferences(rel, subject, relations.ConfigReferencesAnswer{
		Declared: []relations.Declared{
			{Ref: resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}, State: relations.TargetPresent},
			{Ref: resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "gone"}, State: relations.TargetAbsent},
			{Ref: resourceid.Ref{Kind: "Secret", Namespace: "shop", Name: "db"}, State: relations.TargetWithheld},
			{Ref: resourceid.Ref{Kind: "PersistentVolumeClaim", Namespace: "shop", Name: "data"}, State: relations.TargetUnread, Gap: gap},
			{Ref: resourceid.Ref{Kind: "ServiceAccount", Namespace: "shop", Name: "web"}, State: relations.TargetPresent},
		},
		Coverage: relations.Coverage{NotChecked: []ownership.Gap{gap}, Narrowed: true},
	}, relations.ConfigConsumersAnswer{})
	if got := relationshipRefNames(rel.ConfigRefs); !slices.Equal(got, []string{"ConfigMap shop/settings"}) {
		t.Errorf("configRefs = %v; present and readable targets only, each once", got)
	}
	if rel.ServiceAccount == nil || rel.ServiceAccount.Name != "web" {
		t.Errorf("serviceAccount = %+v", rel.ServiceAccount)
	}
	c := rel.Coverage[ConfigReferenceFamily]
	if !c.Narrowed || len(c.NotChecked) != 1 || c.NotChecked[0].Kind != "PersistentVolumeClaim" || c.NotChecked[0].Reason != string(readreason.KindSyncPending) {
		t.Errorf("coverage = %+v", c)
	}
}

func TestCoverageOnTheWireSaysWhichFamiliesWereAnswered(t *testing.T) {
	encode := func(rel *Relationships) string {
		b, err := json.Marshal(rel)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := encode(&Relationships{}); strings.Contains(got, `"coverage"`) {
		t.Errorf("a graph-only answer sends coverage: %s", got)
	}
	complete := &Relationships{}
	ApplyConfigReferences(complete, resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}, relations.ConfigReferencesAnswer{}, relations.ConfigConsumersAnswer{})
	if got := encode(complete); !strings.Contains(got, `"coverage":{"configReferences":{}}`) {
		t.Errorf("a complete answer = %s; want the family listed with an empty entry", got)
	}
	unavailable := &Relationships{}
	RelationFamilyUnavailable(unavailable, ConfigReferenceFamily, string(readreason.ClusterConnecting))
	if got := encode(unavailable); !strings.Contains(got, `"configReferences":{"notChecked":[{"reason":"cluster_connecting"}]}`) {
		t.Errorf("an unanswered family = %s", got)
	}
}
