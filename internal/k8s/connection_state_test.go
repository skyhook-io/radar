package k8s

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMarkDisconnectedIfClusterUnreachable(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})

	message := `failed to list helm releases: Kubernetes cluster unreachable: Get "https://127.0.0.1:64287/version": dial tcp 127.0.0.1:64287: connect: connection refused`
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false")
	}

	got := GetConnectionStatus()
	if got.State != StateDisconnected {
		t.Fatalf("state = %q, want %q", got.State, StateDisconnected)
	}
	if got.Context != "kind-demo" {
		t.Fatalf("context = %q, want kind-demo", got.Context)
	}
	if got.ClusterName != "demo" {
		t.Fatalf("clusterName = %q, want demo", got.ClusterName)
	}
	if got.Error != message {
		t.Fatalf("error = %q, want original message", got.Error)
	}
	if got.ErrorType != "network" {
		t.Fatalf("errorType = %q, want network", got.ErrorType)
	}

	callbacks := 0
	probeCalls := 0
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error {
		probeCalls++
		return nil
	}
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})
	OnConnectionChange(func(ConnectionStatus) {
		callbacks++
	})
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for existing disconnected state")
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls = %d, want 0 for unchanged disconnected state", probeCalls)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0 for unchanged disconnected state", callbacks)
	}
}

func TestMarkDisconnectedIfClusterUnreachableIgnoresRecoveredCluster(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error { return nil }
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})

	message := `failed to list helm releases: Kubernetes cluster unreachable: Get "https://127.0.0.1:64287/version": dial tcp 127.0.0.1:64287: connect: connection refused`
	if MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned true after liveness probe recovered")
	}

	got := GetConnectionStatus()
	if got.State != StateConnected {
		t.Fatalf("state = %q, want %q", got.State, StateConnected)
	}
}

func TestMarkDisconnectedIfClusterUnreachableHandlesRawHelmTransportError(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error { return errors.New("still unreachable") }
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})

	message := `failed to list helm releases: Get "https://127.0.0.1:64287/api/v1/secrets": dial tcp 127.0.0.1:64287: connect: connection refused`
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for raw Helm transport error")
	}

	got := GetConnectionStatus()
	if got.State != StateDisconnected {
		t.Fatalf("state = %q, want %q", got.State, StateDisconnected)
	}
	if got.Error != message {
		t.Fatalf("error = %q, want original message", got.Error)
	}
	if got.ErrorType != "network" {
		t.Fatalf("errorType = %q, want network", got.ErrorType)
	}
}

func TestMarkDisconnectedIfClusterUnreachablePreservesTimeoutClassification(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error { return errors.New("still unreachable") }
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})

	message := `failed to list helm releases: Get "https://127.0.0.1:64287/api/v1/secrets": context deadline exceeded`
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for raw Helm timeout error")
	}

	got := GetConnectionStatus()
	if got.State != StateDisconnected {
		t.Fatalf("state = %q, want %q", got.State, StateDisconnected)
	}
	if got.ErrorType != "timeout" {
		t.Fatalf("errorType = %q, want timeout", got.ErrorType)
	}
}

func TestMarkDisconnectedIfClusterUnreachableIgnoresNonClusterNetworkErrors(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "kind-demo"})

	if MarkDisconnectedIfClusterUnreachable(`failed to update chart repository: Get "https://charts.example.test/index.yaml": dial tcp: no such host`) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned true for chart repository network error")
	}

	got := GetConnectionStatus()
	if got.State != StateConnected {
		t.Fatalf("state = %q, want %q", got.State, StateConnected)
	}
}

