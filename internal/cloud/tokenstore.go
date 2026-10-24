package cloud

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Data keys of the token Secret. The chart creates the Secret empty; Radar
// only ever updates it, and `radar cloud reset-enrollment` clears every key.
const (
	SecretKeyToken       = "token"
	SecretKeyClusterID   = "cluster_id"
	SecretKeyHubOrigin   = "hub_origin"
	SecretKeyWSSURL      = "wss_url"
	SecretKeyNonce       = "nonce"
	SecretKeyNonceOrigin = "nonce_origin"
	SecretKeyStatus      = "enroll_status"
	SecretKeyError       = "enroll_error"
	SecretKeyAt          = "enroll_at"
)

// Values of enroll_status.
const (
	EnrollStatusJoining = "joining"
	EnrollStatusJoined  = "joined"
	EnrollStatusRefused = "refused"
	EnrollStatusWaiting = "waiting"
)

const maxEnrollErrorRunes = 200

var (
	// ErrTokenSecretConflict means the Secret changed since it was read; the
	// caller reloads and decides again.
	ErrTokenSecretConflict = errors.New("token Secret changed since it was read")
	// ErrTokenSecretNotFound means the Secret is gone. Radar never creates it.
	ErrTokenSecretNotFound = errors.New("token Secret not found")
)

// EnrollmentState is what the token Secret holds, plus the version it was
// read at, which every write is conditional on.
type EnrollmentState struct {
	Token       string
	ClusterID   string
	HubOrigin   string
	WSSURL      string
	Nonce       string
	NonceOrigin string
	Status      string
	Error       string
	At          string

	secret *corev1.Secret
}

// Cleared reports a Secret with neither a token nor a join in progress: a
// fresh install, or one an operator reset.
func (s EnrollmentState) Cleared() bool {
	return s.Token == "" && s.Nonce == ""
}

// TokenSecretStore reads and writes one Secret for one Hub. Every credential
// it writes is stamped with HubOrigin, and it refuses to store a token or
// nonce for any other origin.
type TokenSecretStore struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	HubOrigin string
	Now       func() time.Time
}

