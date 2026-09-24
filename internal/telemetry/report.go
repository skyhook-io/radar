package telemetry

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"time"

	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/version"
)

// Report is the exact payload sent once a day.
type Report struct {
	Schema        int    `json:"schema"`
	InstallID     string `json:"installId"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	InstallMethod string `json:"installMethod"`
	Mode          string `json:"mode"`
	PeriodStart   string `json:"periodStart"`
	PeriodEnd     string `json:"periodEnd"`

	Setup      Setup          `json:"setup"`
	Engagement Engagement     `json:"engagement"`
	Views      map[string]int `json:"views"`
	Actions    map[string]int `json:"actions"`
	MCPTools   map[string]int `json:"mcpTools"`
	UIEvents   map[string]int `json:"uiEvents"`
	Errors     map[string]int `json:"errors"`
	Clusters   ClusterSummary `json:"clusters"`
}

// Setup is how this Radar is configured. Every field is a value from a
// fixed vocabulary Radar defines, never something the user typed.
type Setup struct {
	AuthMode        string   `json:"authMode"`
	TimelineStorage string   `json:"timelineStorage"`
	MCPEnabled      bool     `json:"mcpEnabled"`
	Prometheus      string   `json:"prometheus"`
	CostSource      string   `json:"costSource"`
	AIAgents        []string `json:"aiAgents"`
	AuthPlugins     []string `json:"authPlugins"`
	Browsers        []string `json:"browsers"`
}

// Engagement is how much the UI was used that day.
type Engagement struct {
	Sessions      int    `json:"sessions"`
	ActiveMinutes string `json:"activeMinutes"`
}

// ClusterSummary covers every cluster Radar was connected to that day.
type ClusterSummary struct {
	Contexts string         `json:"contexts"`
	Used     int            `json:"used"`
	Shapes   []ClusterShape `json:"shapes"`
}

// ClusterShape is one cluster described without anything that names it:
// version, platform, sizes as small buckets, and known integrations only.
type ClusterShape struct {
	// ID tells this install's clusters apart across days: a hash of the
	// cluster keyed with a random per-install salt, so the same cluster seen
	// from two installs gets two unrelated IDs. The salt is deleted on opt-out.
	ID                string   `json:"id"`
	KubernetesVersion string   `json:"kubernetesVersion"`
	Platform          string   `json:"platform"`
	Nodes             string   `json:"nodes"`
	Pods              string   `json:"pods"`
	Namespaces        string   `json:"namespaces"`
	CRDs              string   `json:"crds"`
	Integrations      []string `json:"integrations"`
}

// Small buckets: fine enough to tell a laptop cluster from a fleet, coarse
// enough that no count points at one company.
var sizeEdges = []int{0, 1, 2, 3, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000}

// Bucket renders n as the small range it falls in: "0", "1", "3-4", "10-19",
// "50000+".
func Bucket(n int) string {
	if n < 0 {
		n = 0
	}
	for i := 0; i < len(sizeEdges)-1; i++ {
		lo, hi := sizeEdges[i], sizeEdges[i+1]-1
		if n <= hi {
			if lo == hi {
				return strconv.Itoa(lo)
			}
			return fmt.Sprintf("%d-%d", lo, hi)
		}
	}
	return strconv.Itoa(sizeEdges[len(sizeEdges)-1]) + "+"
}

var minorVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

// MinorVersion reduces a server version like "v1.30.4-eks-a1b2" to "1.30":
// vendor suffixes can carry build identifiers.
func MinorVersion(v string) string {
	m := minorVersion.FindStringSubmatch(v)
	if m == nil {
		return "unknown"
	}
	return m[1] + "." + m[2]
}

// pending is the on-disk state accumulated between reports.
type pending struct {
	PeriodStart   time.Time               `json:"periodStart"`
	LastSentAt    *time.Time              `json:"lastSentAt,omitempty"`
	Views         map[string]int          `json:"views,omitempty"`
	Actions       map[string]int          `json:"actions,omitempty"`
	MCPTools      map[string]int          `json:"mcpTools,omitempty"`
	UIEvents      map[string]int          `json:"uiEvents,omitempty"`
	Errors        map[string]int          `json:"errors,omitempty"`
	Browsers      map[string]int          `json:"browsers,omitempty"`
	Sessions      int                     `json:"sessions,omitempty"`
	ActiveMinutes int                     `json:"activeMinutes,omitempty"`
	Clusters      map[string]ClusterShape `json:"clusters,omitempty"`
}

// empty means nobody used Radar in the period; cluster shape alone is not use.
func (p *pending) empty() bool {
	return len(p.Views) == 0 && len(p.Actions) == 0 && len(p.MCPTools) == 0 &&
		len(p.UIEvents) == 0 && p.Sessions == 0
}

// clone deep-copies p so it can be read outside the lock while recorders keep
// writing the live maps.
func (p pending) clone() pending {
	out := p
	out.Views = copyCounts(p.Views)
	out.Actions = copyCounts(p.Actions)
	out.MCPTools = copyCounts(p.MCPTools)
	out.UIEvents = copyCounts(p.UIEvents)
	out.Errors = copyCounts(p.Errors)
	out.Browsers = copyCounts(p.Browsers)
	if p.Clusters != nil {
		out.Clusters = make(map[string]ClusterShape, len(p.Clusters))
		for k, v := range p.Clusters {
			out.Clusters[k] = v
		}
	}
	return out
}

// mergeFrom adds a failed send's counts back into the live period.
func (p *pending) mergeFrom(o pending) {
	p.Views = addCounts(p.Views, o.Views)
	p.Actions = addCounts(p.Actions, o.Actions)
	p.MCPTools = addCounts(p.MCPTools, o.MCPTools)
	p.UIEvents = addCounts(p.UIEvents, o.UIEvents)
	p.Errors = addCounts(p.Errors, o.Errors)
	p.Browsers = addCounts(p.Browsers, o.Browsers)
	p.Sessions += o.Sessions
	p.ActiveMinutes += o.ActiveMinutes
	for k, v := range o.Clusters {
		if p.Clusters == nil {
			p.Clusters = map[string]ClusterShape{}
		}
		if _, ok := p.Clusters[k]; !ok {
			p.Clusters[k] = v
		}
	}
}

func copyCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func addCounts(dst, src map[string]int) map[string]int {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]int{}
	}
	for k, v := range src {
		dst[k] += v
	}
	return dst
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Collector) buildReport(p pending, identity *settings.UsageIdentity) Report {
	end := c.now()
	start := p.PeriodStart
	if start.IsZero() {
		start = end
	}
	ver := version.Current
	// A custom build's version string is whatever its builder chose and can
	// name a company or pipeline, so only release versions are reported.
	if version.BuildChannelName() == "custom" {
		ver = "custom"
	}
	setup := c.opts.Setup()
	setup.Browsers = sortedKeys(p.Browsers)
	if setup.AIAgents == nil {
		setup.AIAgents = []string{}
	}
	if setup.AuthPlugins == nil {
		setup.AuthPlugins = []string{}
	}

	installID, salt := "", ""
	if identity != nil {
		installID, salt = identity.InstallID, identity.ClusterSalt
	}
	if salt == "" {
		// Previews before opting in: a throwaway salt shows the shape of
		// the field without producing an ID that could ever be sent.
		salt = newInstallID()
	}
	shapes := make([]ClusterShape, 0, len(p.Clusters))
	for key, s := range p.Clusters {
		if s.Integrations == nil {
			s.Integrations = []string{}
		}
		s.ID = clusterID(salt, key)
		shapes = append(shapes, s)
	}
	// Map order would leak nothing, but a stable order keeps previews calm.
	sort.Slice(shapes, func(i, j int) bool {
		a, b := shapes[i], shapes[j]
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		return a.KubernetesVersion < b.KubernetesVersion
	})

	return Report{
		Schema:        schemaVersion,
		InstallID:     installID,
		Version:       ver,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		InstallMethod: version.InstallMethodName(),
		Mode:          c.opts.Mode(),
		PeriodStart:   start.UTC().Format("2006-01-02"),
		PeriodEnd:     end.UTC().Format("2006-01-02"),
		Setup:         setup,
		Engagement:    Engagement{Sessions: p.Sessions, ActiveMinutes: Bucket(p.ActiveMinutes)},
		Views:         copyCounts(p.Views),
		Actions:       copyCounts(p.Actions),
		MCPTools:      copyCounts(p.MCPTools),
		UIEvents:      copyCounts(p.UIEvents),
		Errors:        copyCounts(p.Errors),
		Clusters: ClusterSummary{
			Contexts: Bucket(c.opts.Contexts()),
			Used:     len(shapes),
			Shapes:   shapes,
		},
	}
}

func newIdentity() *settings.UsageIdentity {
	return &settings.UsageIdentity{InstallID: newInstallID(), ClusterSalt: newInstallID()}
}

// newInstallID is random, not derived from the machine: it links one
// install's reports to each other and to nothing else. Opting out deletes it.
func newInstallID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func clusterID(salt, key string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// hashKey keeps raw cluster identifiers out of the pending file.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
