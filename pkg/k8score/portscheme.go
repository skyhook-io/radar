package k8score

import "strings"

// InferPortScheme guesses the URL scheme for a Kubernetes Service or
// Container port from the strongest available signal:
//
//  1. appProtocol — the K8s-native, authoritative hint when set
//  2. port name   — common conventions (https, https-web, tls, http, h2c)
//  3. port number — well-known web ports (443/8443/… vs 80/8080/…)
//
// Returns "https", "http", or "" when no signal is strong enough to guess.
// Callers that need a default should treat "" as http.
func InferPortScheme(name, appProtocol string, port int32) string {
	switch strings.ToLower(strings.TrimSpace(appProtocol)) {
	case "https", "tls":
		return "https"
	case "http", "http2", "h2c", "grpc", "grpc-web":
		return "http"
	}

	lname := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lname == "":
		// fall through
	case strings.Contains(lname, "https"), strings.Contains(lname, "tls"):
		return "https"
	case strings.Contains(lname, "http"), strings.Contains(lname, "h2c"):
		return "http"
	}

	switch port {
	case 443, 6443, 8443, 9443:
		return "https"
	case 80, 8000, 8080, 3000, 5000, 5001:
		return "http"
	}

	return ""
}
