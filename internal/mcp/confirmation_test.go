package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestMutationConfirmationBinding(t *testing.T) {
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{"edit", "team"}})
	token, err := issueMutationConfirmation(ctx, "delete_resource", "target-uid-rv-policy")
	if err != nil {
		t.Fatal(err)
	}
	reordered := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{"team", "edit"}})
	if target, err := verifyMutationConfirmation(reordered, "delete_resource", token); err != nil || target != "target-uid-rv-policy" {
		t.Fatalf("valid binding: target=%q err=%v", target, err)
	}
	for _, tc := range []struct {
		name          string
		ctx           context.Context
		action, token string
	}{
		{"other user", auth.ContextWithUser(context.Background(), &auth.User{Username: "bob", Groups: []string{"edit", "team"}}), "delete_resource", token},
		{"other groups", auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{"view"}}), "delete_resource", token},
		{"local caller", context.Background(), "delete_resource", token},
		{"other action", ctx, "uninstall", token},
		{"forged", ctx, "delete_resource", "e30.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verifyMutationConfirmation(tc.ctx, tc.action, tc.token); err == nil {
				t.Fatal("invalid confirmation accepted")
			}
		})
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	var binding mutationConfirmation
	json.Unmarshal(raw, &binding)
	binding.Expires = time.Now().Add(-time.Second).Unix()
	raw, _ = json.Marshal(binding)
	key, _ := confirmationKey()
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	expired := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := verifyMutationConfirmation(ctx, "delete_resource", expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatal("expired confirmation accepted")
	}
	binding.Expires = time.Now().Add(time.Minute).Unix()
	binding.Context = "another-cluster"
	raw, _ = json.Marshal(binding)
	mac.Reset()
	mac.Write(raw)
	wrongCluster := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := verifyMutationConfirmation(ctx, "delete_resource", wrongCluster); err == nil || !strings.Contains(err.Error(), "caller or context mismatch") {
		t.Fatal("cross-context confirmation accepted")
	}
}

func TestMutationConfirmationDigestAndSize(t *testing.T) {
	groups := []string{strings.Repeat("private-membership-", 1000), "private-team"}
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "private-user", Groups: groups})
	token, err := issueMutationConfirmation(ctx, "delete_resource", "target")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	for _, private := range append(groups, "private-user") {
		if strings.Contains(string(raw), private) {
			t.Fatal("caller identity disclosed in token")
		}
	}
	if len(token) > maxConfirmationBytes {
		t.Fatal("unverifiable token issued")
	}
	if _, err := verifyMutationConfirmation(ctx, "delete_resource", token); err != nil {
		t.Fatal(err)
	}
	if oversized, err := issueMutationConfirmation(ctx, "delete_resource", strings.Repeat("x", maxConfirmationBytes)); err == nil || oversized != "" {
		t.Fatal("oversized confirmation issued")
	}
}

func TestMutationConfirmationEmptyGroups(t *testing.T) {
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice"})
	token, err := issueMutationConfirmation(ctx, "delete_resource", "target")
	if err != nil {
		t.Fatal(err)
	}
	empty := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{}})
	if _, err := verifyMutationConfirmation(empty, "delete_resource", token); err != nil {
		t.Fatal(err)
	}
}

func TestMutationConfirmationSingleUse(t *testing.T) {
	ctx := auth.ContextWithUser(context.WithValue(t.Context(), confirmationContextKey{}, "ctx-a"), &auth.User{Username: "u"})
	first, err := issueMutationConfirmation(ctx, "uninstall_helm", "target")
	if err != nil {
		t.Fatal(err)
	}
	second, err := issueMutationConfirmation(ctx, "uninstall_helm", "target")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two previews of the same state must issue distinct tokens")
	}
	if !consumeMutationConfirmation(first) || consumeMutationConfirmation(first) {
		t.Fatal("a token must be usable exactly once")
	}
	if !consumeMutationConfirmation(second) {
		t.Fatal("consuming one token must not consume another preview's token")
	}

	// A re-encoded copy (different trailing bits, same payload) is the same token.
	for _, target := range []string{"t", "tt", "ttt"} {
		token, err := issueMutationConfirmation(ctx, "uninstall_helm", target)
		if err != nil {
			t.Fatal(err)
		}
		payload, signature, _ := strings.Cut(token, ".")
		raw, _ := base64.RawURLEncoding.DecodeString(payload)
		for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" {
			alt := payload[:len(payload)-1] + string(c)
			if alt == payload {
				continue
			}
			if decoded, err := base64.RawURLEncoding.DecodeString(alt); err != nil || string(decoded) != string(raw) {
				continue
			}
			reencoded := alt + "." + signature
			if _, err := verifyMutationConfirmation(ctx, "uninstall_helm", reencoded); err != nil {
				t.Fatalf("fixture: re-encoded token should still verify: %v", err)
			}
			if !consumeMutationConfirmation(token) || consumeMutationConfirmation(reencoded) {
				t.Fatal("a re-encoded copy of a used token must be refused")
			}
			return
		}
	}
	t.Fatal("fixture: no payload length left slack for a re-encoding")
}
