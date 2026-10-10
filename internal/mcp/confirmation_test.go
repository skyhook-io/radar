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
	if err := verifyMutationConfirmation(reordered, "delete_resource", "target-uid-rv-policy", token); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                  string
		ctx                   context.Context
		action, target, token string
	}{
		{"other user", auth.ContextWithUser(context.Background(), &auth.User{Username: "bob", Groups: []string{"edit", "team"}}), "delete_resource", "target-uid-rv-policy", token},
		{"other groups", auth.ContextWithUser(context.Background(), &auth.User{Username: "alice", Groups: []string{"view"}}), "delete_resource", "target-uid-rv-policy", token},
		{"local caller", context.Background(), "delete_resource", "target-uid-rv-policy", token},
		{"other action", ctx, "uninstall", "target-uid-rv-policy", token},
		{"other target", ctx, "delete_resource", "replacement", token},
		{"forged", ctx, "delete_resource", "target-uid-rv-policy", "e30.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyMutationConfirmation(tc.ctx, tc.action, tc.target, tc.token); err == nil {
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
	if err := verifyMutationConfirmation(ctx, "delete_resource", "target-uid-rv-policy", expired); err == nil {
		t.Fatal("expired confirmation accepted")
	}
	binding.Expires = time.Now().Add(time.Minute).Unix()
	binding.Context = "another-cluster"
	raw, _ = json.Marshal(binding)
	mac.Reset()
	mac.Write(raw)
	wrongCluster := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if err := verifyMutationConfirmation(ctx, "delete_resource", "target-uid-rv-policy", wrongCluster); err == nil {
		t.Fatal("cross-context confirmation accepted")
	}
}
