package substrate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

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
	return c, ok && c.Client != nil
}

// callerMintTimeout bounds the TokenRequest a request makes.
const callerMintTimeout = 10 * time.Second

// callerTokenGrace is how long before its expiry a cached token stops being
// sent. Requests re-mint it tokenRefreshBefore ahead, so only a cluster no
// caller has asked about for that long gets here.
const callerTokenGrace = time.Minute

// callerToken is the token minted for IdentitySharedAsCaller, for one cluster
// capture. Every caller reads as the same principal, so one token serves the
// reads; but a caller is served only while their own access has minted one
// within callerProofFor (proofs), so no caller reads on another's access.
type callerToken struct {
	// minting serializes mints, so concurrent requests mint once; mu guards
	// the fields only, so an RPC reading the token never waits on a mint.
	minting sync.Mutex
	mu      sync.Mutex
	key     string
	token   string
	expires time.Time
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
}

func callerTokenKey(k Kube) string {
	return k.Binding + "\x00" + k.Fingerprint + "\x00" + k.Generation
}

// callerSettings are the implicit settings of IdentitySharedAsCaller: Radar
// runs in the cluster, so it dials the API's Service directly. CallerDial
// overrides where (tests, and a Radar outside the cluster).
func (m *Manager) callerSettings() Settings {
	s := m.CallerDial
	s.Identity = IdentitySharedAsCaller
	if s.Endpoint == "" {
		s.Endpoint = net.JoinHostPort(APIServerName, strconv.Itoa(APIPort))
	}
	return s
}

// ensureCallerToken mints a token with the caller's own client unless the
// caller minted one within callerProofFor and the shared token has more than
// tokenRefreshBefore left. A successful mint records the caller's proof and
// replaces the shared token when it expires later; a refused one refuses this
// caller, whatever the shared token. It runs only on a caller's own request;
// background reads use the shared token and never mint.
func (m *Manager) ensureCallerToken(ctx context.Context, k Kube, c Caller) *TierError {
	t := &m.callerTok
	t.minting.Lock()
	defer t.minting.Unlock()
	key := callerTokenKey(k)
	now := m.Now()
	t.mu.Lock()
	if t.key != key {
		t.clearLocked(key)
	}
	proof, proven := t.proofs[c.Identity]
	fresh := proven && now.Sub(proof) < callerProofFor && t.token != "" && t.expires.Sub(now) > tokenRefreshBefore
	t.mu.Unlock()
	if fresh {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, callerMintTimeout)
	defer cancel()
	if k.Operation != nil {
		defer context.AfterFunc(k.Operation, cancel)()
	}
	token, expires, err := mintShared(ctx, c.Client, m.Now())
	if err != nil {
		return mintError(err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.key != key {
		// Reset ran during the mint: this capture is retired.
		return errRetired
	}
	if expires.After(t.expires) {
		t.token, t.expires = token, expires
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

func mintError(err error) *TierError {
	who := SystemNamespace + "/" + ClientServiceAccount
	switch {
	case apierrors.IsForbidden(err):
		return &TierError{Code: CodeMintDenied, Message: "Your Kubernetes access cannot create a token for " + who + " (create serviceaccounts/token in " + SystemNamespace + "), so Radar cannot read Substrate's API for you."}
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

func (s callerSource) token(context.Context) (string, error) {
	t := &s.m.callerTok
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.key != s.key || t.token == "" || t.expires.Sub(s.m.Now()) < callerTokenGrace {
		return "", status.Error(codes.Unauthenticated, "Radar's token for Substrate's API has expired; the next request from an org owner mints a new one")
	}
	return t.token, nil
}
