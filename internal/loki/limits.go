package loki

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/skyhook-io/radar/pkg/auth"
)

// ErrBusy: too many reads are running, or this user's last one still is.
var ErrBusy = errors.New("Loki reads are busy")

var (
	errUserBusy = fmt.Errorf("%w: your previous log history read is still running; try again when it finishes", ErrBusy)
	errAllBusy  = fmt.Errorf("%w: Radar is already running %d log history reads; try again in a moment", ErrBusy, maxReads)
)

// maxReads bounds concurrent reads across the process: each holds up to
// maxResponseBytes of answer plus its decoded lines in memory.
const maxReads = 4

var reads struct {
	mu    sync.Mutex
	total int
	users map[string]bool
}

// readerKey is who a read counts against. Without authentication every
// caller is the same reader.
func readerKey(ctx context.Context) string {
	if u := auth.UserFromContext(ctx); u != nil {
		return "user:" + u.Username
	}
	return ""
}

// acquireRead takes a slot for one read by user, or returns ErrBusy at once
// rather than queueing behind a slow Loki.
func acquireRead(user string) (release func(), err error) {
	reads.mu.Lock()
	defer reads.mu.Unlock()
	if reads.users[user] {
		return nil, errUserBusy
	}
	if reads.total >= maxReads {
		return nil, errAllBusy
	}
	if reads.users == nil {
		reads.users = map[string]bool{}
	}
	reads.users[user] = true
	reads.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			reads.mu.Lock()
			defer reads.mu.Unlock()
			delete(reads.users, user)
			reads.total--
		})
	}, nil
}