func withContextExecAuth(t *testing.T, enabled bool) {
	t.Helper()
	prev := SetTestContextUsesExec(enabled)
	t.Cleanup(func() {
		SetTestContextUsesExec(prev)
	})
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      string
		execAuth bool
		want     string
	}{
		{
			name: "rbac forbidden",
			err:  `deployments.apps is forbidden: User "alice" cannot list resource`,
			want: "rbac",
		},
		{
			name: "aws sso expired",
			err:  "SSOProviderInvalidToken: the SSO session has expired or is invalid; run aws sso login",
			want: "auth",
		},
		{
			name: "client-go exec credential failure is auth",
			err:  `failed to connect to cluster: Get "https://example.eks.amazonaws.com/version": getting credentials: exec: executable aws failed with exit code 255`,
			want: "auth",
		},
		{
			name: "plain context deadline is timeout without exec auth",
			err:  "failed to connect to cluster: context deadline exceeded",
			want: "timeout",
		},
		{
			name: "cluster unreachable context deadline is timeout without exec auth",
			err:  "failed to connect to cluster: cluster unreachable: context deadline exceeded",
			want: "timeout",
		},
		{
			name:     "exec auth context deadline stays timeout",
			err:      "failed to connect to cluster: context deadline exceeded",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth plugin timeout wrapper stays timeout",
			err:      "failed to connect to cluster: auth plugin timeout: context deadline exceeded",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth cluster unreachable context deadline stays timeout",
			err:      "failed to connect to cluster: cluster unreachable: context deadline exceeded",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth cluster unreachable io timeout stays timeout",
			err:      "failed to connect to cluster: cluster unreachable: i/o timeout",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth read io timeout stays timeout",
			err:      "read tcp 10.0.0.1:443: i/o timeout",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth tls handshake timeout stays timeout",
			err:      "net/http: TLS handshake timeout",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth connection refused is network",
			err:      "failed to connect to cluster: dial tcp 127.0.0.1:6443: connect: connection refused",
			execAuth: true,
			want:     "network",
		},
		{
			name:     "exec auth connection reset is network",
			err:      "failed to connect to cluster: read tcp 10.0.0.1:443: connection reset by peer",
			execAuth: true,
			want:     "network",
		},
		{
			name: "credentials word outside auth phrase is unknown",
			err:  "loaded kubeconfig credentials-cache metadata",
			want: "unknown",
		},
		{
			name: "missing kubeconfig is config",
			err:  "failed to build kubeconfig from /Users/alice/.kube/config: invalid configuration: no configuration has been provided, try setting KUBERNETES_MASTER environment variable",
			want: "config",
		},
		{
			name: "no context configured is config",
			err:  "no context configured",
			want: "config",
		},
		{
			name: "missing current context is config",
			err:  `context was not found for specified context: prod`,
			want: "config",
		},
		{
			name: "mtls proxy rejecting client cert is tls",
			err:  `failed to connect to cluster: Get "https://10.0.0.1:6443/version": remote error: tls: bad certificate`,
			want: "tls",
		},
		{
			name: "mtls proxy requiring client cert is tls",
			err:  `Get "https://10.0.0.1:6443/version": remote error: tls: certificate required`,
			want: "tls",
		},
		{
			name: "mtls proxy unknown certificate is tls",
			err:  `Get "https://teleport.example.com/version": remote error: tls: unknown certificate authority`,
			want: "tls",
		},
		{
			name: "oidc refresh token expired is auth",
			err:  `Get "https://api.example.com/version": failed to refresh token: oauth2: cannot fetch token: 400 Bad Request Response: {"error":"invalid_grant","error_description":"Token is not active"}`,
			want: "auth",
		},
		{
			name: "oidc no refresh token is auth",
			err:  "No valid id-token, and cannot refresh without refresh-token",
			want: "auth",
		},
		{
			name: "exec plugin cert path without wrapper is auth",
			err:  `Get "https://teleport.example.com/version": exec: executable tsh failed with exit code 1`,
			want: "auth",
		},
		{
			name: "unreadable token file is auth",
			err:  `failed to read token file "/var/run/secrets/kubernetes.io/serviceaccount/token": open /var/run/secrets/kubernetes.io/serviceaccount/token: no such file or directory`,
			want: "auth",
		},
		{
			name: "missing auth provider plugin is config",
			err:  `no Auth Provider found for name "oidc"`,
			want: "config",
		},
		{
			name: "unknown",
			err:  "something novel happened",
			want: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withContextExecAuth(t, tt.execAuth)
			if got := ClassifyError(errors.New(tt.err)); got != tt.want {
				t.Fatalf("ClassifyError(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyErrorTypedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "typed unauthorized is auth",
			err:  apierrors.NewUnauthorized("token expired"),
			want: "auth",
		},
		{
			name: "typed forbidden is rbac",
			err:  apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied")),
			want: "rbac",
		},
		{
			name: "typed server timeout is timeout",
			err:  apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "list", 1),
			want: "timeout",
		},
		{
			name: "typed request timeout is timeout",
			err:  apierrors.NewTimeoutError("server slow", 1),
			want: "timeout",
		},
		{
			name: "context deadline is timeout",
			err:  context.DeadlineExceeded,
			want: "timeout",
		},
		{
			name: "dns error is network",
			err:  &url.Error{Op: "Get", URL: "https://cluster.example", Err: &net.DNSError{Err: "no such host", Name: "cluster.example"}},
			want: "network",
		},
		{
			name: "url-wrapped unrecognized roundtripper failure stays unknown",
			// http.Client wraps EVERYTHING a RoundTripper returns in url.Error,
			// and the RoundTripper chain is the credential chain — an unknown
			// credential failure must not earn a confident "network" verdict.
			err:  &url.Error{Op: "Get", URL: "https://cluster.example/version", Err: errors.New("novel credential provider failure")},
			want: "unknown",
		},
		{
			name: "url-wrapped exec credential failure is auth",
			err: fmt.Errorf("failed to connect to cluster: %w", &url.Error{
				Op:  "Get",
				URL: "https://example.eks.amazonaws.com/version",
				Err: errors.New("getting credentials: exec: executable aws failed with exit code 255"),
			}),
			want: "auth",
		},
		{
			name: "url-wrapped aws sso expiry is auth",
			err: fmt.Errorf("failed to connect to cluster: %w", &url.Error{
				Op:  "Get",
				URL: "https://example.eks.amazonaws.com/version",
				Err: errors.New("SSOProviderInvalidToken: the SSO session has expired or is invalid; run aws sso login"),
			}),
			want: "auth",
		},
		{
			name: "wrapped auth plugin deadline stays timeout",
			err:  fmt.Errorf("auth plugin timeout: %w", context.DeadlineExceeded),
			want: "timeout",
		},
		{
			name: "tls unknown authority is tls",
			err: &url.Error{
				Op:  "Get",
				URL: "https://cluster.example/version",
				Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
			},
			want: "tls",
		},
		{
			name: "pointer tls unknown authority is tls",
			err: &url.Error{
				Op:  "Get",
				URL: "https://cluster.example/version",
				Err: &x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
			},
			want: "tls",
		},
		{
			name: "stringified x509 cluster unreachable is tls",
			err:  errors.New(`Kubernetes cluster unreachable: Get "https://cluster.example/version": tls: failed to verify certificate: x509: certificate signed by unknown authority`),
			want: "tls",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err); got != tt.want {
				t.Fatalf("ClassifyError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyErrorNil(t *testing.T) {
	if got := ClassifyError(nil); got != "" {
		t.Fatalf("ClassifyError(nil) = %q, want empty", got)
	}
}
