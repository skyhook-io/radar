package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCorefileRules(t *testing.T) {
	cases := []struct {
		name     string
		corefile string
		want     []corefileRule
	}{
		{
			name: "one-line rules keep their own line number",
			corefile: `.:53 {
    errors
    rewrite name foo.example.com foo.default.svc.cluster.local
    kubernetes cluster.local
    rewrite stop type ANY HINFO
}`,
			want: []corefileRule{
				{line: 3, text: "rewrite name foo.example.com foo.default.svc.cluster.local"},
				{line: 5, text: "rewrite stop type ANY HINFO"},
			},
		},
		{
			name: "a block rule runs to its closing brace and {1} is not a brace",
			corefile: `.:53 {
    rewrite stop {
        name regex (.*)-(us-west-1)\.example\.org {1}.service.{2}.consul
        answer name (.*)\.service\.(us-west-1)\.consul {1}-{2}.example.org
    }
    forward . /etc/resolv.conf
}`,
			want: []corefileRule{{line: 2, text: `rewrite stop {
name regex (.*)-(us-west-1)\.example\.org {1}.service.{2}.consul
answer name (.*)\.service\.(us-west-1)\.consul {1}-{2}.example.org
}`}},
		},
		{
			name: "quoted braces and a commented brace do not open or close a block",
			corefile: `.:53 {
    template IN A example.com {
        match "^(?:[^.]+\.){4,}example\.com\.$"
        answer "{{ .Name }} 60 IN A 192.0.2.1" # not a block {
        fallthrough
    }
    cache 30
}`,
			want: []corefileRule{{line: 2, text: `template IN A example.com {
match "^(?:[^.]+\.){4,}example\.com\.$"
answer "{{ .Name }} 60 IN A 192.0.2.1" # not a block {
fallthrough
}`}},
		},
		{
			name: "only directives directly inside a server block are rules",
			corefile: `rewrite name top.level ignored
.:53 {
    kubernetes cluster.local {
        rewrite is-not-a-directive-here
    }
    hosts {
        10.0.0.1 template
    }
}
# rewrite name commented.out ignored`,
			want: nil,
		},
		{
			name: "a second server block is read too",
			corefile: `example.org:53 {
    forward . 10.0.0.1
}
.:53 {
    rewrite name a.example b.example
}`,
			want: []corefileRule{{line: 5, text: "rewrite name a.example b.example"}},
		},
		{
			name: "a block that never closes is marked truncated",
			corefile: `.:53 {
    template IN A example.com {
        rcode NXDOMAIN`,
			want: []corefileRule{{line: 2, text: "template IN A example.com {\nrcode NXDOMAIN", truncated: true}},
		},
		{
			name: "a comment attached to a brace does not hide the block",
			corefile: `.:53 {#dns
    rewrite name a.example b.example#old alias
    template IN A example.com {#block
        rcode NXDOMAIN
    }#end
    rewrite name c.example d.example
}`,
			want: []corefileRule{
				{line: 2, text: "rewrite name a.example b.example#old alias"},
				{line: 3, text: "template IN A example.com {#block\nrcode NXDOMAIN\n}#end"},
				{line: 6, text: "rewrite name c.example d.example"},
			},
		},
		{
			name: "quoted text may span lines and hold braces",
			corefile: `.:53 {
    template IN TXT example.com {
        answer "{{ .Name }} 60 IN TXT \"a {
}\""
    }
    rewrite name a.example b.example
}`,
			want: []corefileRule{
				{line: 2, text: "template IN TXT example.com {\nanswer \"{{ .Name }} 60 IN TXT \\\"a {\n}\\\"\"\n}"},
				{line: 6, text: "rewrite name a.example b.example"},
			},
		},
		{
			name:     "windows line endings",
			corefile: ".:53 {\r\n    rewrite name a.example b.example\r\n}\r\n",
			want:     []corefileRule{{line: 2, text: "rewrite name a.example b.example"}},
		},
		{
			name: "a server block that is never closed still yields its rules whole",
			corefile: `.:53 {
    rewrite name a.example b.example`,
			want: []corefileRule{{line: 2, text: "rewrite name a.example b.example"}},
		},
		{
			name: "a single server block written without braces",
			corefile: `.:53
rewrite name a.example b.example
kubernetes cluster.local {
    rewrite is-not-a-directive-here
}
template IN A example.com {
    rcode NXDOMAIN
}`,
			want: []corefileRule{
				{line: 2, text: "rewrite name a.example b.example"},
				{line: 6, text: "template IN A example.com {\nrcode NXDOMAIN\n}"},
			},
		},
		{
			name: "addresses over two lines, then no braces",
			corefile: `example.org,
example.net
rewrite name a.example b.example`,
			want: []corefileRule{{line: 3, text: "rewrite name a.example b.example"}},
		},
		{
			name: "the opening brace may sit on the line after the addresses",
			corefile: `.:53
{
    rewrite name a.example b.example
}`,
			want: []corefileRule{{line: 3, text: "rewrite name a.example b.example"}},
		},
		{
			name: "the first directive may follow the opening brace on its line",
			corefile: `.:53 { rewrite name a.example b.example
    template IN A example.com {
        rcode NXDOMAIN
    }
}`,
			want: []corefileRule{
				{line: 1, text: ".:53 { rewrite name a.example b.example"},
				{line: 2, text: "template IN A example.com {\nrcode NXDOMAIN\n}"},
			},
		},
		{
			name: "a server without braces after a snippet",
			corefile: `(common) {
    errors
}
.:53
rewrite name a.example b.example
import common`,
			want: []corefileRule{{line: 5, text: "rewrite name a.example b.example"}},
		},
		{name: "empty", corefile: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := corefileRules(tc.corefile)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rules %+v, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("rule %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCorefileRulesCapsLongText(t *testing.T) {
	long := "rewrite name regex " + strings.Repeat("a", 500) + " b.example"
	got := corefileRules(".:53 {\n    " + long + "\n}")
	if len(got) != 1 || !got[0].truncated || len(got[0].text) != maxCoreDNSRuleChars {
		t.Fatalf("got %+v, want one rule cut to %d chars and marked truncated", got, maxCoreDNSRuleChars)
	}
}

func TestCoreDNSRuleExcerptsOrderCapAndSource(t *testing.T) {
	defer ResetTestState()

	many := ".:53 {\n"
	for _, n := range []string{"a", "b", "c", "d"} {
		many += "    rewrite name " + n + ".example " + n + ".default.svc.cluster.local\n"
	}
	many += "}\n"
	client := fake.NewClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "coredns-custom", Namespace: "kube-system"}, Data: map[string]string{
			"Corefile": ".:53 {\n    rewrite name x.example x.default.svc.cluster.local\n    rewrite name y.example y.default.svc.cluster.local\n}\n",
		}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"}, Data: map[string]string{"Corefile": many}},
		// Not CoreDNS by name, not in kube-system, and not under the Corefile key.
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "node-local-dns", Namespace: "kube-system"}, Data: map[string]string{"Corefile": ".:53 {\n    rewrite name n.example n.example.org\n}\n"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "other"}, Data: map[string]string{"Corefile": ".:53 {\n    rewrite name o.example o.example.org\n}\n"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "coredns-extra", Namespace: "kube-system"}, Data: map[string]string{"custom.override": "rewrite name z.example z.example.org\n"}},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	rules, omitted := CoreDNSRuleExcerpts(GetResourceCache())
	if len(rules) != MaxCoreDNSRuleExcerpts || omitted != 1 {
		t.Fatalf("got %d rules, %d omitted; want %d and 1: %+v", len(rules), omitted, MaxCoreDNSRuleExcerpts, rules)
	}
	var got []string
	for _, r := range rules {
		if r.Namespace != "kube-system" || r.Key != "Corefile" {
			t.Errorf("excerpt from the wrong source: %+v", r)
		}
		got = append(got, r.Name+":"+strings.Fields(r.Text)[2])
	}
	want := "coredns:a.example coredns:b.example coredns:c.example coredns:d.example coredns-custom:x.example"
	if strings.Join(got, " ") != want {
		t.Fatalf("order = %q, want %q", strings.Join(got, " "), want)
	}
	if rules[0].Line != 2 || rules[4].Line != 2 {
		t.Errorf("lines = %d, %d; want each ConfigMap's own line 2", rules[0].Line, rules[4].Line)
	}
}

// Default Corefiles that distributions ship must never produce an Issue, with
// or without rules in them.
func TestDefaultCorefilesProduceNoDetection(t *testing.T) {
	files, err := filepath.Glob("testdata/corefiles/*.Corefile")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Corefile fixtures: %v", err)
	}
	wantRules := map[string]int{"vcluster-default.Corefile": 1, "k3s-default.Corefile": 0, "kubeadm-default.Corefile": 0, "aks-style-templates.Corefile": 2}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			defer ResetTestState()
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			client := fake.NewClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
				Data:       map[string]string{"Corefile": string(data)},
			})
			if err := InitTestResourceCache(client); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}
			for _, p := range DetectProblems(GetResourceCache(), "") {
				if p.Kind == "ConfigMap" {
					t.Fatalf("default Corefile reported as a problem: %+v", p)
				}
			}
			rules, _ := CoreDNSRuleExcerpts(GetResourceCache())
			if want, ok := wantRules[filepath.Base(file)]; !ok || len(rules) != want {
				t.Fatalf("got %d rule excerpts %+v, want %d (known fixture: %v)", len(rules), rules, want, ok)
			}
		})
	}
}
