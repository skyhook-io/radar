// Package vulnintel reads the public exploit-intelligence feeds that rank
// scanner findings: CISA's Known Exploited Vulnerabilities catalog (KEV) and
// FIRST's Exploit Prediction Scoring System (EPSS). It parses the feeds'
// published formats and holds the newest copy it could load; it never
// decides that a CVE is "not exploited" when it holds no catalog.
package vulnintel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Default feed locations. FIRST moved the EPSS file from epss.cyentia.com,
// which still redirects here.
const (
	DefaultKEVURL  = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	DefaultEPSSURL = "https://epss.empiricalsecurity.com/epss_scores-current.csv.gz"
)

// Size bounds for one feed, decompressed. Today's KEV is about 2 MB and
// EPSS about 12 MB.
const (
	maxKEVBytes  = 64 << 20
	maxEPSSBytes = 128 << 20
)

// KEV is one catalog entry, as CISA publishes it.
type KEV struct {
	// DateAdded is when CISA added the CVE (YYYY-MM-DD).
	DateAdded string `json:"dateAdded"`
	// DueDate is the remediation date BOD 22-01 sets for federal agencies
	// (YYYY-MM-DD).
	DueDate string `json:"dueDate"`
	// RansomwareUse is "known" when CISA knows of ransomware campaigns using
	// it, else "unknown": CISA doesn't say a CVE is never used that way.
	RansomwareUse  string `json:"ransomwareUse"`
	RequiredAction string `json:"requiredAction,omitempty"`
	Name           string `json:"name,omitempty"`
}

// EPSS is one CVE's score: the probability of exploitation activity in the
// next 30 days, and the share of scored CVEs at or below it.
type EPSS struct {
	Score      float64 `json:"score"`
	Percentile float64 `json:"percentile"`
}

// KEVCatalog is a parsed catalog.
type KEVCatalog struct {
	// Version is CISA's catalogVersion, e.g. 2026.11.20.
	Version  string
	Released time.Time
	entries  map[string]KEV
}

// Len is how many CVEs the catalog lists.
func (c *KEVCatalog) Len() int { return len(c.entries) }

// Lookup returns the catalog's entry for a CVE ID.
func (c *KEVCatalog) Lookup(id string) (KEV, bool) {
	if c == nil {
		return KEV{}, false
	}
	k, ok := c.entries[strings.ToUpper(strings.TrimSpace(id))]
	return k, ok
}

// ParseKEV reads CISA's known_exploited_vulnerabilities.json.
func ParseKEV(r io.Reader) (*KEVCatalog, error) {
	var doc struct {
		CatalogVersion  string `json:"catalogVersion"`
		DateReleased    string `json:"dateReleased"`
		Vulnerabilities []struct {
			CVEID                      string `json:"cveID"`
			DateAdded                  string `json:"dateAdded"`
			DueDate                    string `json:"dueDate"`
			KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
			RequiredAction             string `json:"requiredAction"`
			VulnerabilityName          string `json:"vulnerabilityName"`
		} `json:"vulnerabilities"`
	}
	body, err := readCapped(r, maxKEVBytes)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("KEV catalog: %w", err)
	}
	if len(doc.Vulnerabilities) == 0 {
		return nil, errors.New("KEV catalog lists no vulnerabilities")
	}
	c := &KEVCatalog{Version: doc.CatalogVersion, entries: make(map[string]KEV, len(doc.Vulnerabilities))}
	c.Released, _ = time.Parse(time.RFC3339, doc.DateReleased)
	for _, v := range doc.Vulnerabilities {
		id := strings.ToUpper(strings.TrimSpace(v.CVEID))
		if id == "" {
			continue
		}
		use := "unknown"
		if strings.EqualFold(strings.TrimSpace(v.KnownRansomwareCampaignUse), "known") {
			use = "known"
		}
		c.entries[id] = KEV{
			DateAdded: v.DateAdded, DueDate: v.DueDate, RansomwareUse: use,
			RequiredAction: v.RequiredAction, Name: v.VulnerabilityName,
		}
	}
	return c, nil
}

// epssEntry packs one score: about 400,000 CVEs are scored, so a map of
// strings would cost tens of MB.
type epssEntry struct {
	key        uint64
	score, pct float32
}

