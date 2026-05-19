package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type apiKeyResponse struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Username    string     `json:"username"`
	Groups      []string   `json:"groups"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	// Key is only populated on creation — never returned again.
	Key string `json:"key,omitempty"`
}

func toAPIKeyResponse(k *APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:          k.ID,
		Description: k.Description,
		Username:    k.Username,
		Groups:      k.Groups,
		CreatedAt:   k.CreatedAt,
		LastUsedAt:  k.LastUsedAt,
	}
}

func writeAPIKeyJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAPIKeyError(w http.ResponseWriter, status int, msg string) {
	writeAPIKeyJSON(w, status, map[string]string{"error": msg})
}

// HandleListAPIKeys returns the caller's API keys (no hash, no plaintext).
func HandleListAPIKeys(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.APIKeys == nil {
			writeAPIKeyError(w, http.StatusNotImplemented, "API key management is not configured")
			return
		}
		user := UserFromContext(r.Context())
		if user == nil {
			writeAPIKeyError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		keys := cfg.APIKeys.ListForUser(user.Username)
		resp := make([]apiKeyResponse, 0, len(keys))
		for _, k := range keys {
			resp = append(resp, toAPIKeyResponse(k))
		}
		writeAPIKeyJSON(w, http.StatusOK, resp)
	}
}

// HandleCreateAPIKey generates a new API key for the authenticated user.
// The plaintext key is returned exactly once in the response field "key".
// Requires auth to be enabled (mode != "none") — otherwise there is no
// stable user identity to bind the key to.
func HandleCreateAPIKey(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.APIKeys == nil {
			writeAPIKeyError(w, http.StatusNotImplemented, "API key management is not configured")
			return
		}
		if !cfg.Enabled() {
			writeAPIKeyError(w, http.StatusNotImplemented, "API key creation requires auth-mode to be set (proxy or oidc)")
			return
		}
		user := UserFromContext(r.Context())
		if user == nil {
			writeAPIKeyError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		var body struct {
			Description string `json:"description"`
		}
		// Ignore decode error — empty description is fine
		json.NewDecoder(r.Body).Decode(&body)

		entry, plaintext, err := cfg.APIKeys.Create(user.Username, user.Groups, body.Description)
		if err != nil {
			writeAPIKeyError(w, http.StatusInternalServerError, "failed to create API key")
			return
		}
		resp := toAPIKeyResponse(entry)
		resp.Key = plaintext
		writeAPIKeyJSON(w, http.StatusCreated, resp)
	}
}

// HandleDeleteAPIKey revokes the key with {id} belonging to the caller.
// Returns 404 whether the key doesn't exist or belongs to a different user
// to avoid leaking existence of other users' keys.
func HandleDeleteAPIKey(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.APIKeys == nil {
			writeAPIKeyError(w, http.StatusNotImplemented, "API key management is not configured")
			return
		}
		user := UserFromContext(r.Context())
		if user == nil {
			writeAPIKeyError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		id := chi.URLParam(r, "id")
		if !cfg.APIKeys.Delete(id, user.Username) {
			writeAPIKeyError(w, http.StatusNotFound, "API key not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
