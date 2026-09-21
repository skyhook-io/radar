package runtimeevidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	pfpkg "github.com/skyhook-io/radar/pkg/portforward"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

type Target struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	UID       string `json:"uid,omitempty"`
	Container string `json:"container,omitempty"`
}

type Result struct {
	Adapter     evidence.Adapter `json:"adapter"`
	Target      Target           `json:"target"`
	ObservedAt  string           `json:"observedAt"`
	Source      string           `json:"source"`
	Outcome     string           `json:"outcome"`
	Reason      string           `json:"reason,omitempty"`
	HTTPStatus  int              `json:"httpStatus,omitempty"`
	Facts       *evidence.Facts  `json:"facts,omitempty"`
	Limitations []string         `json:"limitations"`
}

type endpoint struct {
	port  int
	path  string
	image string
}

func endpointFor(a evidence.Adapter) (endpoint, bool) {
	switch a {
	case evidence.RabbitMQ:
		return endpoint{15692, "/metrics", "rabbitmq"}, true
	case evidence.NATS:
		return endpoint{8222, "/jsz?streams=true&consumers=true&config=false&limit=20", "nats"}, true
	case evidence.Vault:
		return endpoint{8200, "/v1/sys/health", "hashicorp/vault"}, true
	}
	return endpoint{}, false
}

type tunnel struct {
	baseURL string
	close   func()
}
type tunnelStarter func(context.Context, kubernetes.Interface, *rest.Config, string, string, int) (tunnel, error)
type Collector struct {
	slots chan struct{}
	start tunnelStarter
}

func NewCollector() *Collector { return &Collector{slots: make(chan struct{}, 4), start: startTunnel} }

func (c *Collector) Collect(ctx context.Context, client kubernetes.Interface, config *rest.Config, adapter evidence.Adapter, namespace, pod string) Result {
	result := Result{Adapter: adapter, Target: Target{Namespace: namespace, Pod: pod}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Source: "selected_pod_endpoint", Outcome: "unavailable", Limitations: []string{"One selected Pod endpoint only; unavailable evidence is not absence of a problem.", "Image and Pod identity checks do not attest endpoint contents; a same-name Pod replacement can be contacted before the final identity check.", "Plaintext default endpoint only; credentials, custom ports and TLS endpoints are not supported."}}
	fail := func(reason string) Result { result.Reason = reason; return result }
	ep, ok := endpointFor(adapter)
	if !ok {
		return fail("unsupported_adapter")
	}
	if len(validation.IsDNS1123Label(namespace)) > 0 || len(validation.IsDNS1123Subdomain(pod)) > 0 {
		return fail("invalid_target")
	}
	if client == nil || (reflect.ValueOf(client).Kind() == reflect.Pointer && reflect.ValueOf(client).IsNil()) || config == nil {
		return fail("cluster_unavailable")
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return fail("collection_busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	before, err := client.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return fail("pod_read_unavailable")
	}
	identity, reason := identify(before, ep)
	if reason != "" {
		return fail(reason)
	}
	result.Target.UID = string(before.UID)
	result.Target.Container = identity.name
	t, err := c.start(ctx, client, config, namespace, pod, ep.port)
	if err != nil {
		if ctx.Err() != nil {
			return fail("collection_cancelled")
		}
		return fail("port_forward_unavailable")
	}
	defer t.close()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 * 1024, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+ep.path, nil)
	if err != nil {
		return fail("endpoint_unavailable")
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fail("collection_cancelled")
		}
		return fail("endpoint_unavailable")
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	// Authorization and redirect bodies may contain credentials or proxy diagnostics.
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fail("endpoint_denied")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fail("redirect_refused")
	}
	if adapter != evidence.Vault && resp.StatusCode != 200 {
		return fail("unexpected_http_status")
	}
	if adapter == evidence.Vault {
		mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			return fail("endpoint_response_unavailable")
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, evidence.MaxBodyBytes+1))
	if err != nil {
		return fail("endpoint_unavailable")
	}
	facts, err := evidence.Parse(adapter, resp.StatusCode, body)
	if err != nil {
		var r evidence.Reason
		if errors.As(err, &r) {
			return fail(string(r))
		}
		return fail("unexpected_shape")
	}
	after, err := client.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return fail("pod_recheck_unavailable")
	}
	final, reason := identify(after, ep)
	if reason != "" || final != identity {
		return fail("target_changed")
	}
	result.Outcome = "observed"
	result.Facts = &facts
	return result
}

