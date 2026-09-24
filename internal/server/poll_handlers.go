package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/poll"
	"github.com/skyhook-io/radar/internal/version"
)

const maxPollSubmitBytes = 16 << 10

// Test seams.
var (
	pollStore     = poll.NewStore(poll.DefaultPath())
	pollSubmitURL = "https://releases.skyhook.io/radar/poll"
	pollNow       = time.Now
	pollInstalled = version.InstalledAt
	pollMode      = deploymentMode
	pollDevBuild  = version.IsDevelopmentBuild
)

var pollSubmissionID = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

func pollDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RADAR_POLL"))) {
	case "off", "false", "0", "no":
		return true
	}
	return false
}

func (s *Server) pollInputs(ctx context.Context) poll.Inputs {
	mode := pollMode()
	star := readStarJSON()
	var starPrompted time.Time
	if star.PromptedAt != "" {
		starPrompted, _ = time.Parse(time.RFC3339, star.PromptedAt)
	}
	in := poll.Inputs{
		Now:            pollNow(),
		Round:          poll.Current,
		Disabled:       pollDisabled(),
		DevBuild:       pollDevBuild(),
		CloudMode:      mode == k8s.DeploymentModeCloud,
		TunnelSet:      s.cloudConnectCfg.CloudTunnelConfigured,
		Mode:           string(mode),
		InstalledAt:    pollInstalled(ctx, pollInstallMode(mode)),
		StarPromptedAt: starPrompted,
	}
	if mode == k8s.DeploymentModeLocal {
		in.State = pollStore.Load()
	}
	return in
}

func pollInstallMode(mode k8s.DeploymentMode) string {
	if mode == k8s.DeploymentModeInCluster {
		return "in-cluster"
	}
	return "local"
}

type pollStatusResponse struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
	Round    string `json:"round"`
	Mode     string `json:"mode"`
}

func (s *Server) handlePollStatus(w http.ResponseWriter, r *http.Request) {
	in := s.pollInputs(r.Context())
	eligible, reason := poll.Eligible(in)
	s.writeJSON(w, pollStatusResponse{Eligible: eligible, Reason: reason, Round: in.Round.ID, Mode: in.Mode})
}

// pollMutationAllowed guards the three writes. A cross-site page must not be
// able to send answers, or silence the poll, on the user's behalf.
func (s *Server) pollMutationAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !s.sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request refused")
		return false
	}
	if pollDisabled() || pollDevBuild() || pollMode() == k8s.DeploymentModeCloud || s.cloudConnectCfg.CloudTunnelConfigured {
		s.writeError(w, http.StatusNotFound, "the poll is not available here")
		return false
	}
	return true
}