func (s *TokenSecretStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load reads the Secret.
func (s *TokenSecretStore) Load(ctx context.Context) (EnrollmentState, error) {
	secret, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return EnrollmentState{}, ErrTokenSecretNotFound
	}
	if err != nil {
		return EnrollmentState{}, fmt.Errorf("read token Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return stateFromSecret(secret), nil
}

func stateFromSecret(secret *corev1.Secret) EnrollmentState {
	get := func(k string) string { return string(secret.Data[k]) }
	return EnrollmentState{
		Token:       get(SecretKeyToken),
		ClusterID:   get(SecretKeyClusterID),
		HubOrigin:   get(SecretKeyHubOrigin),
		WSSURL:      get(SecretKeyWSSURL),
		Nonce:       get(SecretKeyNonce),
		NonceOrigin: get(SecretKeyNonceOrigin),
		Status:      get(SecretKeyStatus),
		Error:       get(SecretKeyError),
		At:          get(SecretKeyAt),
		secret:      secret,
	}
}

// SaveNonce records a new join attempt. It must be written before the first
// enroll call so a restart retries the same attempt instead of starting
// another one.
func (s *TokenSecretStore) SaveNonce(ctx context.Context, st EnrollmentState, nonce string) (EnrollmentState, error) {
	if st.Token != "" {
		return st, errors.New("token Secret already holds a token")
	}
	return s.write(ctx, st, func(data map[string][]byte) {
		data[SecretKeyNonce] = []byte(nonce)
		data[SecretKeyNonceOrigin] = []byte(s.HubOrigin)
		s.setStatus(data, EnrollStatusJoining, "")
	})
}

// SaveJoined stores the token from a successful enroll and drops the attempt.
func (s *TokenSecretStore) SaveJoined(ctx context.Context, st EnrollmentState, res *EnrollResult) (EnrollmentState, error) {
	if st.Nonce == "" || st.NonceOrigin != s.HubOrigin {
		return st, fmt.Errorf("token Secret holds no join attempt for %s", s.HubOrigin)
	}
	return s.write(ctx, st, func(data map[string][]byte) {
		data[SecretKeyToken] = []byte(res.Token)
		data[SecretKeyClusterID] = []byte(res.ClusterID)
		data[SecretKeyHubOrigin] = []byte(s.HubOrigin)
		data[SecretKeyWSSURL] = []byte(res.WSSURL)
		delete(data, SecretKeyNonce)
		delete(data, SecretKeyNonceOrigin)
		s.setStatus(data, EnrollStatusJoined, "")
	})
}

// ClearForeign drops a token and a join attempt that belong to another Hub
// origin, so this store's Hub can be joined fresh. Nothing from another Hub
// is ever sent; it is only removed.
func (s *TokenSecretStore) ClearForeign(ctx context.Context, st EnrollmentState) (EnrollmentState, error) {
	return s.write(ctx, st, func(data map[string][]byte) {
		if len(data[SecretKeyToken]) > 0 && string(data[SecretKeyHubOrigin]) != s.HubOrigin {
			for _, k := range []string{SecretKeyToken, SecretKeyClusterID, SecretKeyHubOrigin, SecretKeyWSSURL} {
				delete(data, k)
			}
		}
		if len(data[SecretKeyNonce]) > 0 && string(data[SecretKeyNonceOrigin]) != s.HubOrigin {
			delete(data, SecretKeyNonce)
			delete(data, SecretKeyNonceOrigin)
		}
		for _, k := range []string{SecretKeyStatus, SecretKeyError, SecretKeyAt} {
			delete(data, k)
		}
	})
}

// SaveStatus records enroll_status and enroll_error. It is a no-op when both
// already match, so a retry loop does not rewrite the Secret every attempt.
func (s *TokenSecretStore) SaveStatus(ctx context.Context, st EnrollmentState, status, message string) (EnrollmentState, error) {
	message = truncateRunes(sanitizeConnectErrorText(message), maxEnrollErrorRunes)
	if st.Status == status && st.Error == message {
		return st, nil
	}
	return s.write(ctx, st, func(data map[string][]byte) {
		s.setStatus(data, status, message)
	})
}

func (s *TokenSecretStore) setStatus(data map[string][]byte, status, message string) {
	data[SecretKeyStatus] = []byte(status)
	if message == "" {
		delete(data, SecretKeyError)
	} else {
		data[SecretKeyError] = []byte(message)
	}
	data[SecretKeyAt] = []byte(s.now().UTC().Format(time.RFC3339))
}

func (s *TokenSecretStore) write(ctx context.Context, st EnrollmentState, mutate func(map[string][]byte)) (EnrollmentState, error) {
	if st.secret == nil {
		return st, errors.New("token Secret was not loaded")
	}
	if s.HubOrigin == "" {
		return st, errors.New("token Secret store has no Hub origin")
	}
	next := st.secret.DeepCopy()
	data := make(map[string][]byte, len(next.Data)+4)
	for k, v := range next.Data {
		data[k] = v
	}
	mutate(data)
	if err := checkOrigins(data); err != nil {
		return st, err
	}
	next.Data = data
	next.StringData = nil
	// Update carries the resourceVersion that was read, so the apiserver
	// refuses the write if anything changed the Secret in between.
	updated, err := s.Client.CoreV1().Secrets(s.Namespace).Update(ctx, next, metav1.UpdateOptions{})
	switch {
	case apierrors.IsConflict(err):
		return st, ErrTokenSecretConflict
	case apierrors.IsNotFound(err):
		return st, ErrTokenSecretNotFound
	case err != nil:
		return st, fmt.Errorf("update token Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return stateFromSecret(updated), nil
}

// checkOrigins refuses a write that leaves a token or a join attempt with no
// Hub origin beside it. The typed Save methods only ever stamp this store's
// own origin; fields another Hub wrote earlier stay as they are, and the
// caller never sends them anywhere.
func checkOrigins(data map[string][]byte) error {
	if len(data[SecretKeyToken]) > 0 && len(data[SecretKeyHubOrigin]) == 0 {
		return errors.New("refusing to store a token without its Hub origin")
	}
	if len(data[SecretKeyNonce]) > 0 && len(data[SecretKeyNonceOrigin]) == 0 {
		return errors.New("refusing to store a join attempt without its Hub origin")
	}
	return nil
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
