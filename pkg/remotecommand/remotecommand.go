// Package remotecommand builds executors for running commands in pod containers
// (exec, attach, cp).
package remotecommand

import (
	"net/url"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

// NewExecutor builds an executor that uses the WebSocket API, falling back to
// the legacy SPDY API when the apiserver doesn't support the WebSocket exec
// subprotocol.
func NewExecutor(config *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	wsExec, err := remotecommand.NewWebSocketExecutor(config, "GET", u.String())
	if err != nil {
		return nil, err
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(config, "POST", u)
	if err != nil {
		return nil, err
	}

	shouldFallback := func(err error) bool {
		return streamhttp.IsUpgradeFailure(err) || streamhttp.IsHTTPSProxyError(err)
	}
	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, shouldFallback)
}
