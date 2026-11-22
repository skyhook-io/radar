package substrate

import "regexp"

// KagentSession marks an actor that runs a kagent session. kagent names each
// session's actor session-<session UUID> and runs its lifecycle itself: it
// checks the actor's UID and template before every operation, and treats an
// actor moved to another template as lost, so the session can only be
// deleted. Recovery goes through kagent (Recover on the session's chat, its
// RevertSession call), never through kubectl ate.
//
// It is set only on a cluster that serves kagent's API, from the name alone:
// the name says kagent's naming, not which Agent the session belongs to.
type KagentSession struct {
	SessionID string `json:"sessionId"`
}

var kagentSessionName = regexp.MustCompile(`^session-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// KagentSessionOf is the kagent session an actor's name names; nil for any
// other name.
func KagentSessionOf(name string) *KagentSession {
	m := kagentSessionName.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	return &KagentSession{SessionID: m[1]}
}

// MarkKagentSessions sets Kagent on the actors kagent names, when the cluster
// serves kagent's API (kagentServed). It changes actors in place, so callers
// pass a slice no one else holds yet.
func MarkKagentSessions(actors []Actor, kagentServed bool) {
	if !kagentServed {
		return
	}
	for i := range actors {
		actors[i].Kagent = KagentSessionOf(actors[i].Name)
	}
}

// The steps for kagent sessions, which kagent recovers itself.
const (
	kagentCrashedStep = "These actors are kagent sessions. Recover each with Recover on its chat in kagent; kagent treats a session whose actor was moved to another template as lost."
	// kagentAside follows a generic step when only some actors are kagent's.
	kagentAside       = " Recover the kagent sessions among them with Recover on their chats in kagent instead."
	kagentActorStep   = "This actor is a kagent session. Recover it with Recover on the session's chat in kagent, not with kubectl ate: kagent treats a session whose actor was moved to another template as lost."
	kagentMemoryStep  = "This actor is a kagent session, killed for exceeding its memory limit. Recover it with Recover on the session's chat in kagent; it keeps the template it started with, so it may be killed again. kagent gives every agent the memory in its Helm value config.runtimeDefaults.memory; raise it for new sessions. Don't move it to another template with kubectl ate: kagent then treats the session as lost."
	kagentLostStep    = "This actor is a kagent session: recover it with Recover on the session's chat in kagent. Substrate marks it crashed when it reconciles the worker anyway."
	kagentLostAllStep = "These actors are kagent sessions: recover each with Recover on its chat in kagent. Substrate marks them crashed when it reconciles the worker anyway."
)

// KagentMemoryStep is the memory-limit step for a kagent session's actor.
func KagentMemoryStep() string { return kagentMemoryStep }

func isKagent(a Actor) bool { return a.Kagent != nil }
