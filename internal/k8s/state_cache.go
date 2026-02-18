package k8s

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// StateCache provides SQLite-backed caching for cluster discovery data
// to accelerate startup by avoiding redundant API server calls.
type StateCache struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

// CachedAPIResource is the serializable form of a discovered API resource.
type CachedAPIResource struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Namespaced bool     `json:"namespaced"`
	IsCRD      bool     `json:"isCrd"`
	Verbs      []string `json:"verbs"`
}

// CachedRBACResult is the serializable form of a resource permission check.
type CachedRBACResult struct {
	Resource        string `json:"resource"`
	Group           string `json:"group"`
	Allowed         bool   `json:"allowed"`
	NamespaceScoped bool   `json:"namespaceScoped"`
	Namespace       string `json:"namespace"`
}

// CachedCRDAccess stores whether a CRD GVR passed the access probe.
type CachedCRDAccess struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Resource string `json:"resource"`
	Allowed  bool   `json:"allowed"`
}

// NewStateCache opens (or creates) a SQLite state cache at the given path.
func NewStateCache(dbPath string) (*StateCache, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create cache directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open state cache database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-16000", // 16MB cache
		"PRAGMA busy_timeout=5000",
		"PRAGMA temp_store=MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: failed to set %s: %v", pragma, err)
		}
	}

	sc := &StateCache{db: db, path: dbPath}
	if err := sc.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize state cache schema: %w", err)
	}

	log.Printf("State cache initialized at %s", dbPath)
	return sc, nil
}

