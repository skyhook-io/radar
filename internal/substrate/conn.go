package substrate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	pfpkg "github.com/skyhook-io/radar/pkg/portforward"
)

// Where Substrate installs its API, as kubectl ate finds it.
const (
	SystemNamespace      = "ate-system"
	APIService           = "api"
	APIPort              = 443
	ClientServiceAccount = "ate-client"
	// APIServerName is the name on the API's serving certificate and the
	// audience of the tokens minted for it.
	APIServerName = APIService + "." + SystemNamespace + ".svc"
	// SharedPrincipal is who the shared identity authenticates as.
	SharedPrincipal = "system:serviceaccount:" + SystemNamespace + ":" + ClientServiceAccount

	serviceDNSSignerName = "servicedns.podcert.ate.dev/identity"
	liveBundleSelector   = "podcert.ate.dev/canarying=live"
	tokenLifetime        = time.Hour
	// tokenRefreshBefore re-mints a shared token well before it expires, so
	// an RPC in flight never carries one that lapses mid-call.
	tokenRefreshBefore = 10 * time.Minute
	maxRecvMessage     = 64 << 20
)

// Kube is the cluster connection Radar reads Substrate through.
type Kube struct {
	Client  kubernetes.Interface
	Config  *rest.Config
	Context string
	// Binding and Fingerprint identify the context and the cluster behind it
	// (k8s.ProfileTarget). Generation changes with every client rebuild and
	// context operation, so work captured under one is not used under another.
	Binding     string
	Fingerprint string
	Generation  string
	// Operation is the context operation the capture belongs to; it is
	// cancelled when that operation ends. Nil in tests that build a Kube.
	Operation context.Context
}

// conn is one gRPC client for one (context, settings).
type conn struct {
	key    string
	cc     *grpc.ClientConn
	client ateapi.ControlClient
	creds  *bearerCreds
	// forwardPort is the local port of Radar's port-forward; 0 when dialing
	// an endpoint directly.
	forwardPort int
	// endpoint is what the status line shows: the Service or the endpoint.
	endpoint string
}

// close closes the client and its port-forward, only while the forward is
// still the one it dialed: Radar has one Substrate forward, and a later dial
// may have replaced it.
func (c *conn) close() {
	if c == nil {
		return
	}
	if c.cc != nil {
		c.cc.Close()
	}
	if c.forwardPort != 0 {
		portforward.StopIfAddress(portforward.OwnerSubstrate, forwardAddress(c.forwardPort))
	}
}

func forwardAddress(port int) string { return fmt.Sprintf("http://localhost:%d", port) }

// alive reports whether the port-forward this conn dials is still up.
func (c *conn) alive() bool {
	if c.forwardPort == 0 {
		return true
	}
	info := portforward.GetConnectionInfo(portforward.OwnerSubstrate)
	return info.Connected && info.LocalPort == c.forwardPort
}

func connKey(k Kube, s Settings) string {
	data, _ := json.Marshal(struct {
		Binding, Fingerprint, Generation string
		Settings                         Settings
	}{k.Binding, k.Fingerprint, k.Generation, s})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// endpointLabel is how the status line names where Radar reads from.
func endpointLabel(s Settings) string {
	if s.Endpoint != "" {
		return s.Endpoint
	}
	return fmt.Sprintf("%s/%s:%d (port-forward)", SystemNamespace, APIService, APIPort)
}

// dial connects with s. caller is the token source of IdentitySharedAsCaller,
// nil for the other identities.
func dial(ctx context.Context, k Kube, s Settings, caller tokenSource) (*conn, error) {
	c := &conn{key: connKey(k, s), endpoint: endpointLabel(s)}
	target := s.Endpoint
	if target == "" {
		port, err := startForward(ctx, k)
		if err != nil {
			return nil, err
		}
		c.forwardPort = port
		target = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: APIServerName}
	if s.ServerName != "" {
		tlsCfg.ServerName = s.ServerName
	}
	// Per-RPC credentials require transport security, so the token is sent
	// only after the handshake verifies the server against these roots.
	if s.CAFile != "" {
		pool, err := caFilePool(s.CAFile)
		if err != nil {
			c.close()
			if s.Identity == IdentitySharedAsCaller {
				return nil, &TierError{Code: CodeTLS, Message: "Cannot read a PEM CA certificate from RADAR_SUBSTRATE_CA_FILE; check the installation's mounted CA."}
			}
			return nil, err
		}
		tlsCfg.RootCAs = pool
	} else {
		pool, err := clusterTrustPool(ctx, k.Client)
		if err != nil {
			c.close()
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}

	var source tokenSource
	switch s.Identity {
	case IdentitySharedAsCaller:
		if caller == nil {
			c.close()
			return nil, &TierError{Code: CodeUnauthenticated, Message: "no token was minted for this request"}
		}
		source = caller
	case IdentityShared:
		source = &sharedToken{client: k.Client}
	case IdentityTokenFile:
		source = &fileToken{path: s.TokenFile}
	default:
		c.close()
		return nil, &TierError{Code: CodeNotConfigured, Message: "choose who Radar reads Substrate as"}
	}
	c.creds = &bearerCreds{source: source}

	cc, err := grpc.NewClient("passthrough:///"+target,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(c.creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRecvMessage)),
	)
	if err != nil {
		c.close()
		return nil, &TierError{Code: CodeUnreachable, Message: "cannot set up the connection to Substrate's API: " + err.Error()}
	}
	c.cc = cc
	c.client = ateapi.NewControlClient(cc)
	return c, nil
}

func caFilePool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &TierError{Code: CodeTLS, Message: fmt.Sprintf("cannot read CA file %s", path)}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, &TierError{Code: CodeTLS, Message: fmt.Sprintf("CA file %s holds no PEM certificate", path)}
	}
	return pool, nil
}