type podIdentity struct {
	uid, name, image, imageID, containerID string
	restarts                               int32
	started                                time.Time
}

func identify(p *corev1.Pod, ep endpoint) (podIdentity, string) {
	if p == nil || p.UID == "" || p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
		return podIdentity{}, "pod_not_running"
	}
	if p.Spec.HostNetwork {
		return podIdentity{}, "host_network_unsupported"
	}
	var selected *corev1.Container
	for i := range p.Spec.Containers {
		c := &p.Spec.Containers[i]
		if imageRepository(c.Image) == ep.image {
			if selected != nil {
				return podIdentity{}, "ambiguous_target"
			}
			selected = c
		}
	}
	if selected == nil {
		return podIdentity{}, "unsupported_image"
	}
	declared := false
	for _, c := range p.Spec.Containers {
		for _, port := range c.Ports {
			if int(port.ContainerPort) == ep.port && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
				if c.Name != selected.Name {
					return podIdentity{}, "ambiguous_target"
				}
				declared = true
			}
		}
	}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			for _, port := range c.Ports {
				if int(port.ContainerPort) == ep.port && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
					return podIdentity{}, "ambiguous_target"
				}
			}
		}
	}
	if !declared {
		return podIdentity{}, "endpoint_port_not_declared"
	}
	for _, s := range p.Status.ContainerStatuses {
		if s.Name == selected.Name {
			if s.State.Running == nil || s.ContainerID == "" || s.ImageID == "" {
				return podIdentity{}, "container_not_running"
			}
			return podIdentity{string(p.UID), s.Name, selected.Image, s.ImageID, s.ContainerID, s.RestartCount, s.State.Running.StartedAt.Time}, ""
		}
	}
	return podIdentity{}, "container_not_running"
}
func imageRepository(image string) string {
	image = strings.SplitN(image, "@", 2)[0]
	if last := strings.LastIndex(image, ":"); last > strings.LastIndex(image, "/") {
		image = image[:last]
	}
	image = strings.TrimPrefix(image, "docker.io/")
	image = strings.TrimPrefix(image, "index.docker.io/")
	image = strings.TrimPrefix(image, "library/")
	return image
}

type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t contextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(t.ctx))
}
func startTunnel(ctx context.Context, client kubernetes.Interface, config *rest.Config, namespace, pod string, port int) (tunnel, error) {
	req := client.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("portforward").VersionedParams(&corev1.PodPortForwardOptions{Ports: []int32{int32(port)}}, scheme.ParameterCodec)
	cfg := rest.CopyConfig(config)
	cfg.Timeout = 15 * time.Second
	previous := cfg.WrapTransport
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if previous != nil {
			rt = previous(rt)
		}
		return contextTransport{ctx, rt}
	}
	dialer, err := pfpkg.NewDialer(cfg, req.URL())
	if err != nil {
		return tunnel{}, err
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{":" + strconv.Itoa(port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return tunnel{}, err
	}
	return runTunnel(ctx, pf, stop, ready)
}

type forwarder interface {
	ForwardPorts() error
	GetPorts() ([]portforward.ForwardedPort, error)
}

func runTunnel(ctx context.Context, pf forwarder, stop chan struct{}, ready <-chan struct{}) (tunnel, error) {
	var once sync.Once
	signalStop := func() { once.Do(func() { close(stop) }) }
	done := make(chan struct{})
	var forwardErr error
	go func() { forwardErr = pf.ForwardPorts(); close(done) }()
	// Retain the collection slot until the forwarder releases its listeners and upstream connection.
	closeTunnel := func() { signalStop(); <-done }
	go func() {
		select {
		case <-ctx.Done():
			closeTunnel()
		case <-stop:
		}
	}()
	select {
	case <-ctx.Done():
		closeTunnel()
		return tunnel{}, ctx.Err()
	case <-done:
		closeTunnel()
		if forwardErr == nil {
			forwardErr = fmt.Errorf("tunnel stopped")
		}
		return tunnel{}, forwardErr
	case <-ready:
		ports, err := pf.GetPorts()
		if err != nil || len(ports) != 1 {
			closeTunnel()
			return tunnel{}, fmt.Errorf("tunnel unavailable")
		}
		return tunnel{baseURL: "http://127.0.0.1:" + strconv.Itoa(int(ports[0].Local)), close: closeTunnel}, nil
	}
}
