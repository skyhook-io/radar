package server

import "fmt"

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