// clusterTrustPool collects the live ClusterTrustBundles for Substrate's
// service-DNS signer, through certificates.k8s.io/v1 where it is served and
// v1beta1 on older clusters.
func clusterTrustPool(ctx context.Context, client kubernetes.Interface) (*x509.CertPool, error) {
	if client == nil {
		return nil, &TierError{Code: CodeUnreachable, Message: "not connected to Kubernetes"}
	}
	opts := metav1.ListOptions{LabelSelector: liveBundleSelector}
	type bundle struct{ name, signer, pem string }
	var bundles []bundle
	switch servedVersion(client) {
	case "v1":
		list, err := client.CertificatesV1().ClusterTrustBundles().List(ctx, opts)
		if err != nil {
			return nil, trustListError(err)
		}
		for _, b := range list.Items {
			bundles = append(bundles, bundle{b.Name, b.Spec.SignerName, b.Spec.TrustBundle})
		}
	case "v1beta1":
		list, err := client.CertificatesV1beta1().ClusterTrustBundles().List(ctx, opts)
		if err != nil {
			return nil, trustListError(err)
		}
		for _, b := range list.Items {
			bundles = append(bundles, bundle{b.Name, b.Spec.SignerName, b.Spec.TrustBundle})
		}
	default:
		return nil, &TierError{Code: CodeTLS, Message: "this cluster serves no ClusterTrustBundle API, so Radar cannot verify Substrate's API; set an endpoint with a CA file"}
	}
	pool := x509.NewCertPool()
	found := false
	for _, b := range bundles {
		if b.signer != serviceDNSSignerName {
			continue
		}
		if !pool.AppendCertsFromPEM([]byte(b.pem)) {
			return nil, &TierError{Code: CodeTLS, Message: fmt.Sprintf("ClusterTrustBundle %s holds no valid certificate", b.name)}
		}
		found = true
	}
	if !found {
		return nil, &TierError{Code: CodeTLS, Message: fmt.Sprintf("no live ClusterTrustBundle for signer %s, so Radar cannot verify Substrate's API", serviceDNSSignerName)}
	}
	return pool, nil
}

func servedVersion(client kubernetes.Interface) string {
	for _, version := range []string{"v1", "v1beta1"} {
		list, err := client.Discovery().ServerResourcesForGroupVersion("certificates.k8s.io/" + version)
		if err != nil {
			continue
		}
		for _, r := range list.APIResources {
			if r.Name == "clustertrustbundles" {
				return version
			}
		}
	}
	return ""
}

func trustListError(err error) error {
	if apierrors.IsForbidden(err) {
		return &TierError{Code: CodeTLS, Message: "Kubernetes denied list clustertrustbundles, which Radar needs to verify Substrate's API"}
	}
	return &TierError{Code: CodeTLS, Message: "cannot list ClusterTrustBundles: " + err.Error()}
}

// startForward forwards a local port to Service ate-system/api port 443,
// resolving the Service port to its container port as kubectl does.
func startForward(ctx context.Context, k Kube) (int, error) {
	if k.Client == nil || k.Config == nil {
		return 0, &TierError{Code: CodeUnreachable, Message: "not connected to Kubernetes"}
	}
	svc, err := k.Client.CoreV1().Services(SystemNamespace).Get(ctx, APIService, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, &TierError{Code: CodeNotInstalled, Message: fmt.Sprintf("Service %s/%s does not exist; Substrate's API is not installed here", SystemNamespace, APIService)}
		}
		return 0, &TierError{Code: CodeUnreachable, Message: fmt.Sprintf("cannot read Service %s/%s: %v", SystemNamespace, APIService, err)}
	}
	targetPort, err := resolveTargetPort(ctx, k.Client, svc)
	if err != nil {
		return 0, err
	}
	info, err := portforward.StartWithClients(portforward.OwnerSubstrate, ctx, SystemNamespace, APIService, targetPort, k.Context, k.Client, k.Config)
	if err != nil {
		if errors.Is(err, portforward.ErrAutoDisabled) {
			return 0, &TierError{Code: CodeUnreachable, Message: "automatic port-forwarding is turned off; set an endpoint that reaches Substrate's API"}
		}
		return 0, &TierError{Code: CodeUnreachable, Message: "cannot port-forward to Substrate's API: " + err.Error()}
	}
	return info.LocalPort, nil
}

