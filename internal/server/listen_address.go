package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"

	"github.com/skyhook-io/radar/internal/cloud"
)

const (
	DefaultListenAddress = "127.0.0.1"
	AllInterfacesAddress = "0.0.0.0"
)

func NormalizeListenAddress(address string) (string, error) {
	if address == "" || address == "localhost" {
		return DefaultListenAddress, nil
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.String(), nil
	}
	return "", fmt.Errorf("listen address must be an IPv4 or IPv6 address, or %q", "localhost")
}

func normalizePortForwardAddress(address string) (string, error) {
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

func clientAddress(listenAddress string, port int) string {
	switch listenAddress {
	case "", DefaultListenAddress, "localhost", AllInterfacesAddress:
		listenAddress = "localhost"
	case "::":
		listenAddress = "::1"
	}
	return net.JoinHostPort(listenAddress, strconv.Itoa(port))
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

// AllowAnyHost is the --allowed-hosts value that turns off Host validation on
// an unauthenticated shared listener.
const AllowAnyHost = "*"

// ParseAllowedHosts reads --allowed-hosts: hostnames, *.<domain> for any name
// under a domain (as an Ingress or HTTPRoute wildcard host reaches Radar), or
// "*" alone.
func ParseAllowedHosts(raw string) ([]string, error) {
	var hosts []string
	var errs []error
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == AllowAnyHost {
			hosts = append(hosts, entry)
			continue
		}
		name, wildcard := strings.CutPrefix(normalizeHostname(entry), "*.")
		// Browsers send internationalized names in their ASCII (punycode) form.
		ascii, err := idna.Lookup.ToASCII(name)
		if err != nil || ascii == "" || strings.ContainsAny(ascii, "*/:@ ") || (wildcard && !strings.Contains(ascii, ".")) {
			errs = append(errs, fmt.Errorf("%q: expected a hostname such as radar.example.com or *.example.com, without scheme or port (or %q alone)", entry, AllowAnyHost))
			continue
		}
		if wildcard {
			ascii = "*." + ascii
		}
		hosts = append(hosts, ascii)
	}
	return hosts, errors.Join(errs...)
}

func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func requestHostname(r *http.Request) string {
	return normalizeHostname((&url.URL{Host: r.Host}).Hostname())
}

// sharedListenerHostAllowed lists the Host values an unauthenticated shared
// listener answers. DNS rebinding needs a name the attacker serves through
// public DNS, so IP literals, single-label names and Kubernetes Service names
// are safe without configuration; any other name must be listed. A custom
// cluster domain is not inferred from ".svc.": svc.attacker.example would match.
// A *.<domain> entry matches names under the domain, not the domain itself,
// as Gateway API hostnames do.
func (s *Server) sharedListenerHostAllowed(r *http.Request) bool {
	if s.allowAnyHost {
		return true
	}
	host := requestHostname(r)
	switch {
	// 253 is the DNS name limit; it also bounds the wildcard walk below.
	case host == "" || len(host) > 253:
		return false
	case browserLoopbackHostname(host), net.ParseIP(host) != nil, !strings.Contains(host, "."),
		strings.HasSuffix(host, ".svc"), strings.HasSuffix(host, ".svc.cluster.local"):
		return true
	}
	if _, ok := s.allowedHosts[host]; ok {
		return true
	}
	for rest := host; strings.Contains(rest, "."); {
		_, rest, _ = strings.Cut(rest, ".")
		if _, ok := s.allowedHosts["*."+rest]; ok {
			return true
		}
	}
	return false
}

// Without authentication, the request Host is what separates a direct client
// from a page that rebound its own hostname to Radar's address. A loopback bind
// accepts only loopback names; a shared listener also accepts the names in
// sharedListenerHostAllowed. Authenticated deployments and the
// transport-authenticated Cloud tunnel have separate boundaries.
func (s *Server) protectUnauthenticatedHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s.hostCheck(r) {
		case hostCheckLoopbackOnly:
			if !requestHostIsLoopback(r) {
				s.writeError(w, http.StatusForbidden, "loopback-only Radar requires a loopback request Host; enable authentication for a reverse proxy, or explicitly use --listen-address=0.0.0.0 only on a trusted network")
				return
			}
		case hostCheckSharedAllowlist:
			if !s.sharedListenerHostAllowed(r) {
				s.writeError(w, http.StatusForbidden, fmt.Sprintf("Radar without authentication answers only localhost, IP addresses, Kubernetes Service names and hosts in --allowed-hosts; add %q to --allowed-hosts (Helm: allowedHosts) or enable authentication", requestHostname(r)))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