// EPSSScores is a parsed daily score file.
type EPSSScores struct {
	// ModelVersion and ScoreDate come from the file's header comment.
	ModelVersion string
	ScoreDate    time.Time
	entries      []epssEntry
}

// Len is how many CVEs are scored.
func (s *EPSSScores) Len() int { return len(s.entries) }

// Lookup returns a CVE's score. Only CVE IDs are scored; a GHSA or distro
// advisory ID has none.
func (s *EPSSScores) Lookup(id string) (EPSS, bool) {
	if s == nil {
		return EPSS{}, false
	}
	key, ok := cveKey(id)
	if !ok {
		return EPSS{}, false
	}
	i, found := slices.BinarySearchFunc(s.entries, key, func(e epssEntry, k uint64) int {
		switch {
		case e.key < k:
			return -1
		case e.key > k:
			return 1
		}
		return 0
	})
	if !found {
		return EPSS{}, false
	}
	e := s.entries[i]
	return EPSS{Score: round5(e.score), Percentile: round5(e.pct)}, true
}

// round5 undoes float32's noise: the feed publishes five decimals.
func round5(f float32) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'f', 5, 32), 64)
	return v
}

// cveKey packs CVE-YYYY-NNNN into one number.
func cveKey(id string) (uint64, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	rest, ok := strings.CutPrefix(id, "CVE-")
	if !ok {
		return 0, false
	}
	year, num, ok := strings.Cut(rest, "-")
	if !ok || len(year) != 4 {
		return 0, false
	}
	y, err := strconv.ParseUint(year, 10, 16)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(num, 10, 40)
	if err != nil {
		return 0, false
	}
	return y<<40 | n, true
}

// ParseEPSS reads FIRST's daily CSV, gzipped or not: a
// "#model_version:…,score_date:…" comment, a cve,epss,percentile header,
// then one row per CVE.
func ParseEPSS(r io.Reader) (*EPSSScores, error) {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("EPSS scores: %w", err)
		}
		defer gz.Close()
		br = bufio.NewReader(gz)
	}
	body, err := readCapped(br, maxEPSSBytes)
	if err != nil {
		return nil, err
	}
	s := &EPSSScores{}
	if bytes.HasPrefix(body, []byte("#")) {
		line, rest, _ := bytes.Cut(body, []byte("\n"))
		body = rest
		for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(string(line)), "#"), ",") {
			k, v, _ := strings.Cut(part, ":")
			switch strings.TrimSpace(k) {
			case "model_version":
				s.ModelVersion = strings.TrimSpace(v)
			case "score_date":
				s.ScoreDate, _ = time.Parse(time.RFC3339, strings.TrimSpace(v))
			}
		}
	}
	cr := csv.NewReader(bytes.NewReader(body))
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("EPSS scores: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	ci, ok1 := col["cve"]
	ei, ok2 := col["epss"]
	pi, ok3 := col["percentile"]
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("EPSS scores: expected columns cve, epss and percentile")
	}
	s.entries = make([]epssEntry, 0, bytes.Count(body, []byte("\n")))
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("EPSS scores: %w", err)
		}
		if len(rec) <= max(ci, ei, pi) {
			continue
		}
		key, ok := cveKey(rec[ci])
		if !ok {
			continue
		}
		score, err1 := strconv.ParseFloat(rec[ei], 32)
		pct, err2 := strconv.ParseFloat(rec[pi], 32)
		if err1 != nil || err2 != nil {
			continue
		}
		s.entries = append(s.entries, epssEntry{key: key, score: float32(score), pct: float32(pct)})
	}
	if len(s.entries) == 0 {
		return nil, errors.New("EPSS scores: the file scores no CVEs")
	}
	slices.SortFunc(s.entries, func(a, b epssEntry) int {
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return 0
	})
	s.entries = slices.CompactFunc(s.entries, func(a, b epssEntry) bool { return a.key == b.key })
	s.entries = slices.Clip(s.entries)
	return s, nil
}

func readCapped(r io.Reader, n int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > n {
		return nil, fmt.Errorf("feed is over %d MB", n>>20)
	}
	return body, nil
}