func resolveTargetPort(ctx context.Context, client kubernetes.Interface, svc *corev1.Service) (int, error) {
	for _, p := range svc.Spec.Ports {
		if p.Port != APIPort {
			continue
		}
		if p.TargetPort.IntValue() > 0 {
			return p.TargetPort.IntValue(), nil
		}
		if p.TargetPort.StrVal == "" {
			return APIPort, nil
		}
		podName, err := pfpkg.FindPodForService(ctx, client, svc.Namespace, svc.Name)
		if err != nil {
			return 0, &TierError{Code: CodeUnreachable, Message: "no running pod serves Substrate's API: " + err.Error()}
		}
		pod, err := client.CoreV1().Pods(svc.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return 0, &TierError{Code: CodeUnreachable, Message: "cannot read Substrate's API pod: " + err.Error()}
		}
		for _, c := range pod.Spec.Containers {
			for _, cp := range c.Ports {
				if cp.Name == p.TargetPort.StrVal && cp.ContainerPort > 0 {
					return int(cp.ContainerPort), nil
				}
			}
		}
		return 0, &TierError{Code: CodeUnreachable, Message: fmt.Sprintf("Substrate's API pod has no port named %s", p.TargetPort.StrVal)}
	}
	return 0, &TierError{Code: CodeUnreachable, Message: fmt.Sprintf("Service %s/%s has no port %d", SystemNamespace, APIService, APIPort)}
}

type tokenSource interface {
	token(ctx context.Context) (string, error)
}

// bearerCreds attaches the identity's token to every RPC and remembers the
// last one sent, so error text can be scrubbed of it.
type bearerCreds struct {
	source tokenSource
	mu     sync.Mutex
	last   string
}

func (b *bearerCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	t, err := b.source.token(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.last = t
	b.mu.Unlock()
	return map[string]string{"authorization": "Bearer " + t}, nil
}

func (b *bearerCreds) RequireTransportSecurity() bool { return true }

func (b *bearerCreds) scrub(text string) string {
	if b == nil {
		return text
	}
	b.mu.Lock()
	t := b.last
	b.mu.Unlock()
	if len(t) >= 8 {
		text = strings.ReplaceAll(text, t, "[token]")
	}
	return text
}

// sharedToken mints tokens for ate-system/ate-client and re-mints them before
// they expire.
type sharedToken struct {
	client kubernetes.Interface
	// now is the clock; nil means time.Now.
	now     func() time.Time
	mu      sync.Mutex
	current string
	expires time.Time
}

func (s *sharedToken) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *sharedToken) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != "" && s.expires.Sub(s.clock()) > tokenRefreshBefore {
		return s.current, nil
	}
	t, expires, err := mintShared(ctx, s.client, s.clock())
	if err != nil {
		msg := fmt.Sprintf("cannot mint a token for %s/%s", SystemNamespace, ClientServiceAccount)
		switch {
		case apierrors.IsForbidden(err):
			msg += ": Kubernetes denied create serviceaccounts/token in " + SystemNamespace
		case apierrors.IsNotFound(err):
			msg += ": the ServiceAccount does not exist"
		}
		return "", status.Error(codes.Unauthenticated, msg)
	}
	s.current, s.expires = t, expires
	return s.current, nil
}

// errEmptyToken is a TokenRequest answered with no token.
var errEmptyToken = errors.New("Kubernetes returned an empty token for " + SharedPrincipal)

// mintShared makes a TokenRequest for ate-system/ate-client with client, as
// kubectl ate does without --token-file: audience the API's name, an hour.
func mintShared(ctx context.Context, client kubernetes.Interface, now time.Time) (string, time.Time, error) {
	seconds := int64(tokenLifetime / time.Second)
	resp, err := client.CoreV1().ServiceAccounts(SystemNamespace).CreateToken(ctx, ClientServiceAccount, &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{Audiences: []string{APIServerName}, ExpirationSeconds: &seconds},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.Status.Token == "" {
		return "", time.Time{}, errEmptyToken
	}
	expires := resp.Status.ExpirationTimestamp.Time
	if expires.IsZero() {
		expires = now.Add(tokenLifetime)
	}
	return resp.Status.Token, expires, nil
}

// fileToken reads its file on every RPC, so a rotated token is picked up
// without a reconnect.
type fileToken struct{ path string }

func (f *fileToken) token(context.Context) (string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return "", status.Error(codes.Unauthenticated, fmt.Sprintf("cannot read token file %s", f.path))
	}
	t := strings.TrimSpace(string(data))
	if t == "" {
		return "", status.Error(codes.Unauthenticated, fmt.Sprintf("token file %s is empty", f.path))
	}
	return t, nil
}
