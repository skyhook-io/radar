package trace

import (
	"encoding/json"
	"testing"

	"github.com/skyhook-io/radar/pkg/probe"
)

func TestGuessConcretePath(t *testing.T) {
	cases := []struct {
		in        string
		wantPath  string
		wantGuess bool
	}{
		{"", "/", false},
		{"/", "/", false},
		{"/api", "/api", false},    // Exact / Prefix literal - verbatim
		{"api", "/api", false},     // ensures leading slash
		{"/api/.*", "/api/", true}, // regex tail stripped to leading literal
		{"/v1/[0-9]+", "/v1/", true},
		{"/shop(/.*)?", "/shop", true},
		{".*", "/", true},                               // pure pattern → root guess
		{"/api/v1.0/orders", "/api/v1.0/orders", false}, // literal dot is not a metacharacter
		{"/health.json", "/health.json", false},         // literal file extension
		{"Exact:/api/v1.0", "/api/v1.0", false},         // an exact match is concrete, dots literal
		{"Exact:/foo", "/foo", false},
		{"RegularExpression:/foo.*", "/foo", true}, // regex: dot hugging the quantifier trimmed
		{"RegularExpression:/api/v.", "/api/v.", true},
	}
	for _, c := range cases {
		p, g := guessConcretePath(c.in)
		if p != c.wantPath || g != c.wantGuess {
			t.Errorf("guessConcretePath(%q) = (%q,%v), want (%q,%v)", c.in, p, g, c.wantPath, c.wantGuess)
		}
	}
}

func TestSchemeForPort(t *testing.T) {
	cases := []struct {
		port PortMap
		want string
	}{
		{PortMap{Port: 80}, "http"},
		{PortMap{Port: 8080}, "http"},
		{PortMap{Port: 443}, "https"},
		{PortMap{Port: 8443, AppProtocol: "https"}, "https"},
		{PortMap{Port: 8443, AppProtocol: "HTTPS2"}, "https"},
		{PortMap{Port: 9000, Name: "https-alt"}, "https"},
		{PortMap{Port: 9000, Name: "http2"}, "http"}, // http2 is not https
	}
	for _, c := range cases {
		if got := schemeForPort(c.port); got != c.want {
			t.Errorf("schemeForPort(%+v) = %q, want %q", c.port, got, c.want)
		}
	}
}

func TestProtocolForPort(t *testing.T) {
	cases := []struct {
		name string
		port PortMap
		want string
	}{
		{name: "ordinary HTTP", port: PortMap{Port: 8080}, want: "http"},
		{name: "HTTPS appProtocol", port: PortMap{Port: 8443, AppProtocol: "https"}, want: "https"},
		{name: "Redis name", port: PortMap{Port: 1234, Name: "redis"}, want: "tcp"},
		{name: "Valkey name", port: PortMap{Port: 1234, Name: "valkey"}, want: "tcp"},
		{name: "Redis number", port: PortMap{Port: 6379}, want: "tcp"},
		{name: "Postgres", port: PortMap{Port: 5432}, want: "tcp"},
		{name: "Kafka appProtocol", port: PortMap{Port: 19092, AppProtocol: "kafka"}, want: "tcp"},
		{name: "UDP is unsupported", port: PortMap{Port: 53, Protocol: "UDP"}, want: ""},
		{name: "SCTP is unsupported", port: PortMap{Port: 3868, Protocol: "SCTP"}, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := protocolForPort(c.port); got != c.want {
				t.Errorf("protocolForPort(%+v) = %q, want %q", c.port, got, c.want)
			}
		})
	}
}

func TestConcreteHost(t *testing.T) {
	if got := concreteHost("*.example.com"); got != "www.example.com" {
		t.Errorf("wildcard host = %q, want www.example.com", got)
	}
	if got := concreteHost("shop.example.com"); got != "shop.example.com" {
		t.Errorf("concrete host changed = %q", got)
	}
	if got := concreteHost(""); got != "" {
		t.Errorf("empty host = %q, want empty", got)
	}
}

