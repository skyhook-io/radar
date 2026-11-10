package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

type confirmationContextKey struct{}

const maxConfirmationBytes = 8192

var confirmationKey = sync.OnceValues(func() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
})

type mutationConfirmation struct {
	Action       string `json:"action"`
	Context      string `json:"context"`
	CallerDigest string `json:"callerDigest"`
	Target       string `json:"target"`
	Expires      int64  `json:"expires"`
	// Nonce keeps two previews of the same state apart for single-use tokens.
	Nonce string `json:"nonce,omitempty"`
}

func confirmationBinding(ctx context.Context, action, target string) mutationConfirmation {
	contextName, captured := ctx.Value(confirmationContextKey{}).(string)
	if !captured {
		contextName = k8s.ActiveClusterContext()
	}
	caller := struct {
		Username string
		Groups   []string
	}{}
	if user := auth.UserFromContext(ctx); user != nil {
		caller.Username = user.Username
		if len(user.Groups) > 0 {
			caller.Groups = slices.Clone(user.Groups)
			slices.Sort(caller.Groups)
		}
	}
	raw, _ := json.Marshal(caller)
	digest := sha256.Sum256(raw)
	return mutationConfirmation{Action: action, Context: contextName, CallerDigest: base64.RawURLEncoding.EncodeToString(digest[:]), Target: target}
}

func issueMutationConfirmation(ctx context.Context, action, target string) (string, error) {
	binding := confirmationBinding(ctx, action, target)
	binding.Expires = time.Now().Add(5 * time.Minute).Unix()
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	binding.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	key, err := confirmationKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > maxConfirmationBytes {
		return "", fmt.Errorf("deletion preview is too large to issue a verifiable confirmation; no deletion attempted")
	}
	return token, nil
}

func verifyMutationConfirmation(ctx context.Context, action, token string) (string, error) {
	malformed := fmt.Errorf("confirm is malformed or invalid; run dry_run=true again and review the new preview")
	if len(token) > maxConfirmationBytes {
		return "", malformed
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", malformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", malformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", malformed
	}
	key, err := confirmationKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		// The signing key lives for one process, so a restart lands here too.
		return "", fmt.Errorf("confirm is invalid, or Radar restarted since the preview; run dry_run=true again and review the new preview")
	}
	var binding mutationConfirmation
	if json.Unmarshal(raw, &binding) != nil || binding.Action == "" || binding.CallerDigest == "" || binding.Target == "" || binding.Expires == 0 {
		return "", malformed
	}
	if binding.Expires <= time.Now().Unix() {
		return "", fmt.Errorf("confirm expired; run dry_run=true again and review the new preview")
	}
	expected := confirmationBinding(ctx, action, "")
	if binding.Context != expected.Context || binding.CallerDigest != expected.CallerDigest {
		return "", fmt.Errorf("confirm caller or context mismatch; run dry_run=true as the current caller in the intended cluster and review the new preview")
	}
	if binding.Action != action {
		return "", malformed
	}
	return binding.Target, nil
}

var usedConfirmations = struct {
	sync.Mutex
	expires map[[sha256.Size]byte]time.Time
}{expires: map[[sha256.Size]byte]time.Time{}}

// consumeMutationConfirmation marks a verified token used and reports whether
// this was its first use, so a repeated confirm (a retry after a client
// timeout, say) can't start the action twice in this Radar process. It keys
// on the signed payload, not the token text, so a re-encoded copy counts as
// the same token. Entries outlive any token's 5-minute validity.
func consumeMutationConfirmation(token string) bool {
	payload, _, _ := strings.Cut(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	key := sha256.Sum256(raw)
	now := time.Now()
	usedConfirmations.Lock()
	defer usedConfirmations.Unlock()
	for k, expires := range usedConfirmations.expires {
		if now.After(expires) {
			delete(usedConfirmations.expires, k)
		}
	}
	if _, used := usedConfirmations.expires[key]; used {
		return false
	}
	usedConfirmations.expires[key] = now.Add(6 * time.Minute)
	return true
}
