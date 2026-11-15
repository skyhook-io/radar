package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

type radarClient struct {
	c    config
	http *http.Client
}

func newClient(c config) (*radarClient, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("-url must be an HTTP(S) origin with optional base path, without credentials/query/fragment")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = c.Askers + c.SSE + 8
	tr.ResponseHeaderTimeout = 45 * time.Second
	return &radarClient{c: c, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *radarClient) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.c.URL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.c.User != "" {
		req.Header.Set(c.c.UserHeader, c.c.User)
	}
	if c.c.Groups != "" {
		req.Header.Set(c.c.GroupsHeader, c.c.Groups)
	}
	return c.http.Do(req)
}

func (c *radarClient) bytes(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.c.Timeout)
	defer cancel()
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *radarClient) json(ctx context.Context, path string, v any) error {
	b, err := c.bytes(ctx, path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

func (c *radarClient) waitReady(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.c.ReadyTimeout)
	defer cancel()
	var last error
	for {
		var connection struct {
			State string `json:"state"`
		}
		err := c.json(ctx, "/api/connection?contexts=0", &connection)
		if err == nil && connection.State == "connected" {
			var d struct {
				Version   string `json:"radarVersion"`
				GoVersion string `json:"goVersion"`
				OS        string `json:"goos"`
				Arch      string `json:"goarch"`
				Informers struct {
					Sync *struct {
						Phase string `json:"phase"`
					} `json:"syncStatus"`
				} `json:"informers"`
			}
			err = c.json(ctx, "/api/diagnostics", &d)
			if err == nil && d.Informers.Sync != nil && d.Informers.Sync.Phase == "complete" {
				return map[string]string{"version": d.Version, "go_version": d.GoVersion, "os": d.OS, "arch": d.Arch, "source": "/api/diagnostics"}, nil
			}
			if err == nil {
				err = fmt.Errorf("cache sync has not reported complete in /api/diagnostics")
			}
		}
		if err == nil {
			err = fmt.Errorf("connection state %q", connection.State)
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("readiness: %w (last observation: %v)", ctx.Err(), last)
		case <-time.After(time.Second):
		}
	}
}

type subject = resourceid.Ref

type inventory struct {
	Kind     string          `json:"kind"`
	Group    string          `json:"group"`
	Count    int             `json:"count"`
	Sampled  int             `json:"sampled"`
	Error    string          `json:"error,omitempty"`
	Coverage json.RawMessage `json:"coverage,omitempty"`
}
type kindRef struct {
	Kind  string `json:"kind"`
	Group string `json:"group"`
}

func (c *radarClient) subjects(ctx context.Context) ([]subject, []inventory, []string, error) {
	kinds := []kindRef{{"Pod", ""}, {"Deployment", "apps"}, {"Service", ""}, {"ConfigMap", ""}, {"Secret", ""}, {"Ingress", "networking.k8s.io"}, {"Node", ""}, {"PersistentVolumeClaim", ""}}
	relevant := map[string]bool{"HTTPRoute": true, "GRPCRoute": true, "Gateway": true, "VirtualService": true, "DestinationRule": true, "Application": true, "Kustomization": true, "HelmRelease": true, "Certificate": true}
	var discovered []kindRef
	var notes []string
	if err := c.json(ctx, "/api/api-resources", &discovered); err != nil {
		notes = append(notes, "CRD discovery unavailable: "+err.Error())
	} else {
		seen := map[kindRef]bool{}
		sort.Slice(discovered, func(i, j int) bool {
			if discovered[i].Kind != discovered[j].Kind {
				return discovered[i].Kind < discovered[j].Kind
			}
			return discovered[i].Group < discovered[j].Group
		})
		for _, k := range discovered {
			if relevant[k.Kind] && !seen[k] {
				kinds = append(kinds, k)
				seen[k] = true
			}
		}
	}
	rng := rand.New(rand.NewSource(c.c.Seed))
	var subjects []subject
	var inventoryRows []inventory
	for _, k := range kinds {
		row := inventory{Kind: k.Kind, Group: k.Group}
		path := "/api/resources/" + url.PathEscape(k.Kind) + "?maxItems=50000&group=" + url.QueryEscape(k.Group)
		b, err := c.bytes(ctx, path)
		if err != nil {
			row.Error = err.Error()
			inventoryRows = append(inventoryRows, row)
			continue
		}
		var items []struct {
			APIVersion string `json:"apiVersion"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		// Resource lists shipped as arrays before the coverage envelope; both are real wire contracts.
		b = []byte(strings.TrimSpace(string(b)))
		if len(b) > 0 && b[0] == '[' {
			err = json.Unmarshal(b, &items)
		} else {
			var envelope struct {
				Items    json.RawMessage `json:"items"`
				Coverage json.RawMessage `json:"coverage"`
			}
			err = json.Unmarshal(b, &envelope)
			if err == nil {
				if len(envelope.Items) == 0 {
					err = fmt.Errorf("resource list missing items")
				} else {
					err = json.Unmarshal(envelope.Items, &items)
					row.Coverage = envelope.Coverage
				}
			}
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i].Metadata, items[j].Metadata
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			return a.Name < b.Name
		})
		row.Count = len(items)
		for _, idx := range rng.Perm(len(items))[:min(len(items), c.c.PerKind)] {
			item := items[idx]
			if item.Metadata.Name == "" {
				return nil, nil, nil, fmt.Errorf("%s: item has no name", path)
			}
			group := k.Group
			if item.APIVersion != "" {
				group = resourceid.GroupFromAPIVersion(item.APIVersion)
			}
			subjects = append(subjects, subject{Kind: k.Kind, Group: group, Namespace: item.Metadata.Namespace, Name: item.Metadata.Name})
			row.Sampled++
		}
		inventoryRows = append(inventoryRows, row)
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].Key() < subjects[j].Key() })
	return subjects, inventoryRows, notes, nil
}

func resourcePath(prefix string, s subject) string {
	ns := s.Namespace
	if ns == "" {
		ns = "_"
	}
	return prefix + "/" + url.PathEscape(s.Kind) + "/" + url.PathEscape(ns) + "/" + url.PathEscape(s.Name) + "?group=" + url.QueryEscape(s.Group)
}

func (c *radarClient) profile(ctx context.Context, out, kind, path string) profileResult {
	p := profileResult{Started: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := c.get(ctx, path)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		p.Error = fmt.Sprintf("GET %s: HTTP %d", path, resp.StatusCode)
		return p
	}
	// A disabled pprof route may fall through to the SPA with a misleading 200.
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/octet-stream") {
		p.Error = "expected binary pprof, got " + resp.Header.Get("Content-Type")
		return p
	}
	dest := strings.TrimSuffix(out, filepath.Ext(out)) + "." + kind + ".pprof"
	f, err := os.Create(dest)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	_, err = io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Path = dest
	return p
}

func (c *radarClient) neighborhoodSubjects(ctx context.Context, subjects []subject) ([]subject, map[string]int) {
	var valid []subject
	rejected := map[string]int{}
	for _, s := range subjects {
		var response struct {
			Subgraph *struct {
				Nodes []json.RawMessage `json:"nodes"`
			} `json:"subgraph"`
		}
		err := c.json(ctx, resourcePath("/api/ai/neighborhood", s)+"&profile=auto&hops=1", &response)
		if err != nil {
			key := "request failed"
			if strings.Contains(err.Error(), "HTTP 404") {
				key = "HTTP 404"
			}
			rejected[key]++
			continue
		}
		if response.Subgraph == nil || len(response.Subgraph.Nodes) == 0 {
			rejected["no subgraph nodes"]++
			continue
		}
		valid = append(valid, s)
	}
	return valid, rejected
}
