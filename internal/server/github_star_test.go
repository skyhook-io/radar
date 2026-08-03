package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleGitHubStarIntentOnlyUpdatesLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeStarJSON(&githubStarState{Opens: 10})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/github/star-intent", nil)
	(&Server{}).handleGitHubStarIntent(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]bool
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response["acknowledged"] {
		t.Fatalf("response = %v, want acknowledged=true", response)
	}

	state := readStarJSON()
	if state.PromptSatisfiedAt == "" || state.PromptedAt == "" {
		t.Fatalf("local state did not record star intent: %+v", state)
	}
	if state.StarredAt != "" {
		t.Fatalf("local intent was recorded as a verified GitHub star: %+v", state)
	}
	if state.Dismissals != 0 || state.DismissedAt != "" {
		t.Fatalf("star intent was recorded as a dismissal: %+v", state)
	}
	if state.shouldPromptUI() {
		t.Fatalf("acknowledged web prompt remained eligible: %+v", state)
	}
}
