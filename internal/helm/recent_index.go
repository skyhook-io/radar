package helm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"helm.sh/helm/v3/pkg/repo"
)

// recentIndexTTL bounds how old a shared version listing may be. A minute
// keeps "newest" current while sparing a large source one download per
// caller: Bitnami's index is about 28 MB, and it throttles parallel
// downloads (six at once took 149 s each, against 3 s for one).
var recentIndexTTL = 60 * time.Second

// recentIndexes holds per-chart version lists from classic index downloads
// that finished within recentIndexTTL, and coalesces concurrent downloads of
// the same repository. Only listings use it; install and upgrade still fetch
// their own index for checksum verification.
type recentIndexes struct {
	mu     sync.Mutex
	byKey  map[string]recentIndex
	flight singleflight.Group
}

type recentIndex struct {
	fetched  time.Time
	versions map[string][]string // chart name -> versions, in index order
}

func (r *recentIndexes) get(key string, now time.Time) (map[string][]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byKey[key]
	if !ok || now.Sub(entry.fetched) >= recentIndexTTL {
		return nil, false
	}
	return entry.versions, true
}

func (r *recentIndexes) put(key string, versions map[string][]string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = map[string]recentIndex{}
	}
	for k, entry := range r.byKey {
		if now.Sub(entry.fetched) >= recentIndexTTL {
			delete(r.byKey, k)
		}
	}
	r.byKey[key] = recentIndex{fetched: now, versions: versions}
}

// recentIndexVersions returns the versions of chartName in a configured
// classic repository from a download that finished within recentIndexTTL,
// downloading once for all concurrent callers otherwise. The key covers the
// whole configured entry, credentials and TLS settings included, so a
// changed configuration never reuses an older listing. Failures aren't
// cached. The returned slice is a copy.
func (c *Client) recentIndexVersions(selected ChartSourceCandidate, chartName string) ([]string, error) {
	configured := c.configuredRepositoryForSource(selected)
	if configured == nil {
		return nil, errSourceConflict
	}
	snapshot := *configured
	snapshot.URL = selected.URL
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	key := hex.EncodeToString(sum[:])

	versions, ok := c.listings.get(key, time.Now())
	if !ok {
		shared, err, _ := c.listings.flight.Do(key, func() (any, error) {
			if versions, ok := c.listings.get(key, time.Now()); ok {
				return versions, nil
			}
			dir, err := os.MkdirTemp("", "radar-versions-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			index, err := c.downloadRepositoryIndex(snapshot, dir, freshVersionsTimeout, maxPreparedIndexBytes)
			if err != nil {
				return nil, err
			}
			byChart := make(map[string][]string, len(index.Entries))
			for name, entries := range index.Entries {
				list := make([]string, 0, len(entries))
				for _, entry := range entries {
					list = append(list, entry.Version)
				}
				byChart[name] = list
			}
			c.listings.put(key, byChart, time.Now())
			return byChart, nil
		})
		if err != nil {
			return nil, err
		}
		versions = shared.(map[string][]string)
	}
	return append([]string(nil), versions[chartName]...), nil
}

// downloadRepositoryIndex downloads one configured repository's index into dir.
func (c *Client) downloadRepositoryIndex(entry repo.Entry, dir string, timeout time.Duration, maxBytes int64) (*repo.IndexFile, error) {
	entry.Name = "selected"
	repository, err := repo.NewChartRepository(&entry, c.repositoryGettersWithLimits(&entry, timeout, maxBytes))
	if err != nil {
		return nil, err
	}
	repository.CachePath = dir
	if _, err := repository.DownloadIndexFile(); err != nil {
		return nil, fmt.Errorf("could not refresh the selected repository: %w", err)
	}
	index, err := repo.LoadIndexFile(filepath.Join(dir, "selected-index.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read selected repository index: %w", err)
	}
	return index, nil
}
