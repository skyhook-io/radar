package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

const tokenReviewTTL = time.Minute

type tokenReviewEntry struct {
	user    pkgauth.User
	expires time.Time
}

var (
	tokenReviewMu    sync.Mutex
	tokenReviewCache = map[[sha256.Size]byte]tokenReviewEntry{}
	// Collapses the burst of identical reviews a page load sends with one token.
	tokenReviewGroup singleflight.Group
)

// ReviewUserToken asks the API server who a user token belongs to
// (SelfSubjectReview, made with that token and nothing else). The answer is
// cached briefly by token hash, never past the token's own expiry.
func ReviewUserToken(ctx context.Context, token string) (*pkgauth.User, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	tokenReviewMu.Lock()
	if e, ok := tokenReviewCache[key]; ok && now.Before(e.expires) {
		tokenReviewMu.Unlock()
		u := e.user
		return &u, nil
	}
	tokenReviewMu.Unlock()

	// Detached from the caller so one cancelled request does not fail the
	// others waiting on the same review.
	ch := tokenReviewGroup.DoChan(string(key[:]), func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return reviewUserToken(ctx, token, key)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		u := res.Val.(pkgauth.User)
		return &u, nil
	}
}

func reviewUserToken(ctx context.Context, token string, key [sha256.Size]byte) (pkgauth.User, error) {
	base := GetConfig()
	if base == nil {
		return pkgauth.User{}, fmt.Errorf("K8s config not initialized")
	}
	cfg := rest.AnonymousClientConfig(base)
	cfg.BearerToken = token
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return pkgauth.User{}, err
	}
	review, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return pkgauth.User{}, err
	}
	info := review.Status.UserInfo
	if info.Username == "" {
		return pkgauth.User{}, fmt.Errorf("the API server returned no username for this token")
	}
	user := pkgauth.User{Username: info.Username, Groups: info.Groups, UID: info.UID}
	if len(info.Extra) > 0 {
		user.Extra = make(map[string][]string, len(info.Extra))
		for k, v := range info.Extra {
			user.Extra[k] = append([]string(nil), v...)
		}
	}

	now := time.Now()
	expires := now.Add(tokenReviewTTL)
	if exp, ok := jwtExpiry(token); ok && exp.Before(expires) {
		expires = exp
	}
	tokenReviewMu.Lock()
	if len(tokenReviewCache) > 4096 {
		tokenReviewCache = map[[sha256.Size]byte]tokenReviewEntry{}
	}
	tokenReviewCache[key] = tokenReviewEntry{user: user, expires: expires}
	tokenReviewMu.Unlock()
	return user, nil
}

// jwtExpiry reads exp from a JWT without verifying it. Only used to bound a
// cache entry for a token the API server has already accepted.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
