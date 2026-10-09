package auth

import (
	"encoding/json"
	"testing"
)

func TestGrantString(t *testing.T) {
	for _, c := range []struct {
		grant Grant
		want  string
	}{
		{Grant{Verb: "patch", Group: "postgresql.cnpg.io", Resource: "clusters", Subresource: "status", Namespace: "pg"}, "patch clusters/status (postgresql.cnpg.io) in namespace pg"},
		{Grant{Verb: "create", Group: "postgresql.cnpg.io", Resource: "backups", Namespace: "db"}, "create backups (postgresql.cnpg.io) in namespace db"},
		{Grant{Verb: "get", Resource: "pods", Subresource: "proxy", Namespace: "pgrt"}, "get pods/proxy in namespace pgrt"},
		{Grant{Verb: "get", Resource: "pods", Subresource: "proxy"}, "get pods/proxy cluster-wide"},
		{Grant{Verb: "get", Resource: "nodes"}, "get nodes cluster-wide"},
		{Grant{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "mutatingwebhookconfigurations"}, "get mutatingwebhookconfigurations.admissionregistration.k8s.io cluster-wide"},
	} {
		if got := c.grant.String(); got != c.want {
			t.Errorf("%+v.String() = %q, want %q", c.grant, got, c.want)
		}
	}
}

func TestGrantInBindsWithoutMutating(t *testing.T) {
	template := Grant{Verb: "list", Resource: "pods"}
	bound := template.In("pg")
	if template.Namespace != "" || bound.Namespace != "pg" || bound.In("").Namespace != "" {
		t.Errorf("template=%+v bound=%+v", template, bound)
	}
}

func TestGrantJSON(t *testing.T) {
	b, err := json.Marshal(Grant{Verb: "get", Resource: "pods", Subresource: "proxy", Namespace: "pg"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"verb":"get","resource":"pods","subresource":"proxy","namespace":"pg"}` {
		t.Errorf("namespaced = %s", got)
	}
	b, _ = json.Marshal(Grant{Verb: "patch", Group: "postgresql.cnpg.io", Resource: "clusters"})
	if got := string(b); got != `{"verb":"patch","group":"postgresql.cnpg.io","resource":"clusters"}` {
		t.Errorf("cluster-wide = %s", got)
	}
}
