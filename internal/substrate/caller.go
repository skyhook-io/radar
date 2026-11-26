package substrate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// Caller is a request served as IdentitySharedAsCaller: Radar mints the
// shared ate-client token with the caller's own Kubernetes client
// (impersonated, or the user's token), so Kubernetes lets Radar read
// Substrate only because the caller could do so with kubectl ate.
type Caller struct {
	Client kubernetes.Interface
	// Identity is the caller's per-user cache key (pkg/auth
	// IdentityCacheKey of the username and groups Radar impersonates).
	Identity string
}

type callerKey struct{}

// WithCaller marks ctx as a request served as IdentitySharedAsCaller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// callerMintTimeout bounds the TokenRequest a request makes.
const callerMintTimeout = 10 * time.Second

// callerTokenGrace is how long before its expiry a cached token stops being
// sent. Requests re-mint it tokenRefreshBefore ahead, so only a cluster no
// caller has asked about for that long gets here.
const callerTokenGrace = time.Minute

const callerRejectionCooldown = 30 * time.Second

// callerToken is the token minted for IdentitySharedAsCaller, for one cluster
// capture. Every caller reads as the same principal, so one token serves the
// reads; but a caller is served only while their own access has minted one
// within callerProofFor (proofs), so no caller reads on another's access.
type callerToken struct {
	// minting serializes mints. mu guards cached tokens and proofs so warm
	// requests never wait for a mint.
	minting         sync.Mutex
	mu              sync.Mutex
	key             string
	token           string
	expires         time.Time
	rejected        bool
	retryRejectedAt time.Time
	// proofs is when each caller's own client last minted a token, by
	// Caller.Identity.
	proofs map[string]time.Time
}

// callerProofFor is how long one caller's successful mint lets them read
// without minting again: once an hour, per caller.
const callerProofFor = tokenLifetime - tokenRefreshBefore

// clearLocked drops the token and every proof; t.mu is held.
func (t *callerToken) clearLocked(key string) {
	t.key, t.token, t.expires, t.proofs = key, "", time.Time{}, nil
	t.rejected, t.retryRejectedAt = false, time.Time{}
}

func callerTokenKey(k Kube) string {
	return k.Binding + "\x00" + k.Fingerprint + "\x00" + k.Generation
}

// callerSettings are the implicit settings of IdentitySharedAsCaller: the
// installation's endpoint and trust (CallerDial), with no token of Radar's.
func (m *Manager) callerSettings() Settings {
	s := m.CallerDial
	s.Identity = IdentitySharedAsCaller
	return s
}

// ensureCallerToken mints a token with the caller's own client unless the
// caller minted one within callerProofFor, the shared token has more than
// tokenRefreshBefore left and a rejection does not need a retry. It runs only
// on a caller's own request; background reads use the shared token and never mint.
func (m *Manager) ensureCallerToken(ctx context.Context, k Kube, c Caller) *TierError {
	if c.Client == nil {
		return &TierError{Code: CodeMintDenied, Message: "Radar could not act as you in Kubernetes, so it cannot create Substrate's token for you."}
	}
	t := &m.callerTok
	key := callerTokenKey(k)
	if t.proofFresh(key, c.Identity, m.Now()) {
		return nil
	}

	t.minting.Lock()
	defer t.minting.Unlock()
	now := m.Now()
	if t.proofFresh(key, c.Identity, now) {
		return nil
	}
	t.mu.Lock()
	if t.key != key {
		t.clearLocked(key)
	}
	if t.rejectionRetryDue(now) {
		t.retryRejectedAt = now.Add(callerRejectionCooldown)
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, callerMintTimeout)
	defer cancel()
	if k.Operation != nil {
		defer context.AfterFunc(k.Operation, cancel)()
	}
	token, expires, err := mintShared(ctx, c.Client, m.Now())
	if err != nil {
		return mintError(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.key != key {
		// Reset ran during the mint: this capture is retired.
		return errRetired
	}
	if t.rejected || expires.After(t.expires) {
		t.rejected = false
		t.token, t.expires = token, expires
		m.callerTokenVersion++
		if failed := m.failed; failed != nil && failed.err.Code == CodeUnauthenticated && failed.key == connKey(k, m.callerSettings()) {
			m.failed = nil
		}
	}
	if t.proofs == nil {
		t.proofs = map[string]time.Time{}
	}
	for id, at := range t.proofs {
		if now.Sub(at) >= tokenLifetime {
			delete(t.proofs, id)
		}
	}
	t.proofs[c.Identity] = now
	return nil
}

func (t *callerToken) proofFresh(key, identity string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	proof, proven := t.proofs[identity]
	return t.key == key && proven && now.Sub(proof) < callerProofFor &&
		t.token != "" && t.expires.Sub(now) > tokenRefreshBefore && !t.rejectionRetryDue(now)
}

// rejectionRetryDue is called with t.mu held.
func (t *callerToken) rejectionRetryDue(now time.Time) bool {
	return t.rejected && !now.Before(t.retryRejectedAt)
}

func mintError(err error) *TierError {
	who := SystemNamespace + "/" + ClientServiceAccount
	switch {
	case apierrors.IsForbidden(err):
		return &TierError{Code: CodeMintDenied, Message: "Your Kubernetes access cannot create a token for " + who + " (create serviceaccounts/token in " + SystemNamespace + "), so Radar cannot read Substrate's API for you."}
	case apierrors.IsUnauthorized(err):
		return &TierError{Code: CodeMintDenied, Message: "Kubernetes did not accept the credentials for this request, so Radar cannot create Substrate's token for you."}
	case apierrors.IsNotFound(err):
		return &TierError{Code: CodeNotInstalled, Message: "ServiceAccount " + who + " does not exist, so Substrate's shared identity is not installed here."}
	case errors.Is(err, errEmptyToken):
		return &TierError{Code: CodeUnauthenticated, Message: err.Error()}
	}
	return &TierError{Code: CodeUnreachable, Message: fmt.Sprintf("cannot create a token for %s: %v", who, err)}
}

// callerSource sends the token cached for its cluster capture.
type callerSource struct {
	m   *Manager
	key string
}

type callerTokenUseKey struct{}

func (s callerSource) token(ctx context.Context) (string, error) {
	t := &s.m.callerTok
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.key != s.key || t.token == "" || t.expires.Sub(s.m.Now()) < callerTokenGrace {
		return "", &TierError{Code: CodeUnauthenticated, Message: "Radar's token for Substrate's API has expired; the next signed-in request mints a new one"}
	}
	token := t.token
	if sent, ok := ctx.Value(callerTokenUseKey{}).(*atomic.Pointer[string]); ok {
		sent.Store(&token)
	}
	return token, nil
}

// recordRejection ties a remote rejection to the token sent on that RPC.
func (s callerSource) recordRejection(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	var sent atomic.Pointer[string]
	err := invoke(context.WithValue(ctx, callerTokenUseKey{}, &sent), method, req, reply, cc, opts...)
	rejected := sent.Load()
	if status.Code(err) == codes.Unauthenticated && rejected != nil {
		t := &s.m.callerTok
		t.mu.Lock()
		if t.key == s.key && t.token == *rejected {
			t.rejected = true
		}
		t.mu.Unlock()
	}
	return err
}
