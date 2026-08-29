package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
)

const (
	DefaultListenAddress = "127.0.0.1"
	AllInterfacesAddress = "0.0.0.0"
)

// NormalizeListenAddress validates the supported listener intents and returns
// a concrete address suitable for net.Listen. Radar's local clients always
// dial localhost, so arbitrary interface addresses are intentionally rejected.
func NormalizeListenAddress(address string) (string, error) {
	switch address {
	case "", DefaultListenAddress:
		return DefaultListenAddress, nil
	case "localhost":
		return DefaultListenAddress, nil
	case AllInterfacesAddress:
		return AllInterfacesAddress, nil
	default:
		return "", fmt.Errorf("listen address must be %q, %q, or %q", DefaultListenAddress, "localhost", AllInterfacesAddress)
	}
}

// socketAddress preserves the explicit 0.0.0.0 operator-facing opt-in while
// using Go's empty-host wildcard for the actual listener. The latter retains
// the previous dual-stack behavior on hosts where IPv6 is available.
func socketAddress(listenAddress string, port int) string {
	bindHost := listenAddress
	if bindHost == AllInterfacesAddress {
		bindHost = ""
	}
	return net.JoinHostPort(bindHost, strconv.Itoa(port))
}

func browserLoopbackHostname(hostname string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	return cloud.IsLoopbackHostname(hostname) || strings.HasSuffix(hostname, ".localhost")
}

func requestHostIsLoopback(r *http.Request) bool {
	return browserLoopbackHostname((&url.URL{Host: r.Host}).Hostname())
}

// A loopback bind is the unauthenticated local deployment's access boundary.
// Reject attacker-controlled Host values so DNS rebinding cannot cross it;
// authenticated proxies, shared listeners, and the transport-authenticated
// Cloud tunnel have separate boundaries and legitimately use public hosts.
func (s *Server) protectUnauthenticatedLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.listenAddress != "" &&
			cloud.IsLoopbackHostname(s.listenAddress) &&
			!s.authConfig.Enabled() &&
			!cloud.IsAuthenticatedTunnelRequest(r.Context()) &&
			!requestHostIsLoopback(r) {
			s.writeError(w, http.StatusForbidden, "loopback-only Radar requires a loopback request Host")
			return
		}
		next.ServeHTTP(w, r)
	})
}
