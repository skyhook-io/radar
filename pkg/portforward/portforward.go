// Package portforward provides low-level K8s port-forwarding primitives.
// These are the pure K8s API building blocks: finding pods, finding ports,
// and running tunnels. Lifecycle management and singleton state live in each
// consumer (e.g., Radar's internal/portforward for metrics proxying).
package portforward

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

// NewDialer builds a port-forward dialer that uses the WebSocket API
// (SPDY-over-WebSocket), falling back to the legacy raw-SPDY API when the
// apiserver doesn't support the WebSocket port-forward subprotocol.
func NewDialer(config *rest.Config, u *url.URL) (httpstream.Dialer, error) {
	wsDialer, err := portforward.NewSPDYOverWebsocketDialer(u, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create websocket dialer: %w", err)
	}

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create round tripper: %w", err)
	}
	spdyDialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", u)

	// The WebSocket dialer is built on k8s.io/streaming/pkg/httpstream, so a
	// rejected upgrade surfaces as that package's error types.
	shouldFallback := func(err error) bool {
		return streamhttp.IsUpgradeFailure(err) || streamhttp.IsHTTPSProxyError(err)
	}
	return portforward.NewFallbackDialer(wsDialer, spdyDialer, shouldFallback), nil
}

// RunPortForward runs a port-forward from localPort to targetPort on the given pod.
// It blocks until the port-forward terminates (stopCh closed or context cancelled).
// readyCh is closed once the tunnel is established and ready to accept connections.
func RunPortForward(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName string, localPort, targetPort int, stopCh chan struct{}, readyCh chan struct{},
) error {
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("portforward").
		VersionedParams(&corev1.PodPortForwardOptions{
			Ports: []int32{int32(targetPort)},
		}, scheme.ParameterCodec)

	dialer, err := NewDialer(config, req.URL())
	if err != nil {
		return err
	}

	ports := []string{fmt.Sprintf("%d:%d", localPort, targetPort)}

	pf, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("failed to create port forwarder: %w", err)
	}

	return pf.ForwardPorts()
}

// FindPodForService finds a running pod backing the given service by selector matching.
func FindPodForService(ctx context.Context, client kubernetes.Interface, namespace, serviceName string) (string, error) {
	svc, err := client.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get service: %w", err)
	}

	if svc.Spec.ClusterIP == "None" || svc.Spec.ClusterIP == "" {
		if len(svc.Spec.Selector) == 0 {
			return "", fmt.Errorf("headless service %s has no selector", serviceName)
		}
	} else if len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s has no selector", serviceName)
	}

	var selector string
	for k, v := range svc.Spec.Selector {
		if selector != "" {
			selector += ","
		}
		selector += k + "=" + v
	}

	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found matching selector for service %s", serviceName)
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("no running pod found for service %s", serviceName)
}

// FindFreePort finds an available local TCP port.
func FindFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}