func TestGuessInClusterRequest_TCPHasNoHTTPFields(t *testing.T) {
	req := guessInClusterRequest("database.example.com", "/healthz", PortMap{
		Name: "valkey", Port: 6379, Protocol: "TCP",
	})
	if req.Protocol != "tcp" {
		t.Fatalf("protocol = %q, want tcp", req.Protocol)
	}
	if req.Scheme != "" || req.Host != "" || req.Path != "" || req.PathGuessed {
		t.Errorf("TCP request carried HTTP-only fields: %+v", req)
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(wire); got != `{"protocol":"tcp"}` {
		t.Errorf("TCP wire request = %s, want protocol only", got)
	}
}

// A Service subject (no route, single port) gets a root request, scheme from its
// port. An https-named port yields an https guess.
func TestBuildRoutes_AttachesInClusterRequest_ServiceSubject(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 443, Name: "https"}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Port: 443, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || tr.Routes[0].InClusterRequest == nil {
		t.Fatalf("want 1 route with an in-cluster request, got %+v", tr.Routes)
	}
	req := tr.Routes[0].InClusterRequest
	if req.Protocol != "https" || req.Scheme != "https" || req.Path != "/" || req.PathGuessed {
		t.Errorf("service-subject request = %+v, want https / not-guessed", req)
	}
}

// An Ingress route whose path is a regex pattern produces a guessed concrete
// path on the route's in-cluster request, with the declared host as the Host
// header and scheme from the BACKEND service port (plain http here).
func TestBuildRoutes_AttachesInClusterRequest_RegexRoute(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "shop"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api/.*"}, Backends: []BackendRef{{Kind: "Service", Name: "api"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 8080}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Port: 8080, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy}}},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || tr.Routes[0].InClusterRequest == nil {
		t.Fatalf("want 1 route with an in-cluster request, got %+v", tr.Routes)
	}
	req := tr.Routes[0].InClusterRequest
	if req.Protocol != "http" || req.Scheme != "http" || req.Host != "shop.example.com" || req.Path != "/api/" || !req.PathGuessed {
		t.Errorf("regex-route request = %+v, want http shop.example.com /api/ guessed", req)
	}
}

// The prober and the request builder must share one TLS classification - a
// port the prober treats as TLS must never receive a plaintext HTTP Job.
func TestSchemeForPort_AgreesWithProber(t *testing.T) {
	cases := []PortMap{
		{Port: 9000, Name: "wss"},
		{Port: 9000, Name: "tls"},
		{Port: 8443},
		{Port: 9000, AppProtocol: "kubernetes.io/wss"},
	}
	for _, pm := range cases {
		if !isHTTPSPort(pm.Name, pm.AppProtocol, pm.Port) {
			t.Errorf("prober should classify %+v as TLS", pm)
		}
		if got := schemeForPort(pm); got != "https" {
			t.Errorf("schemeForPort(%+v) = %q, want https (prober treats it as TLS)", pm, got)
		}
	}
}

// Anything the TLS classifier accepts must be HTTP-probable - a port the
// prober calls HTTPS must never receive a TCP-only candidate.
func TestTLSClassifiedPortsAreHTTPProbable(t *testing.T) {
	cases := []PortMap{
		{Port: 9000, AppProtocol: "https2"},
		{Port: 9000, Name: "tls"},
		{Port: 9000, Name: "wss"},
	}
	for _, pm := range cases {
		if !isHTTPSPort(pm.Name, pm.AppProtocol, pm.Port) {
			t.Errorf("precondition: %+v should classify TLS", pm)
		}
		if !isHTTPProbablePort(pm.Name, pm.AppProtocol, pm.Port) {
			t.Errorf("%+v is TLS per the prober but not HTTP-probable - it would get a TCP-only candidate", pm)
		}
		if got := protocolForPort(pm); got != "https" {
			t.Errorf("protocolForPort(%+v) = %q, want https", pm, got)
		}
	}
	// Explicit non-HTTP appProtocol keeps winning, even on a TLS-looking port.
	if isHTTPProbablePort("", "tcp", 443) {
		t.Error("explicit appProtocol=tcp must stay non-HTTP-probable even on 443")
	}
}
