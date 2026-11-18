package substrate

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

// A caller's request mints; what reads after it, a background refresh
// included, only uses the cached token, and a retired generation's token is
// never sent.
func TestCallerTokenIsMintedOnlyOnARequest(t *testing.T) {
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	m := NewManager(&Store{Path: filepath.Join(t.TempDir(), "substrate.json")}, fixedKube)
	m.Now = now
	k, _ := fixedKube()
	source := callerSource{m: m, key: callerTokenKey(k)}
	if _, err := source.token(context.Background()); err == nil {
		t.Fatal("with nothing minted, a read has no token, and does not mint one")
	}

	c := fake.NewClientset()
	var requests []*authv1.TokenRequest
	tokenReactor(c, now, &requests, nil)
	caller := Caller{Client: c}
	_, s, tier := m.settings(WithCaller(context.Background(), caller))
	if s == nil || s.Identity != IdentitySharedAsCaller || !strings.HasPrefix(s.Endpoint, APIServerName+":") || tier.Identity.Principal != SharedPrincipal {
		t.Fatalf("caller settings = %+v, %+v", s, tier)
	}
	if got, err := source.token(context.Background()); err != nil || got != "token-1" || len(requests) != 1 {
		t.Fatalf("token = %q %v after %d mints", got, err, len(requests))
	}
	m.settings(WithCaller(context.Background(), caller))
	if len(requests) != 1 {
		t.Errorf("a fresh token is reused: %d mints", len(requests))
	}
	clock = clock.Add(55 * time.Minute)
	if _, err := source.token(context.Background()); err != nil {
		t.Error("a token with minutes left is still sent")
	}
	m.settings(WithCaller(context.Background(), caller))
	if len(requests) != 2 {
		t.Errorf("a request within tokenRefreshBefore of expiry mints again: %d", len(requests))
	}
	// Saved settings are not affected by a caller's token.
	if _, s, _ := m.settings(context.Background()); s != nil {
		t.Errorf("without a caller, the saved settings (none here) are used: %+v", s)
	}
	m.Reset()
	if _, err := source.token(context.Background()); err == nil {
		t.Error("Reset drops the caller's token")
	}
}

// Each caller reads only on their own access: a caller whose client cannot
// mint is refused even while another's token serves, and a caller who can
// proves it once per callerProofFor, however many requests they make.
func TestCallerTokenNeedsEachCallersOwnProof(t *testing.T) {
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	capture := Kube{Binding: "ctx-a", Fingerprint: "cluster-a", Generation: "1"}
	kube := func() (Kube, error) { return capture, nil }
	m := NewManager(&Store{Path: filepath.Join(t.TempDir(), "substrate.json")}, kube)
	m.Now = now
	minter := func(fail error) (Caller, *[]*authv1.TokenRequest) {
		c := fake.NewClientset()
		var requests []*authv1.TokenRequest
		tokenReactor(c, now, &requests, fail)
		return Caller{Client: c}, &requests
	}
	read := func(c Caller) *APITier {
		_, _, tier := m.settings(WithCaller(context.Background(), c))
		if tier.State == TierFailed {
			return tier
		}
		return nil
	}

	alice, aliceMints := minter(nil)
	alice.Identity = "alice"
	if tier := read(alice); tier != nil {
		t.Fatalf("alice = %+v", tier)
	}
	bob, bobMints := minter(apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, ClientServiceAccount, nil))
	bob.Identity = "bob"
	if tier := read(bob); tier == nil || tier.Code != CodeMintDenied {
		t.Errorf("bob's access cannot mint, so bob is refused although alice's token serves: %+v", tier)
	}
	if tier := read(alice); tier != nil || len(*aliceMints) != 1 {
		t.Errorf("alice still reads, without minting again: %+v, %d mints", tier, len(*aliceMints))
	}

	carol, carolMints := minter(nil)
	carol.Identity = "carol"
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); read(carol) }()
	}
	wg.Wait()
	if len(*carolMints) != 1 {
		t.Errorf("concurrent requests of one caller mint once: %d", len(*carolMints))
	}
	clockMu.Lock()
	clock = clock.Add(30 * time.Minute)
	clockMu.Unlock()
	read(carol)
	if len(*carolMints) != 1 {
		t.Errorf("a proof lasts the hour: %d", len(*carolMints))
	}
	clockMu.Lock()
	clock = clock.Add(25 * time.Minute)
	clockMu.Unlock()
	read(carol)
	if len(*carolMints) != 2 {
		t.Errorf("an hour on, carol proves again: %d", len(*carolMints))
	}
	_ = bobMints

	// A new cluster capture (a context switch) starts with no proofs.
	capture.Generation = "2"
	read(alice)
	if len(*aliceMints) != 2 {
		t.Errorf("a context switch clears the proofs: %d", len(*aliceMints))
	}
	m.Reset()
	m.callerTok.mu.Lock()
	cleared := m.callerTok.proofs == nil && m.callerTok.token == ""
	m.callerTok.mu.Unlock()
	if !cleared {
		t.Error("Reset clears the token and the proofs")
	}
}