// handlePollShown records that the card was actually rendered, which starts
// the 90-day quiet period. Only local Radar keeps this; in-cluster the
// browser does, so one viewer's showing never hides it from the rest.
func (s *Server) handlePollShown(w http.ResponseWriter, r *http.Request) {
	if !s.pollMutationAllowed(w, r) {
		return
	}
	if pollMode() == k8s.DeploymentModeLocal {
		if err := pollStore.Update(func(st *poll.State) { st.ShownAt = pollNow() }); err != nil {
			log.Printf("[poll] Failed to record showing: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type pollDismissRequest struct {
	Kind string `json:"kind"`
}

func (s *Server) handlePollDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.pollMutationAllowed(w, r) {
		return
	}
	var req pollDismissRequest
	if err := decodeBoundedJSONBody(w, r, 1<<10, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid dismiss request")
		return
	}
	if req.Kind != "snooze" && req.Kind != "never" {
		s.writeError(w, http.StatusBadRequest, `kind must be "snooze" or "never"`)
		return
	}
	if pollMode() == k8s.DeploymentModeLocal {
		err := pollStore.Update(func(st *poll.State) {
			st.ShownAt = pollNow()
			if req.Kind == "never" {
				st.NeverAt = pollNow()
			}
		})
		if err != nil {
			log.Printf("[poll] Failed to record dismissal: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type pollSubmitRequest struct {
	SubmissionID string                 `json:"submissionId"`
	Answers      map[string]poll.Answer `json:"answers"`
	Email        string                 `json:"email,omitempty"`
	WantsCall    bool                   `json:"wantsCall,omitempty"`
	// ContactOnly resends just the email after the answers already landed.
	ContactOnly bool `json:"contactOnly,omitempty"`
}

type pollFacts struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Method  string `json:"method"`
	Mode    string `json:"mode"`
	Age     string `json:"age"`
}

type pollUpstreamRequest struct {
	Round        string                 `json:"round"`
	Block        string                 `json:"block"`
	SubmissionID string                 `json:"submissionId"`
	Answers      map[string]poll.Answer `json:"answers,omitempty"`
	Facts        pollFacts              `json:"facts"`
	Email        string                 `json:"email,omitempty"`
	WantsCall    bool                   `json:"wantsCall,omitempty"`
	ContactOnly  bool                   `json:"contactOnly,omitempty"`
}

// pollSubmitResponse reports the answers and the contact handoff separately,
// so the UI can say exactly which part didn't go through.
type pollSubmitResponse struct {
	Answers string `json:"answers"`
	Contact string `json:"contact"`
}

func (s *Server) handlePollSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.pollMutationAllowed(w, r) {
		return
	}
	if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, "answers must be sent as JSON")
		return
	}
	var req pollSubmitRequest
	if err := decodeBoundedJSONBody(w, r, maxPollSubmitBytes, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "answers are too long to send")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid answers: "+err.Error())
		return
	}
	if !pollSubmissionID.MatchString(req.SubmissionID) {
		s.writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	email := strings.TrimSpace(req.Email)
	if email != "" {
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email || len(email) > 254 {
			s.writeError(w, http.StatusBadRequest, "that email address doesn't look right")
			return
		}
	}
	if req.WantsCall && email == "" {
		s.writeError(w, http.StatusBadRequest, "add an email so we can set up the call")
		return
	}
	if req.ContactOnly && email == "" {
		s.writeError(w, http.StatusBadRequest, "nothing to resend without an email")
		return
	}

	mode := pollMode()
	round := poll.Current
	now := pollNow()
	if !round.Active(now) {
		s.writeError(w, http.StatusGone, "This poll has closed.")
		return
	}
	var answers map[string]poll.Answer
	if !req.ContactOnly {
		normalized, err := round.Normalize(req.Answers, string(mode))
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid answers: "+err.Error())
			return
		}
		answers = normalized
	}

	method := version.InstallMethodName()
	if mode == k8s.DeploymentModeInCluster {
		method = "helm"
	}
	upstream := pollUpstreamRequest{
		Round:        round.ID,
		Block:        round.Block,
		SubmissionID: req.SubmissionID,
		Answers:      answers,
		Facts: pollFacts{
			Version: version.Current,
			OS:      runtime.GOOS,
			Method:  method,
			Mode:    string(mode),
			Age:     poll.AgeBucket(now, pollInstalled(r.Context(), pollInstallMode(mode))),
		},
		Email:       email,
		WantsCall:   req.WantsCall,
		ContactOnly: req.ContactOnly,
	}

	status, resp, err := forwardPollSubmission(r.Context(), upstream)
	switch {
	case err != nil:
		log.Printf("[poll] Failed to send answers: %v", err)
		s.writeError(w, http.StatusBadGateway, "Couldn't send your answers. Try again in a moment.")
		return
	case status == http.StatusGone:
		s.writeError(w, http.StatusGone, "This poll has closed.")
		return
	case status < 200 || status >= 300:
		log.Printf("[poll] Answers were refused upstream with status %d", status)
		s.writeError(w, http.StatusBadGateway, "Couldn't send your answers. Try again in a moment.")
		return
	}

	if !req.ContactOnly && resp.Answers == "ok" && mode == k8s.DeploymentModeLocal {
		if err := pollStore.Update(func(st *poll.State) {
			st.SubmittedRound = round.ID
			st.SubmittedAt = now
		}); err != nil {
			log.Printf("[poll] Failed to record submission: %v", err)
		}
	}
	s.writeJSON(w, resp)
}

func forwardPollSubmission(ctx context.Context, body pollUpstreamRequest) (int, pollSubmitResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, pollSubmitResponse{}, err
	}
	// Detached from the browser request: a closed tab mid-send should not
	// leave it unknown whether the answers landed.
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, pollSubmitURL, bytes.NewReader(payload))
	if err != nil {
		return 0, pollSubmitResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("radar/%s", version.Current))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, pollSubmitResponse{}, err
	}
	defer res.Body.Close()
	var out pollSubmitResponse
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		data, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		if err != nil {
			return 0, pollSubmitResponse{}, err
		}
		if err := json.Unmarshal(data, &out); err != nil || out.Answers == "" {
			return 0, pollSubmitResponse{}, fmt.Errorf("unexpected response from the poll service")
		}
	}
	return res.StatusCode, out, nil
}
