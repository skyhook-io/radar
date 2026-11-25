package substrate

import "regexp"

// KagentSession marks an actor kagent runs. kagent names the actor of each
// chat session, and of each standalone sandbox, session-<UUID>, and runs its
// lifecycle itself: it checks the actor's UID and template before every
// operation, and treats an actor moved to another template as lost. So no
// step offers kubectl ate's revert or template move for it; recovery goes
// through kagent.
//
// It is set only on a cluster that serves kagent's API, from the names alone:
// they say kagent's naming, not which Agent the actor belongs to.
type KagentSession struct {
	SessionID string `json:"sessionId"`
	// Sandbox: the actor runs a standalone kagent sandbox (its template is
	// named sandbox-<40 hex>), which has no chat and no RevertSession; kagent's
	// SandboxService deletes it. Otherwise it is a chat session's actor.
	Sandbox bool `json:"sandbox,omitempty"`
}

var (
	kagentSessionName     = regexp.MustCompile(`^session-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	kagentSandboxTemplate = regexp.MustCompile(`^sandbox-[0-9a-f]{40}$`)
)

// KagentSessionOf is what kagent runs in an actor of this name and template
// name: a chat session, or a standalone sandbox when the template is one
// kagent makes for sandboxes. nil for any other actor name.
func KagentSessionOf(name, template string) *KagentSession {
	m := kagentSessionName.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	return &KagentSession{SessionID: m[1], Sandbox: kagentSandboxTemplate.MatchString(template)}
}

// MarkKagentSessions sets Kagent on the actors kagent names, when the cluster
// serves kagent's API (kagentServed). It changes actors in place, so callers
// pass a slice no one else holds yet.
func MarkKagentSessions(actors []Actor, kagentServed bool) {
	if !kagentServed {
		return
	}
	for i := range actors {
		actors[i].Kagent = KagentSessionOf(actors[i].Name, actors[i].Template.Name)
	}
}

// The steps for actors kagent runs. A chat session recovers through kagent's
// SessionService/RevertSession call, valid once Substrate marks the actor
// crashed; kagent 1.0.1 has no button or CLI command for it (later versions
// add Recover on the chat). Deleting the chat in kagent ("Stop active turns
// and delete") always works. A standalone sandbox has neither: kagent's
// SandboxService creates, suspends, resumes and deletes it.
const (
	kagentCrashedStep = "These actors are kagent sessions. Recover each through kagent (its RevertSession API, or Recover on the chat where offered) or delete the chat there, never with kubectl ate."
	// kagentAside follows a generic step when only some actors are kagent's.
	kagentAside       = " Recover the kagent sessions among them through kagent instead (RevertSession, or delete the chat)."
	kagentActorStep   = "This actor is a kagent session. Recover it through kagent (its RevertSession API, or Recover on the chat where offered) or delete the chat there. Moved to another template with kubectl ate, it can't be recovered."
	kagentMemoryStep  = "This actor is a kagent session, killed for exceeding its memory limit. Recover it through kagent (RevertSession, or Recover on the chat where offered) or delete the chat; it keeps its template, so it may be killed again. kagent sets every agent's memory in its Helm value config.runtimeDefaults.memory: raise it for new sessions. Never move it to another template with kubectl ate: kagent then can't recover it."
	kagentLostStep    = "This actor is a kagent session. Once Substrate reconciles the worker and marks it crashed, recover it through kagent (RevertSession, or Recover on the chat where offered), or delete the chat there."
	kagentLostAllStep = "These actors are kagent sessions. Once Substrate reconciles the worker and marks them crashed, recover each through kagent (RevertSession, or Recover on the chat where offered), or delete the chat there."

	kagentSandboxStep   = "This actor runs a kagent sandbox. kagent manages it: delete the sandbox through kagent (its SandboxService) and create a new one; don't move or revert it with kubectl ate."
	kagentSandboxesStep = "These actors run kagent sandboxes. kagent manages them: delete each sandbox through kagent (its SandboxService) and create a new one; don't move or revert them with kubectl ate."
	kagentSandboxAside  = " Delete the kagent sandboxes among them through kagent (its SandboxService) and create new ones instead."
	kagentBothStep      = "These actors are kagent sessions and sandboxes, which kagent manages. Recover each session through kagent (RevertSession, or Recover on the chat where offered) or delete its chat; delete each sandbox through kagent's SandboxService and create a new one. Never use kubectl ate on them."
)

func isKagent(a Actor) bool { return a.Kagent != nil }

func isKagentSandbox(a Actor) bool { return a.Kagent != nil && a.Kagent.Sandbox }

// kagentCounts counts the chat sessions and the sandboxes among actors.
func kagentCounts(actors []Actor) (sessions, sandboxes int) {
	for _, a := range actors {
		switch {
		case isKagentSandbox(a):
			sandboxes++
		case isKagent(a):
			sessions++
		}
	}
	return sessions, sandboxes
}

// kagentStep is the step for actors that are all kagent's. lost: their
// worker restarted under them and Substrate has not yet marked them crashed.
func kagentStep(sessions, sandboxes int, lost bool) string {
	switch {
	case sessions > 0 && sandboxes > 0:
		return kagentBothStep
	case sandboxes == 1:
		return kagentSandboxStep
	case sandboxes > 1:
		return kagentSandboxesStep
	case lost && sessions == 1:
		return kagentLostStep
	case lost:
		return kagentLostAllStep
	}
	return kagentCrashedStep
}

// kagentAsideOf follows a generic step for the kagent actors among others.
func kagentAsideOf(sessions, sandboxes int) string {
	aside := ""
	if sessions > 0 {
		aside += kagentAside
	}
	if sandboxes > 0 {
		aside += kagentSandboxAside
	}
	return aside
}

// KagentActorStep is the step for one actor kagent runs; memory: it was
// killed for exceeding its memory limit.
func KagentActorStep(a Actor, memory bool) string {
	switch {
	case isKagentSandbox(a):
		return kagentSandboxStep
	case memory:
		return kagentMemoryStep
	}
	return kagentActorStep
}
