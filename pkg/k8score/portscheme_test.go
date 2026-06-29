package k8score

import "testing"

func TestInferPortScheme(t *testing.T) {
	tests := []struct {
		name        string
		portName    string
		appProtocol string
		port        int32
		want        string
	}{
		{"appProtocol https wins over name and port", "http", "https", 80, "https"},
		{"appProtocol tls -> https", "", "tls", 0, "https"},
		{"appProtocol HTTPS case-insensitive", "", "HTTPS", 0, "https"},
		{"appProtocol http", "", "http", 0, "http"},
		{"appProtocol http2", "", "http2", 0, "http"},
		{"appProtocol h2c", "", "h2c", 0, "http"},
		{"appProtocol grpc -> http (cleartext)", "", "grpc", 0, "http"},
		{"appProtocol grpc-web -> http", "", "grpc-web", 0, "http"},
		{"appProtocol unrecognized falls through to name", "https", "kafka", 0, "https"},
		{"appProtocol unrecognized + no name + arbitrary port -> empty", "", "kafka", 9092, ""},

		{"name https", "https", "", 0, "https"},
		{"name https-web", "https-web", "", 0, "https"},
		{"name HTTPS-TLS case-insensitive", "HTTPS-TLS", "", 0, "https"},
		{"name tls", "tls", "", 0, "https"},
		{"name tls-grpc", "tls-grpc", "", 0, "https"},
		{"name http", "http", "", 0, "http"},
		{"name http-web", "http-web", "", 0, "http"},
		{"name http2", "http2", "", 0, "http"},
		{"name h2c", "h2c", "", 0, "http"},
		{"name web alone -> empty (defer to port)", "web", "", 0, ""},
		{"name web + port 80 -> http", "web", "", 80, "http"},

		{"port 443", "", "", 443, "https"},
		{"port 6443 (kube-apiserver)", "", "", 6443, "https"},
		{"port 8443", "", "", 8443, "https"},
		{"port 9443", "", "", 9443, "https"},
		{"port 80", "", "", 80, "http"},
		{"port 8080", "", "", 8080, "http"},
		{"port 8000", "", "", 8000, "http"},
		{"port 3000", "", "", 3000, "http"},
		{"port 5000", "", "", 5000, "http"},

		{"port 22 (ssh) -> empty", "", "", 22, ""},
		{"port 5432 (postgres) -> empty", "", "", 5432, ""},
		{"all empty -> empty", "", "", 0, ""},

		{"appProtocol overrides conflicting name", "https", "http", 80, "http"},
		{"appProtocol cleartext (grpc) wins over https name", "https", "grpc", 443, "http"},
		{"name overrides conflicting port", "https", "", 80, "https"},
		{"https in middle of name still matches", "x-https-y", "", 0, "https"},
		{"name with whitespace trimmed", "  https  ", "", 0, "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferPortScheme(tt.portName, tt.appProtocol, tt.port)
			if got != tt.want {
				t.Errorf("InferPortScheme(%q, %q, %d) = %q, want %q",
					tt.portName, tt.appProtocol, tt.port, got, tt.want)
			}
		})
	}
}
