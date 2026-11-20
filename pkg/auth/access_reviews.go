package auth

import (
	"context"
	"sync"
)

// AccessReviews shares a SubjectAccessReview among the callers that ask the
// same question of the same permissions entry at once, and records its verdict
// in that entry. The zero value is ready to use.
type AccessReviews struct {
	mu      sync.Mutex
	flights map[reviewKey]*flight[reviewVerdict]
}

// reviewKey names one shared review: the entry its verdict belongs to (nil
// when nothing is cached for the identity) and the identity, connection and
// question it asks.
type reviewKey struct {
	perms *UserPermissions
	key   string
}

type reviewVerdict struct{ allowed, authoritative bool }

// Decide reports whether user may verb resource in namespace ("" for
// cluster-wide), answering from the verdict cached in perms or by running
// review. authoritative is false when no verdict was reached: review couldn't
// consult the apiserver, or the caller's context ended first. Either way the
// answer is a denial and nothing is cached.
//
// Callers asking the same question of the same entry over the same connection
// share one review. It runs while any of them still waits and is canceled when
// the last one stops, so each caller keeps the budget of its own context; a
// caller that needs a cap applies it inside review. A caller holding a newer
// entry for the identity never joins a review started for an older one, and a
// verdict is recorded only in the entry its review was started for, so an
// answer given before a revocation can't outlive the entry it was decided
// under. perms is nil when nothing is cached for the identity; such callers
// share reviews only with each other, and record nothing.
func (a *AccessReviews) Decide(ctx context.Context, user *User, perms *UserPermissions, connection, verb, group, resource, namespace string, review func(context.Context) (allowed, authoritative bool)) (allowed, authoritative bool) {
	if perms != nil {
		if v, ok := perms.CanI(verb, group, resource, namespace); ok {
			return v, true
		}
	}
	b := []byte(cacheKey(user.Username, user.Groups))
	for _, part := range []string{connection, verb, group, resource, namespace} {
		b = appendKeyPart(b, part)
	}
	k := reviewKey{perms: perms, key: string(b)}

	a.mu.Lock()
	if perms != nil {
		// A review that finished since the check above recorded its verdict
		// before leaving the index.
		if v, ok := perms.CanI(verb, group, resource, namespace); ok {
			a.mu.Unlock()
			return v, true
		}
	}
	f := a.flights[k]
	if f == nil {
		f = startFlight(ctx, &a.mu, "access review", func(ctx context.Context) reviewVerdict {
			allowed, authoritative := review(ctx)
			return reviewVerdict{allowed, authoritative}
		}, func(f *flight[reviewVerdict], v reviewVerdict) reviewVerdict {
			// Only a real apiserver verdict is cached: a transient failure
			// fails this check closed but must not deny the tuple for the
			// entry's lifetime. A review its callers all left may finish after
			// a successor recorded a newer verdict in the same entry, so only
			// the key's current review records.
			if v.authoritative && perms != nil && a.flights[k] == f {
				perms.SetCanI(verb, group, resource, namespace, v.allowed)
			}
			a.detachLocked(k, f)
			return v
		})
		if a.flights == nil {
			a.flights = make(map[reviewKey]*flight[reviewVerdict])
		}
		a.flights[k] = f
	} else {
		f.join()
	}
	a.mu.Unlock()

	v, ok := f.await(ctx, &a.mu, func() { a.detachLocked(k, f) })
	if !ok {
		return false, false
	}
	return v.allowed, v.authoritative
}

// detachLocked stops new callers from joining f, with a.mu held.
func (a *AccessReviews) detachLocked(k reviewKey, f *flight[reviewVerdict]) {
	if a.flights[k] == f {
		delete(a.flights, k)
	}
}
