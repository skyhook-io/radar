package remotecommand

import (
	"net/url"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// NewExecutor creates a WebSocket executor with SPDY fallback for clusters that
// don't support Web Socket exec.
func NewExecutor(config *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	wsExec, err := remotecommand.NewWebSocketExecutor(config, "GET", u.String())
	if err != nil {
		return nil, err
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(config, "POST", u)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, httpstream.IsUpgradeFailure)
}
