package meaningfulchanges

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

var compactBase = time.Date(2026, 10, 16, 4, 43, 24, 0, time.FixedZone("", 3*3600))

func plainCreate(kind, namespace, name string, offset time.Duration) issuesapi.RecentChange {
	apiVersion := "v1"
	if kind == "Deployment" {
		apiVersion = "apps/v1"
	}
	return issuesapi.RecentChange{
		Kind:           kind,
		APIVersion:     apiVersion,
		Namespace:      namespace,
		Name:           name,
		ChangeType:     "add",
		Summary:        "add",
		Timestamp:      compactBase.Add(offset).Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategoryLifecycle,
		RankReason:     lifecycleRankReason,
	}
}

func installRun(namespace string, n int, start time.Duration) []issuesapi.RecentChange {
	var out []issuesapi.RecentChange
	for i := 0; i < n; i++ {
		kind := "Service"
		if i%2 == 0 {
			kind = "Deployment"
		}
		out = append(out, plainCreate(kind, namespace, fmt.Sprintf("svc-%02d", i), start+time.Duration(i)*time.Second))
	}
	return out
}

var createdLine = regexp.MustCompile(`^\+(\d+)s ([^ /]+)/([^ /]+)(?: \(used by (.+)\))?$`)

// expandCreated rebuilds RecentChange rows from a CreatedObjects entry by the
// documented grammar, the way the web adapter does.
func expandCreated(t *testing.T, entry issuesapi.CreatedObjects) []issuesapi.RecentChange {
	t.Helper()
	first, err := time.Parse(time.RFC3339, entry.First)
	if err != nil {
		t.Fatalf("first %q: %v", entry.First, err)
	}
	var out []issuesapi.RecentChange
	for _, line := range entry.Objects {
		m := createdLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %q does not match the grammar", line)
		}
		offset, _ := strconv.Atoi(m[1])
		row := issuesapi.RecentChange{
			Kind:           m[2],
			APIVersion:     entry.APIVersions[m[2]],
			Namespace:      entry.Namespace,
			Name:           m[3],
			ChangeType:     "add",
			Summary:        "add",
			Timestamp:      first.Add(time.Duration(offset) * time.Second).Format(time.RFC3339),
			ChangeCategory: issuesapi.ChangeCategoryLifecycle,
			RankReason:     lifecycleRankReason,
		}
		if m[4] != "" {
			row.ConsumedBy = regexp.MustCompile(`, `).Split(m[4], -1)
		}
		out = append(out, row)
	}
	if len(out) != entry.Count {
		t.Fatalf("count = %d, objects = %d", entry.Count, len(out))
	}
	return out
}

func changesByKey(rows []issuesapi.RecentChange) map[string]issuesapi.RecentChange {
	out := map[string]issuesapi.RecentChange{}
	for _, r := range rows {
		out[r.Kind+"/"+r.Namespace+"/"+r.Name+"@"+r.Timestamp] = r
	}
	return out
}

func TestCompactCreatesIsLossless(t *testing.T) {
	in := installRun("shop", 8, 0)
	in[2].ConsumedBy = []string{"Deployment/a", "Deployment/b"}
	in[2].Kind = "ConfigMap"
	edit := issuesapi.RecentChange{
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "svc-00",
		ChangeType: "update", Summary: "env changed", Timestamp: compactBase.Add(5 * time.Minute).Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategorySpecConfig, RankReason: "desired-state or configuration field changed",
		Fields: []issuesapi.ChangeField{{Path: "spec.template.spec.containers[a].env[X]"}},
	}
	in = append([]issuesapi.RecentChange{edit}, in...)
	original := append([]issuesapi.RecentChange(nil), in...)

	rest, created := CompactCreates(in)
	if !reflect.DeepEqual(rest, []issuesapi.RecentChange{edit}) {
		t.Fatalf("rest = %+v, want only the edit", rest)
	}
	if len(created) != 1 || created[0].Count != 8 || created[0].First != original[1].Timestamp {
		t.Fatalf("created = %+v", created)
	}
	want := changesByKey(original[1:])
	got := changesByKey(expandCreated(t, created[0]))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded rows differ from the originals:\n got %+v\nwant %+v", got, want)
	}
}

func TestCompactCreatesKeepsFullRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*issuesapi.RecentChange)
	}{
		{"message summary", func(c *issuesapi.RecentChange) { c.Summary = "created by operator" }},
		{"recreated with diff", func(c *issuesapi.RecentChange) {
			c.ChangeCategory = issuesapi.ChangeCategorySpecConfig
			c.Fields = []issuesapi.ChangeField{{Path: "spec.selector"}}
		}},
		{"delete", func(c *issuesapi.RecentChange) { c.ChangeType = "delete"; c.Summary = "delete" }},
		{"helm row", func(c *issuesapi.RecentChange) { c.Source = "helm" }},
		{"application configuration flag", func(c *issuesapi.RecentChange) { c.ApplicationConfigurationChange = true }},
		{"different rank reason", func(c *issuesapi.RecentChange) { c.RankReason = "something else" }},
		{"cluster scoped", func(c *issuesapi.RecentChange) { c.Namespace = "" }},
		{"some consumers withheld", func(c *issuesapi.RecentChange) {
			c.Kind = "ConfigMap"
			c.ConsumedBy = []string{"Deployment/web"}
			c.ConsumedByWithheld = &issuesapi.Withheld{Count: 1, Reason: readreason.UserForbidden}
		}},
		{"all consumers withheld", func(c *issuesapi.RecentChange) {
			c.Kind = "ConfigMap"
			c.ConsumedByWithheld = &issuesapi.Withheld{Count: 2, Reason: readreason.UserForbidden}
		}},
	}
	for _, kind := range []string{"NetworkPolicy", "ResourceQuota", "LimitRange", "HorizontalPodAutoscaler", "PodDisruptionBudget", "ReferenceGrant", "Application", "Kustomization", "HelmRelease"} {
		kind := kind
		tests = append(tests, struct {
			name   string
			mutate func(*issuesapi.RecentChange)
		}{"standalone " + kind, func(c *issuesapi.RecentChange) { c.Kind = kind }})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := installRun("shop", 6, 0)
			tt.mutate(&in[3])
			special := in[3]
			rest, created := CompactCreates(in)
			if len(rest) != 1 || !reflect.DeepEqual(rest[0], special) {
				t.Fatalf("rest = %+v, want only %+v", rest, special)
			}
			if len(created) != 1 || created[0].Count != 5 {
				t.Fatalf("created = %+v, want the other 5 creates in one run (a full row does not split a run)", created)
			}
		})
	}
}

func TestCompactCreatesRunRule(t *testing.T) {
	t.Run("fewer than five stay rows", func(t *testing.T) {
		in := installRun("shop", 4, 0)
		rest, created := CompactCreates(in)
		if created != nil || !reflect.DeepEqual(rest, in) {
			t.Fatalf("rest=%+v created=%+v, want unchanged", rest, created)
		}
	})
	t.Run("gap over 60s splits runs and a lone late create stays a row", func(t *testing.T) {
		in := installRun("shop", 6, 0)
		late := plainCreate("Deployment", "shop", "rogue", 6*time.Second+61*time.Second)
		in = append(in, late)
		rest, created := CompactCreates(in)
		if len(created) != 1 || created[0].Count != 6 {
			t.Fatalf("created = %+v", created)
		}
		if !reflect.DeepEqual(rest, []issuesapi.RecentChange{late}) {
			t.Fatalf("rest = %+v, want the late create as a full row", rest)
		}
	})
	t.Run("gap of exactly 60s continues the run", func(t *testing.T) {
		in := installRun("shop", 5, 0)
		in = append(in, plainCreate("Service", "shop", "next", 4*time.Second+60*time.Second))
		_, created := CompactCreates(in)
		if len(created) != 1 || created[0].Count != 6 {
			t.Fatalf("created = %+v", created)
		}
	})
	t.Run("namespaces are separate entries ordered by first", func(t *testing.T) {
		in := append(installRun("b", 5, 0), installRun("a", 5, time.Second)...)
		rest, created := CompactCreates(in)
		if len(rest) != 0 || len(created) != 2 || created[0].Namespace != "b" || created[1].Namespace != "a" {
			t.Fatalf("rest=%+v created=%+v", rest, created)
		}
	})
	t.Run("zone change splits the run", func(t *testing.T) {
		in := installRun("shop", 10, 0)
		for i := 5; i < 10; i++ {
			at, _ := time.Parse(time.RFC3339, in[i].Timestamp)
			in[i].Timestamp = at.UTC().Format(time.RFC3339)
		}
		_, created := CompactCreates(in)
		if len(created) != 2 {
			t.Fatalf("created = %+v, want two runs", created)
		}
	})
	t.Run("kind with mixed apiVersions keeps full rows", func(t *testing.T) {
		in := installRun("shop", 9, 0)
		in[1].APIVersion = ""
		rest, created := CompactCreates(in)
		if len(created) != 1 || created[0].Count != 5 || len(rest) != 4 {
			t.Fatalf("rest=%+v created=%+v, want Services kept as rows", rest, created)
		}
		for _, r := range rest {
			if r.Kind != "Service" {
				t.Fatalf("unexpected row %+v", r)
			}
		}
	})
	t.Run("offsets across hours and order", func(t *testing.T) {
		var in []issuesapi.RecentChange
		for i := 0; i < 130; i++ {
			in = append(in, plainCreate("Service", "shop", fmt.Sprintf("s%03d", i), time.Duration(i)*time.Minute))
		}
		// Reverse to the ranked (newest first) order the handler passes in.
		for i, j := 0, len(in)-1; i < j; i, j = i+1, j-1 {
			in[i], in[j] = in[j], in[i]
		}
		_, created := CompactCreates(in)
		if len(created) != 1 || created[0].Objects[0] != "+0s Service/s000" || created[0].Objects[129] != "+7740s Service/s129" {
			t.Fatalf("created = %+v", created)
		}
	})
	t.Run("empty", func(t *testing.T) {
		rest, created := CompactCreates(nil)
		if rest != nil || created != nil {
			t.Fatalf("rest=%v created=%v", rest, created)
		}
	})
}

