package server

import (
	"fmt"
	"net"
	"strconv"
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
