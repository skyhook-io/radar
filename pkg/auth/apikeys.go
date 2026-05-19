package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// APIKey is a stored API key entry. The plaintext is never persisted —
// only the SHA-256 hash. The plaintext is returned exactly once at creation.
type APIKey struct {
	ID          string
	Description string
	Hash        string // hex(sha256(plaintext)) — never exposed via API
	Username    string
	Groups      []string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
}

// APIKeyStore manages per-user API keys backed by SQLite.
// All methods are safe for concurrent use.
type APIKeyStore struct {
	db   *sql.DB
	mu   sync.Mutex // serialises writes (SQLite allows one writer)
	path string
}

// NewAPIKeyStore opens or creates the SQLite key store at path.
func NewAPIKeyStore(path string) (*APIKeyStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create api key store dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open api key store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("[auth] api key store pragma warning: %v", err)
		}
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS api_keys (
			id          TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			hash        TEXT NOT NULL,
			username    TEXT NOT NULL,
			groups      TEXT NOT NULL DEFAULT '',
			created_at  DATETIME NOT NULL,
			last_used_at DATETIME
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create api_keys table: %w", err)
	}

	return &APIKeyStore{db: db, path: path}, nil
}

// Close releases the SQLite connection.
func (s *APIKeyStore) Close() error {
	return s.db.Close()
}

func hashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Lookup finds the key matching plaintext. Returns nil if not found.
// Updates last_used_at asynchronously on a hit.
func (s *APIKeyStore) Lookup(plaintext string) *APIKey {
	want := hashKey(plaintext)

	rows, err := s.db.Query(`SELECT id, description, hash, username, groups, created_at, last_used_at FROM api_keys`)
	if err != nil {
		log.Printf("[auth] api key lookup error: %v", err)
		return nil
	}
	defer rows.Close()

	var found *APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k.Hash), []byte(want)) == 1 {
			found = k
			break
		}
	}
	if found == nil {
		return nil
	}

	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().UTC()
		if _, err := s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, now, found.ID); err != nil {
			log.Printf("[auth] api key update last_used_at: %v", err)
		}
	}()
	return found
}

// Create generates a new API key for username/groups.
// Returns the stored entry and the plaintext key (returned exactly once).
func (s *APIKeyStore) Create(username string, groups []string, description string) (*APIKey, string, error) {
	plaintext, err := randHex(32)
	if err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}
	id, err := randHex(16)
	if err != nil {
		return nil, "", fmt.Errorf("generate key id: %w", err)
	}
	key := &APIKey{
		ID:          "rk_" + id,
		Description: description,
		Hash:        hashKey(plaintext),
		Username:    username,
		Groups:      groups,
		CreatedAt:   time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(
		`INSERT INTO api_keys (id, description, hash, username, groups, created_at) VALUES (?,?,?,?,?,?)`,
		key.ID, key.Description, key.Hash, key.Username, encodeGroups(key.Groups), key.CreatedAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert api key: %w", err)
	}
	return key, plaintext, nil
}

// ListForUser returns all keys belonging to username (hash is never included).
func (s *APIKeyStore) ListForUser(username string) []*APIKey {
	rows, err := s.db.Query(
		`SELECT id, description, hash, username, groups, created_at, last_used_at FROM api_keys WHERE username=? ORDER BY created_at DESC`,
		username,
	)
	if err != nil {
		log.Printf("[auth] api key list error: %v", err)
		return nil
	}
	defer rows.Close()

	var out []*APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			continue
		}
		k.Hash = "" // never expose hash
		out = append(out, k)
	}
	return out
}

// Delete removes the key with id belonging to username.
// Returns false if not found or owned by a different user.
func (s *APIKeyStore) Delete(id, username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id=? AND username=?`, id, username)
	if err != nil {
		log.Printf("[auth] api key delete error: %v", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

type scanner interface {
	Scan(dest ...any) error
}

func scanKey(s scanner) (*APIKey, error) {
	var k APIKey
	var groups string
	var lastUsed sql.NullTime
	if err := s.Scan(&k.ID, &k.Description, &k.Hash, &k.Username, &groups, &k.CreatedAt, &lastUsed); err != nil {
		return nil, err
	}
	k.Groups = decodeGroups(groups)
	if lastUsed.Valid {
		k.LastUsedAt = &lastUsed.Time
	}
	return &k, nil
}

// Groups are stored as comma-separated string in SQLite.
func encodeGroups(groups []string) string {
	result := ""
	for i, g := range groups {
		if i > 0 {
			result += ","
		}
		result += g
	}
	return result
}

func decodeGroups(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if part := s[start:i]; part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}
