package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/probe"
)

// runProbeCommand implements `radar probe` - run reachability probes against ONE
// target from wherever this process runs and print the results as JSON. When the
// process runs inside a cluster pod, the probes traverse the real dataplane
// (subject to NetworkPolicy + mesh mTLS), which is exactly what the laptop's
// apiserver-proxy view cannot confirm. No HTTP server, no Kubernetes client:
// probe-and-exit. This is the binary the in-cluster reachability runner executes
// (the Radar image invoked as `radar probe ...`).
func runProbeCommand(args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	target := fs.String("target", "", "host:port to probe (host alone for a dns-only check)")
	host := fs.String("host", "", "TLS SNI / HTTP Host header (default: the host part of --target)")
	scheme := fs.String("scheme", "http", "http or https (https adds a TLS check and uses an https URL)")
	path := fs.String("path", "/", "request path for the HTTP probe")
	layersFlag := fs.String("layers", "tcp,http", "comma-separated layers: dns,tcp,tls,http")
	timeout := fs.Duration("timeout", 3*time.Second, "overall probe budget")
	jsonOut := fs.Bool("json", true, "emit JSON (false = one human line per probe)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "radar probe: --target host:port is required")
		return 2
	}

	h := *host
	if h == "" {
		if hp, _, err := net.SplitHostPort(*target); err == nil && hp != "" {
			h = hp
		} else {
			h = *target
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	vantage := probe.DetectVantage()

	want := map[string]bool{}
	for _, l := range strings.Split(*layersFlag, ",") {
		if t := strings.TrimSpace(strings.ToLower(l)); t != "" {
			want[t] = true
		}
	}

	var results []probe.Result
	if want["dns"] {
		results = append(results, probe.DNS(ctx, h, vantage))
	}
	if want["tcp"] {
		results = append(results, probe.TCP(ctx, *target, vantage))
	}
	if want["tls"] || strings.EqualFold(*scheme, "https") {
		results = append(results, probe.TLS(ctx, *target, h, vantage))
	}
	if want["http"] {
		reqPath := trace.SanitizeHTTPPath(*path)
		if reqPath == "" {
			reqPath = "/"
		}
		results = append(results, probe.HTTP(ctx, fmt.Sprintf("%s://%s%s", *scheme, *target, reqPath), h, vantage))
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return 1
		}
		return 0
	}
	for _, r := range results {
		detail := r.Detail
		if detail == "" {
			detail = r.Error
		}
		fmt.Printf("%-5s %-22s ok=%-5v tone=%-9s %s\n", r.Layer, r.Target, r.OK, r.Tone, detail)
	}
	return 0
}
