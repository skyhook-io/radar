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

var confirmationKey = sync.OnceValues(func() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
})

type mutationConfirmation struct {
	Action  string   `json:"action"`
	Context string   `json:"context"`
	User    string   `json:"user"`
	Groups  []string `json:"groups,omitempty"`
	Target  string   `json:"target"`
	Expires int64    `json:"expires"`
}

func confirmationBinding(ctx context.Context, action, target string) mutationConfirmation {
	binding := mutationConfirmation{Action: action, Context: k8s.GetContextName(), Target: target}
	if user := auth.UserFromContext(ctx); user != nil {
		binding.User = user.Username
		binding.Groups = slices.Clone(user.Groups)
		slices.Sort(binding.Groups)
	}
	return binding
}

func issueMutationConfirmation(ctx context.Context, action, target string) (string, error) {
	binding := confirmationBinding(ctx, action, target)
	binding.Expires = time.Now().Add(5 * time.Minute).Unix()
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
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyMutationConfirmation(ctx context.Context, action, target, token string) error {
	refusal := fmt.Errorf("confirm is missing, invalid, expired, or the object/options changed; run dry_run=true again and review the new preview")
	if len(token) > 8192 {
		return refusal
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return refusal
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return refusal
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return refusal
	}
	key, err := confirmationKey()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return refusal
	}
	var binding mutationConfirmation
	if json.Unmarshal(raw, &binding) != nil || binding.Expires <= time.Now().Unix() {
		return refusal
	}
	expected := confirmationBinding(ctx, action, target)
	expected.Expires = binding.Expires
	want, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if !hmac.Equal(raw, want) {
		return refusal
	}
	return nil
}
