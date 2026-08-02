package auth

import (
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
)

// forwardedIdentityLogCap bounds the dedup set so a long-running agent with a
// large customer base doesn't grow this unbounded. Oldest entries are evicted
// first (FIFO), not true LRU — simplicity over precision for a diagnostic log.
const forwardedIdentityLogCap = 1024

// forwardedIdentitySeen deduplicates "[auth] accepted forwarded identity" log
// lines so a steady stream of requests from the same user/group set doesn't
// spam operator logs. Keyed by a hash of username + sorted groups, so a
// changed group set (an IdP group added or removed) is treated as new and
// logs again.
var forwardedIdentitySeen = &forwardedIdentityLogger{
	seen: make(map[uint64]struct{}),
}

type forwardedIdentityLogger struct {
	mu    sync.Mutex
	seen  map[uint64]struct{}
	order []uint64
}

// shouldLog reports whether key has not been seen before, recording it if so.
func (l *forwardedIdentityLogger) shouldLog(key uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.seen[key]; ok {
		return false
	}
	if len(l.order) >= forwardedIdentityLogCap {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.seen, oldest)
	}
	l.seen[key] = struct{}{}
	l.order = append(l.order, key)
	return true
}

// forwardedIdentityKey hashes username + sorted groups into a single bounded key.
func forwardedIdentityKey(username string, groups []string) uint64 {
	sorted := append([]string(nil), groups...)
	sort.Strings(sorted)

	h := fnv.New64a()
	h.Write([]byte(username))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(sorted, ",")))
	return h.Sum64()
}

// logAcceptedForwardedIdentity logs the first sighting of a distinct
// (username, group-set) forwarded identity so an operator can see what
// identity/groups actually arrived from the IdP/hub — e.g. to diagnose a
// missing `radar:idp:<object-id>` group that never reaches k8s RBAC. Repeat
// requests with the same identity are suppressed; a changed group set logs
// again. Usernames and group names are non-sensitive identifiers, safe to log.
func logAcceptedForwardedIdentity(user *User) {
	if user == nil || user.Username == "" {
		return
	}
	key := forwardedIdentityKey(user.Username, user.Groups)
	if !forwardedIdentitySeen.shouldLog(key) {
		return
	}
	log.Printf("[auth] accepted forwarded identity user=%q groups=%q", user.Username, user.Groups)
}