// initSchema creates the required tables if they don't exist.
func (sc *StateCache) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS clusters (
		id TEXT PRIMARY KEY,
		context_name TEXT NOT NULL,
		server_url TEXT NOT NULL,
		server_version TEXT NOT NULL,
		last_seen_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS api_resources (
		cluster_id TEXT NOT NULL,
		group_name TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL,
		kind TEXT NOT NULL,
		resource_name TEXT NOT NULL,
		namespaced INTEGER NOT NULL DEFAULT 0,
		is_crd INTEGER NOT NULL DEFAULT 0,
		verbs TEXT NOT NULL DEFAULT '[]',
		cached_at TEXT NOT NULL,
		PRIMARY KEY (cluster_id, group_name, version, kind)
	);

	CREATE TABLE IF NOT EXISTS rbac_cache (
		cluster_id TEXT NOT NULL,
		resource TEXT NOT NULL,
		api_group TEXT NOT NULL DEFAULT '',
		allowed INTEGER NOT NULL DEFAULT 0,
		namespace_scoped INTEGER NOT NULL DEFAULT 0,
		namespace TEXT NOT NULL DEFAULT '',
		cached_at TEXT NOT NULL,
		PRIMARY KEY (cluster_id, api_group, resource)
	);

	CREATE TABLE IF NOT EXISTS crd_access (
		cluster_id TEXT NOT NULL,
		group_name TEXT NOT NULL,
		version TEXT NOT NULL,
		resource_name TEXT NOT NULL,
		allowed INTEGER NOT NULL DEFAULT 0,
		cached_at TEXT NOT NULL,
		PRIMARY KEY (cluster_id, group_name, version, resource_name)
	);
	`
	_, err := sc.db.Exec(schema)
	return err
}

// Close closes the state cache database.
func (sc *StateCache) Close() error {
	if sc == nil || sc.db == nil {
		return nil
	}
	return sc.db.Close()
}

// ClusterID computes a deterministic fingerprint for a cluster.
// If the server version changes (e.g., cluster upgrade), the ID changes,
// effectively invalidating the cache for that cluster.
func ClusterID(contextName, serverURL, serverVersion string) string {
	data := fmt.Sprintf("%s|%s|%s", contextName, serverURL, serverVersion)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:16]) // 32-char hex string
}

// SaveCluster upserts the cluster record and updates last_seen_at.
func (sc *StateCache) SaveCluster(id, contextName, serverURL, serverVersion string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := sc.db.Exec(`
		INSERT INTO clusters (id, context_name, server_url, server_version, last_seen_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at
	`, id, contextName, serverURL, serverVersion, now, now)
	return err
}

// HasCluster checks if we have cached data for this cluster ID.
func (sc *StateCache) HasCluster(clusterID string) bool {
	var count int
	err := sc.db.QueryRow("SELECT COUNT(*) FROM clusters WHERE id = ?", clusterID).Scan(&count)
	return err == nil && count > 0
}

// --- API Resources ---

// GetAPIResources loads cached API resources for a cluster, filtered by maxAge.
// Returns nil if no cache exists or if the cache is too old.
func (sc *StateCache) GetAPIResources(clusterID string, maxAge time.Duration) ([]CachedAPIResource, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)

	rows, err := sc.db.Query(`
		SELECT group_name, version, kind, resource_name, namespaced, is_crd, verbs
		FROM api_resources
		WHERE cluster_id = ? AND cached_at > ?
	`, clusterID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var resources []CachedAPIResource
	for rows.Next() {
		var r CachedAPIResource
		var namespaced, isCRD int
		var verbsJSON string

		if err := rows.Scan(&r.Group, &r.Version, &r.Kind, &r.Name, &namespaced, &isCRD, &verbsJSON); err != nil {
			return nil, err
		}
		r.Namespaced = namespaced != 0
		r.IsCRD = isCRD != 0
		if err := json.Unmarshal([]byte(verbsJSON), &r.Verbs); err != nil {
			r.Verbs = nil
		}
		resources = append(resources, r)
	}

	if len(resources) == 0 {
		return nil, nil // Cache miss
	}
	return resources, rows.Err()
}

// SaveAPIResources replaces the cached API resources for a cluster.
func (sc *StateCache) SaveAPIResources(clusterID string, resources []CachedAPIResource) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing resources for this cluster
	if _, err := tx.Exec("DELETE FROM api_resources WHERE cluster_id = ?", clusterID); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	stmt, err := tx.Prepare(`
		INSERT INTO api_resources (cluster_id, group_name, version, kind, resource_name, namespaced, is_crd, verbs, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range resources {
		verbsJSON, _ := json.Marshal(r.Verbs)
		namespaced := 0
		if r.Namespaced {
			namespaced = 1
		}
		isCRD := 0
		if r.IsCRD {
			isCRD = 1
		}
		if _, err := stmt.Exec(clusterID, r.Group, r.Version, r.Kind, r.Name, namespaced, isCRD, string(verbsJSON), now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// --- RBAC Cache ---

// GetRBACResults loads cached RBAC permission results for a cluster.
func (sc *StateCache) GetRBACResults(clusterID string, maxAge time.Duration) ([]CachedRBACResult, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)

	rows, err := sc.db.Query(`
		SELECT resource, api_group, allowed, namespace_scoped, namespace
		FROM rbac_cache
		WHERE cluster_id = ? AND cached_at > ?
	`, clusterID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CachedRBACResult
	for rows.Next() {
		var r CachedRBACResult
		var allowed, nsScoped int

		if err := rows.Scan(&r.Resource, &r.Group, &allowed, &nsScoped, &r.Namespace); err != nil {
			return nil, err
		}
		r.Allowed = allowed != 0
		r.NamespaceScoped = nsScoped != 0
		results = append(results, r)
	}

	if len(results) == 0 {
		return nil, nil
	}
	return results, rows.Err()
}

// SaveRBACResults replaces the cached RBAC results for a cluster.
func (sc *StateCache) SaveRBACResults(clusterID string, results []CachedRBACResult) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM rbac_cache WHERE cluster_id = ?", clusterID); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	stmt, err := tx.Prepare(`
		INSERT INTO rbac_cache (cluster_id, resource, api_group, allowed, namespace_scoped, namespace, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range results {
		allowed := 0
		if r.Allowed {
			allowed = 1
		}
		nsScoped := 0
		if r.NamespaceScoped {
			nsScoped = 1
		}
		if _, err := stmt.Exec(clusterID, r.Resource, r.Group, allowed, nsScoped, r.Namespace, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// --- CRD Access Cache ---

// GetCRDAccess loads cached CRD access probe results for a cluster.
func (sc *StateCache) GetCRDAccess(clusterID string, maxAge time.Duration) ([]CachedCRDAccess, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)

	rows, err := sc.db.Query(`
		SELECT group_name, version, resource_name, allowed
		FROM crd_access
		WHERE cluster_id = ? AND cached_at > ?
	`, clusterID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CachedCRDAccess
	for rows.Next() {
		var r CachedCRDAccess
		var allowed int

		if err := rows.Scan(&r.Group, &r.Version, &r.Resource, &allowed); err != nil {
			return nil, err
		}
		r.Allowed = allowed != 0
		results = append(results, r)
	}

	if len(results) == 0 {
		return nil, nil
	}
	return results, rows.Err()
}

// SaveCRDAccess replaces the cached CRD access results for a cluster.
func (sc *StateCache) SaveCRDAccess(clusterID string, results []CachedCRDAccess) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM crd_access WHERE cluster_id = ?", clusterID); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	stmt, err := tx.Prepare(`
		INSERT INTO crd_access (cluster_id, group_name, version, resource_name, allowed, cached_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range results {
		allowed := 0
		if r.Allowed {
			allowed = 1
		}
		if _, err := stmt.Exec(clusterID, r.Group, r.Version, r.Resource, allowed, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// --- Invalidation ---

// InvalidateCluster removes all cached data for a cluster.
func (sc *StateCache) InvalidateCluster(clusterID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"api_resources", "rbac_cache", "crd_access", "clusters"} {
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE cluster_id = ? OR id = ?", table), clusterID, clusterID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// PurgeStale removes clusters not seen within maxAge and their associated data.
func (sc *StateCache) PurgeStale(maxAge time.Duration) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)

	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Find stale cluster IDs
	rows, err := tx.Query("SELECT id FROM clusters WHERE last_seen_at < ?", cutoff)
	if err != nil {
		return err
	}

	var staleIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		staleIDs = append(staleIDs, id)
	}
	rows.Close()

	for _, id := range staleIDs {
		for _, table := range []string{"api_resources", "rbac_cache", "crd_access", "clusters"} {
			if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE cluster_id = ? OR id = ?", table), id, id); err != nil {
				return err
			}
		}
		log.Printf("Purged stale cache for cluster %s", id)
	}

	return tx.Commit()
}