// sharedCreatedFixture is read by the web adapter test, which checks that
// expanding "compacted" reproduces "original" row for row and in order.
// Regenerate with UPDATE_CREATED_FIXTURE=1.
const sharedCreatedFixture = "../../web/src/components/diagnose/investigationEvidence/adapters/testdata/get-changes-created.json"

func TestCompactCreatesSharedFixture(t *testing.T) {
	var rows []issuesapi.RecentChange
	for i, name := range []string{"cart", "checkout", "frontend", "payment", "shipping", "catalog"} {
		rows = append(rows, plainCreate("Deployment", "shop", name, time.Duration(i)*time.Second))
		rows = append(rows, plainCreate("Service", "shop", name, time.Duration(i)*time.Second))
	}
	config := plainCreate("ConfigMap", "shop", "checkout-config", 3*time.Second)
	config.ConsumedBy = []string{"Deployment/checkout"}
	policy := plainCreate("NetworkPolicy", "shop", "deny-all", 4*time.Second)
	policy.APIVersion = "networking.k8s.io/v1"
	lone := plainCreate("Deployment", "shop", "debug", 10*time.Minute)
	deleted := plainCreate("Service", "shop", "legacy", 2*time.Second)
	deleted.ChangeType, deleted.Summary = "delete", "delete"
	edit := issuesapi.RecentChange{
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "checkout",
		ChangeType: "update", Summary: "env(checkout/CART_ADDR) changed",
		Timestamp:      compactBase.Add(12 * time.Minute).Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategorySpecConfig, RankReason: "desired-state or configuration field changed",
		Fields: []issuesapi.ChangeField{{Path: "spec.template.spec.containers[checkout].env[CART_ADDR]", OldValue: "cart:7070", NewValue: "localhost:7070"}},
	}
	status := issuesapi.RecentChange{
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "cart",
		ChangeType: "update", Summary: "ready: 0→1", Timestamp: compactBase.Add(30 * time.Second).Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategoryRuntimeStatus, RankReason: "status field changed",
		Fields: []issuesapi.ChangeField{{Path: "status.readyReplicas", OldValue: 0, NewValue: 1}},
	}
	rows = append(rows, config, policy, lone, deleted, edit, status)
	RankAndCap(&rows, 50)
	original := append([]issuesapi.RecentChange(nil), rows...)
	rest, created := CompactCreates(rows)
	if len(created) != 1 || created[0].Count != 13 {
		t.Fatalf("created = %+v, want one run of 13", created)
	}

	got, err := json.MarshalIndent(map[string]any{
		"original":  map[string]any{"changes": original},
		"compacted": map[string]any{"changes": rest, "created": created},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_CREATED_FIXTURE") != "" {
		if err := os.WriteFile(sharedCreatedFixture, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(sharedCreatedFixture)
	if err != nil {
		t.Fatalf("read %s (regenerate with UPDATE_CREATED_FIXTURE=1): %v", sharedCreatedFixture, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale; regenerate with UPDATE_CREATED_FIXTURE=1", sharedCreatedFixture)
	}
}
