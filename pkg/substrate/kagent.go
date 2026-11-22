package substrate

import "regexp"

// KagentSession marks an actor that runs a kagent session. kagent names each
// session's actor session-<session UUID> and runs its lifecycle itself: it
// checks the actor's UID and template before every operation, and treats an
// actor moved to another template as lost, so the session can only be
// deleted. Recovery goes through kagent (its RevertSession call, or deleting
// the chat), never through kubectl ate.
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

// The steps for kagent sessions, which kagent recovers itself. kagent's
// recovery is its SessionService/RevertSession call, valid once Substrate
// marks the actor crashed; kagent 1.0.1 has no button or CLI command for it
// (later versions add Recover on the chat). Deleting the chat in kagent
// ("Stop active turns and delete") always works.
const (
	kagentCrashedStep = "These actors are kagent sessions. Recover each through kagent (its RevertSession API, or Recover on the chat where offered) or delete the chat there, never with kubectl ate."
	// kagentAside follows a generic step when only some actors are kagent's.
	kagentAside       = " Recover the kagent sessions among them through kagent instead (RevertSession, or delete the chat)."
	kagentActorStep   = "This actor is a kagent session. Recover it through kagent (its RevertSession API, or Recover on the chat where offered) or delete the chat there. Moved to another template with kubectl ate, it can't be recovered."
	kagentMemoryStep  = "This actor is a kagent session, killed for exceeding its memory limit. Recover it through kagent (RevertSession, or Recover on the chat where offered) or delete the chat; it keeps its template, so it may be killed again. kagent sets every agent's memory in its Helm value config.runtimeDefaults.memory: raise it for new sessions. Never move it to another template with kubectl ate: kagent then can't recover it."
	kagentLostStep    = "This actor is a kagent session. Once Substrate reconciles the worker and marks it crashed, recover it through kagent (RevertSession, or Recover on the chat where offered), or delete the chat there."
	kagentLostAllStep = "These actors are kagent sessions. Once Substrate reconciles the worker and marks them crashed, recover each through kagent (RevertSession, or Recover on the chat where offered), or delete the chat there."
)

// KagentMemoryStep is the memory-limit step for a kagent session's actor.
func KagentMemoryStep() string { return kagentMemoryStep }

func isKagent(a Actor) bool { return a.Kagent != nil }
