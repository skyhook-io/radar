package substrate

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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
	_, s, tier, _, _ := m.settings(WithCaller(context.Background(), caller))
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
	if _, s, _, _, _ := m.settings(context.Background()); s != nil {
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
		_, _, tier, _, _ := m.settings(WithCaller(context.Background(), c))
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

func TestCallerTokenKubernetesUnauthorized(t *testing.T) {
	m := NewManager(nil, fixedKube)
	client := fake.NewClientset()
	var requests []*authv1.TokenRequest
	tokenReactor(client, m.Now, &requests, apierrors.NewUnauthorized("token expired"))
	_, _, tier, _, _ := m.settings(WithCaller(t.Context(), Caller{Client: client}))
	if tier.Code != CodeMintDenied || !strings.Contains(tier.Reason, "Kubernetes did not accept the credentials for this request") {
		t.Errorf("Kubernetes 401 = %+v", tier)
	}
}

func TestWarmCallerDoesNotWaitForAnotherMint(t *testing.T) {
	m := NewManager(nil, fixedKube)
	aliceClient, bobClient := fake.NewClientset(), fake.NewClientset()
	var aliceMints, bobMints []*authv1.TokenRequest
	tokenReactor(aliceClient, m.Now, &aliceMints, nil)
	tokenReactor(bobClient, m.Now, &bobMints, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	bobClient.PrependReactor("create", "serviceaccounts", func(ktesting.Action) (bool, runtime.Object, error) {
		close(entered)
		<-release
		return false, nil, nil
	})

	alice := WithCaller(t.Context(), Caller{Identity: "alice", Client: aliceClient})
	bob := WithCaller(t.Context(), Caller{Identity: "bob", Client: bobClient})
	if _, _, tier, _, _ := m.settings(alice); tier.State == TierFailed {
		t.Fatalf("warm proof = %+v", tier)
	}
	go m.settings(bob)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cold caller did not start minting")
	}
	warm := make(chan *APITier, 1)
	go func() {
		_, _, tier, _, _ := m.settings(alice)
		warm <- tier
	}()
	select {
	case tier := <-warm:
		if tier.State == TierFailed || len(aliceMints) != 1 {
			t.Errorf("warm caller = %+v after %d mints", tier, len(aliceMints))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("warm caller waited for the cold mint")
	}
}

func TestCallerTokenLateRejectionKeepsReplacement(t *testing.T) {
	m := NewManager(nil, fixedKube)
	clock := time.Now()
	m.Now = func() time.Time { return clock }
	k, _ := fixedKube()
	client := fake.NewClientset()
	var requests []*authv1.TokenRequest
	tokenReactor(client, m.Now, &requests, nil)
	alice := Caller{Identity: "alice", Client: client}
	bob := Caller{Identity: "bob", Client: client}
	if err := m.ensureCallerToken(t.Context(), k, alice); err != nil {
		t.Fatal(err)
	}

	source := callerSource{m: m, key: callerTokenKey(k)}
	_ = source.recordRejection(t.Context(), "", nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		if token, err := source.token(ctx); err != nil || token != "token-1" {
			t.Fatalf("token sent = %q, %v", token, err)
		}
		clock = clock.Add(time.Second)
		if err := m.ensureCallerToken(t.Context(), k, bob); err != nil {
			t.Fatal(err)
		}
		return status.Error(codes.Unauthenticated, "token-1 was rejected")
	})

	if token, err := source.token(t.Context()); err != nil || token != "token-2" {
		t.Errorf("token after rejection of its predecessor = %q, %v", token, err)
	}
	if err := m.ensureCallerToken(t.Context(), k, alice); err != nil || len(requests) != 2 {
		t.Errorf("late rejection triggered another mint: %d requests, %v", len(requests), err)
	}
}
