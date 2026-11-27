package k8s

import (
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

const inlineCAPIKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: https://10.0.0.1:6443
    certificate-authority-data: Y2E=
contexts:
- name: workload-admin@workload
  context: {cluster: workload, user: workload-admin}
current-context: workload-admin@workload
users:
- name: workload-admin
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
- name: token-user
  user:
    token: abc
`

func TestUntrustedKubeconfigReasons(t *testing.T) {
	cases := []struct {
		name string
		user string
		cert string
		want string
	}{
		{name: "inline credentials", want: ""},
		{name: "exec", user: "exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh, args: [-c, id]}", want: `user "extra" runs the command "/bin/sh" to log in`},
		{name: "auth-provider", user: "auth-provider: {name: oidc, config: {idp-issuer-url: https://idp.example}}", want: `user "extra" uses the "oidc" auth-provider plugin`},
		{name: "token file", user: "tokenFile: /home/op/.config/gcloud/token", want: `user "extra" reads its token from the local file "/home/op/.config/gcloud/token"`},
		{name: "client certificate file", user: "client-certificate: /home/op/cert.pem", want: `user "extra" reads its client certificate from the local file "/home/op/cert.pem"`},
		{name: "client key file", user: "client-key: /home/op/key.pem", want: `user "extra" reads its client key from the local file "/home/op/key.pem"`},
		{name: "certificate authority file", cert: "/etc/ssl/ca.pem", want: `cluster "extra" reads its certificate authority from the local file "/etc/ssl/ca.pem"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := inlineCAPIKubeconfig
			// The unsafe entries sit on a user and cluster that no current
			// context uses: every entry in the file gets registered.
			if tc.user != "" {
				data += "- name: extra\n  user:\n    " + tc.user + "\n"
			}
			if tc.cert != "" {
				data = strings.Replace(data, "contexts:", "- name: extra\n  cluster:\n    server: https://10.0.0.2:6443\n    certificate-authority: "+tc.cert+"\ncontexts:", 1)
			}
			cfg, err := clientcmd.Load([]byte(data))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := strings.Join(UntrustedKubeconfigReasons(cfg), "; ")
			if got != tc.want {
				t.Fatalf("reasons = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMergeAndSwitchContextRefusesExecBeforeWritingAnything(t *testing.T) {
	data := inlineCAPIKubeconfig + "- name: extra\n  user:\n    exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh}\n"

	clientMu.Lock()
	prevCAPI := capiKubeconfigs
	capiKubeconfigs = map[string]string{}
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		capiKubeconfigs = prevCAPI
		clientMu.Unlock()
	})

	_, path, created, err := MergeAndSwitchContext([]byte(data), "workload-admin@workload", "binding")
	if err == nil || !strings.Contains(err.Error(), `runs the command "/bin/sh"`) {
		t.Fatalf("err = %v, want refusal naming the command", err)
	}
	if path != "" || created {
		t.Fatalf("path = %q, created = %v; want nothing written", path, created)
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if len(capiKubeconfigs) != 0 {
		t.Fatalf("capiKubeconfigs = %v, want no registration", capiKubeconfigs)
	}
}
